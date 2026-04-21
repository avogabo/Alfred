package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/api"
	"github.com/avogabo/AlfredEDR/internal/config"
	"github.com/avogabo/AlfredEDR/internal/fusefs"
	"github.com/avogabo/AlfredEDR/internal/importer"
	"github.com/avogabo/AlfredEDR/internal/jobs"
	"github.com/avogabo/AlfredEDR/internal/library"
	"github.com/avogabo/AlfredEDR/internal/plex"
)

var rePercent = regexp.MustCompile(`\b(\d{1,3})%\b`)
var reArticlesProgress = regexp.MustCompile(`(?i)posted\s+(\d+)\s*/\s*(\d+)\s+articles?`)
var reArticlePostingProgress = regexp.MustCompile(`(?i)article posting progress:\s*(\d+)\s+read,\s*(\d+)\s+posted`)
var reFilesProgress = regexp.MustCompile(`(?i)(\d+)\s*/\s*(\d+)\s+files?`)
var reSeasonNum = regexp.MustCompile(`(?i)(?:season|temporada|s)\s*0*(\d{1,2})`)
var reEpisodeNum = regexp.MustCompile(`(?i)\b(?:s\d{1,2}e\d{1,2}|\d{1,2}x\d{1,2})\b`)

type Runner struct {
	jobs *jobs.Store

	UploadConcurrency int
	PollInterval      time.Duration
	Mode              string // "stub" or "exec" (dev)

	NgPostPath string // default: /usr/local/bin/ngpost
	NyuuPath   string // default: /usr/local/bin/nyuu

	GetConfig func() config.Config // optional live config provider
}

func New(j *jobs.Store) *Runner {
	return &Runner{jobs: j, UploadConcurrency: 1, PollInterval: 1 * time.Second, Mode: "stub", NgPostPath: "/usr/local/bin/ngpost", NyuuPath: "/usr/local/bin/nyuu"}
}

func (r *Runner) hasOtherRunningUpload(ctx context.Context, currentID string) bool {
	if r == nil || r.jobs == nil || r.jobs.DB() == nil || r.jobs.DB().SQL == nil {
		return false
	}
	var n int
	err := r.jobs.DB().SQL.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM jobs WHERE type=? AND state='running' AND id<>?`,
		string(jobs.TypeUpload), currentID,
	).Scan(&n)
	if err != nil {
		return false
	}
	return n > 0
}

func (r *Runner) Run(ctx context.Context) {
	semUpload := make(chan struct{}, r.UploadConcurrency)
	t := time.NewTicker(r.PollInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			job, err := r.jobs.ClaimNext(ctx)
			if err != nil {
				if err == jobs.ErrNoQueuedJobs {
					continue
				}
				continue
			}

			switch job.Type {
			case jobs.TypeUpload:
				// Safety guard: never run more than one media upload globally.
				// If another upload is already marked running (e.g. duplicate runner instance),
				// re-queue this job and continue.
				if r.hasOtherRunningUpload(ctx, job.ID) {
					_, _ = r.jobs.DB().SQL.ExecContext(ctx, `UPDATE jobs SET state='queued', updated_at=? WHERE id=?`, time.Now().Unix(), job.ID)
					continue
				}
				semUpload <- struct{}{}
				go func(j *jobs.Job) {
					defer func() { <-semUpload }()
					r.runUpload(ctx, j)
				}(job)
			default:
				go r.runImport(ctx, job)
			}
		}
	}
}

func (r *Runner) runImport(ctx context.Context, j *jobs.Job) {
	_ = r.jobs.AppendLog(ctx, j.ID, "starting import job")
	var p struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(j.Payload, &p)

	cfg := config.Default()
	if r.GetConfig != nil {
		cfg = r.GetConfig()
	}

	if cfg.AltMount.Enabled {
		relativePath, prepErr := prepareRelativePathForAltMount(ctx, cfg, r.jobs, j.ID, p.Path)
		if prepErr != nil {
			msg := prepErr.Error()
			_ = r.jobs.AppendLog(ctx, j.ID, "ERROR preparing rename/structure for AltMount: "+msg)
			_ = r.jobs.SetFailed(ctx, j.ID, msg)
			return
		}
		resp, err := api.DelegateImportToAltMount(ctx, cfg, p.Path, relativePath)
		if err != nil {
			msg := err.Error()
			_ = r.jobs.AppendLog(ctx, j.ID, "ERROR: "+msg)
			_ = r.jobs.SetFailed(ctx, j.ID, msg)
			return
		}
		if err := cleanupPreparedImport(ctx, r.jobs, j.ID); err != nil {
			_ = r.jobs.AppendLog(ctx, j.ID, "WARN cleanup prepared import: "+err.Error())
		}
		_ = r.jobs.AppendLog(ctx, j.ID, "delegated to AltMount")
		if relativePath != nil {
			_ = r.jobs.AppendLog(ctx, j.ID, "target path: "+*relativePath)
		}
		if resp != nil && resp.Data.Message != "" {
			_ = r.jobs.AppendLog(ctx, j.ID, resp.Data.Message)
		}
		_ = r.jobs.SetDone(ctx, j.ID)
		return
	}

	imp := importer.New(r.jobs)
	files, bytes, err := imp.ImportNZB(ctx, j.ID, p.Path)
	if err != nil {
		msg := err.Error()
		_ = r.jobs.AppendLog(ctx, j.ID, "ERROR: "+msg)
		_ = r.jobs.SetFailed(ctx, j.ID, msg)
		return
	}
	_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("imported NZB: files=%d total_bytes=%d", files, bytes))
	enrichCtx, cancelEnrich := context.WithTimeout(ctx, 120*time.Second)
	if err := imp.EnrichLibraryResolved(enrichCtx, cfg, j.ID); err != nil {
		_ = r.jobs.AppendLog(ctx, j.ID, "library_resolved: WARN: "+err.Error())
	}
	cancelEnrich()

	// Optional: ask Plex to refresh only the new item(s) in library-auto.
	if r.GetConfig != nil {
		cfg := r.GetConfig()
		if cfg.Plex.Enabled && cfg.Plex.RefreshOnImport {
			pc := plex.New(cfg.Plex.BaseURL, cfg.Plex.Token)
			if pc.Enabled() {
				paths, perr := fusefs.AutoVirtualPathsForImport(ctx, cfg, r.jobs, j.ID)
				if perr != nil {
					_ = r.jobs.AppendLog(ctx, j.ID, "plex: cannot build auto paths: "+perr.Error())
				} else {
					refreshed := 0
					for _, pth := range paths {
						plexPath := filepath.Join(cfg.Plex.PlexRoot, pth)
						// try directory first, then file path
						if err := pc.RefreshPath(ctx, plexPath, true); err != nil {
							_ = r.jobs.AppendLog(ctx, j.ID, "plex: refresh failed: "+err.Error())
						} else {
							refreshed++
						}
					}
					if refreshed > 0 {
						_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("plex: refresh ok (%d path(s))", refreshed))
					}
				}
			}
		}
	}

	_ = r.jobs.SetDone(ctx, j.ID)
}

func (r *Runner) runUpload(ctx context.Context, j *jobs.Job) {
	_ = r.jobs.AppendLog(ctx, j.ID, "starting upload job")
	_ = r.jobs.AppendLog(ctx, j.ID, "PHASE: Preparando (Preparing)")
	_ = r.jobs.AppendLog(ctx, j.ID, "PROGRESS: 0")
	var p struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(j.Payload, &p)

	if r.Mode == "exec" {
		cfg := config.Default()
		if r.GetConfig != nil {
			cfg = r.GetConfig()
		}
		ng := cfg.NgPost
		provider := strings.ToLower(strings.TrimSpace(cfg.Upload.Provider))
		if provider == "" {
			provider = "ngpost"
		}

		// If upload path is a directory with subdirectories, treat each subdirectory as an independent season pack.
		if st, err := os.Stat(p.Path); err == nil && st.IsDir() {
			entries, _ := os.ReadDir(p.Path)
			hasSubDir := false
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					hasSubDir = true
					break
				}
			}
			if hasSubDir {
				enq := 0
				for _, e := range entries {
					if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
						continue
					}
					sub := filepath.Join(p.Path, e.Name())
					if _, err := r.jobs.Enqueue(ctx, jobs.TypeUpload, map[string]string{"path": sub}); err == nil {
						enq++
					}
				}
				_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("directory pack detected; enqueued %d season subfolder job(s)", enq))
				_ = r.jobs.SetDone(ctx, j.ID)
				return
			}
		}

		outDir := ng.OutputDir
		if outDir == "" {
			outDir = "/host/inbox/nzb"
		}
		sourceGuess := library.GuessFromFilename(filepath.Base(p.Path))
		normalizedInputPath := p.Path
		if np, changed, nerr := maybeNormalizeWithFileBot(ctx, cfg, p.Path, func(line string) {
			_ = r.jobs.AppendLog(ctx, j.ID, line)
		}); nerr != nil {
			_ = r.jobs.AppendLog(ctx, j.ID, "filebot: WARN: "+nerr.Error())
		} else if changed {
			normalizedInputPath = np
			_ = r.jobs.AppendLog(ctx, j.ID, "filebot: normalized for naming -> "+filepath.Base(np))
		}
		finalNZB := buildRawNZBPath(cfg, normalizedInputPath, outDir, sourceGuess.Quality)
		// Use final NZB basename as canonical base name for staging and PAR naming.
		base := strings.TrimSuffix(filepath.Base(finalNZB), filepath.Ext(finalNZB))
		if strings.TrimSpace(base) == "" {
			base = strings.TrimSuffix(filepath.Base(normalizedInputPath), filepath.Ext(normalizedInputPath))
		}

		// IMPORTANT: write NZB to staging first so the import watcher never sees an incomplete NZB.
		cacheDir := cfg.Paths.CacheDir
		if strings.TrimSpace(cacheDir) == "" {
			cacheDir = "/cache"
		}
		stagingDir := filepath.Join(cacheDir, "nzb-staging")
		_ = os.MkdirAll(stagingDir, 0o755)
		stagingNZB := filepath.Join(stagingDir, fmt.Sprintf("%s-%s.nzb", base, j.ID))
		if st, err := os.Stat(finalNZB); err == nil && st.Size() > 0 {
			_ = r.jobs.AppendLog(ctx, j.ID, "nzb already exists at target path; skipping new upload to avoid duplicates: "+finalNZB)
			_ = r.jobs.SetDone(ctx, j.ID)
			return
		}

		lastProgress := -1
		emitProgress := func(p int) {
			if p < 0 {
				p = 0
			}
			if p > 100 {
				p = 100
			}
			if p == lastProgress {
				return
			}
			lastProgress = p
			_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("PROGRESS: %d", p))
		}
		scaleProgress := func(start, end, raw int) {
			if raw < 0 {
				raw = 0
			}
			if raw > 100 {
				raw = 100
			}
			if end < start {
				end = start
			}
			span := end - start
			emitProgress(start + (span*raw)/100)
		}
		lastPhase := ""
		emitPhase := func(p string) {
			p = strings.TrimSpace(p)
			if p == "" || p == lastPhase {
				return
			}
			lastPhase = p
			_ = r.jobs.AppendLog(ctx, j.ID, "PHASE: "+p)
		}

		parEnabled := cfg.Upload.Par.Enabled && cfg.Upload.Par.RedundancyPercent > 0
		combinedInputPath := p.Path
		combinedStagingDir := ""
		cleanupCombined := func() {}
		buildCombinedPayload := func() error {
			combinedStagingDir = filepath.Join(cacheDir, "upload-combined", j.ID)
			_ = os.RemoveAll(combinedStagingDir)
			if err := os.MkdirAll(combinedStagingDir, 0o755); err != nil {
				return err
			}
			if !parEnabled {
				emitPhase("Preparando payload (Preparing payload)")
				if err := cloneTreeFlatWithProgress(p.Path, combinedStagingDir, func(doneBytes, totalBytes int64) {
					if totalBytes > 0 {
						scaleProgress(0, 35, int((doneBytes*100)/totalBytes))
					}
				}); err != nil {
					return fmt.Errorf("preparing combined payload: %w", err)
				}
				combinedInputPath = combinedStagingDir
				emitProgress(35)
				cleanupCombined = func() {
					_ = os.RemoveAll(combinedStagingDir)
				}
				return nil
			}
			emitPhase("Generando PAR (Generating PAR)")
			parStagingDir, _, perr := generateParFiles(ctx, r.jobs, j.ID, cfg, p.Path, base)
			if perr != nil {
				return perr
			}
			if err := cloneTreeFlatWithProgress(p.Path, combinedStagingDir, func(doneBytes, totalBytes int64) {
				if totalBytes > 0 {
					scaleProgress(0, 15, int((doneBytes*100)/totalBytes))
				}
			}); err != nil {
				return fmt.Errorf("preparing combined payload: %w", err)
			}
			if err := cloneTreeFlatWithProgress(parStagingDir, combinedStagingDir, func(doneBytes, totalBytes int64) {
				if totalBytes > 0 {
					scaleProgress(35, 40, int((doneBytes*100)/totalBytes))
				}
			}); err != nil {
				return fmt.Errorf("preparing combined payload: %w", err)
			}
			combinedInputPath = combinedStagingDir
			emitProgress(40)
			cleanupCombined = func() {
				_ = os.RemoveAll(parStagingDir)
				_ = os.RemoveAll(combinedStagingDir)
			}
			_ = r.jobs.AppendLog(ctx, j.ID, "Payload combinado preparado: media + PAR2 en un único envío")
			return nil
		}

		if err := buildCombinedPayload(); err != nil {
			msg := err.Error()
			_ = r.jobs.AppendLog(ctx, j.ID, "ERROR: "+msg)
			_ = r.jobs.SetFailed(ctx, j.ID, msg)
			return
		}

		// Provider implementation
		if provider == "nyuu" {
			if ng.Enabled && ng.Host != "" && ng.User != "" && ng.Pass != "" && ng.Groups != "" {
				args := []string{"-h", ng.Host, "-P", fmt.Sprintf("%d", ng.Port)}
				if ng.SSL {
					args = append(args, "-S")
				}
				if ng.Connections > 0 {
					mediaConns := ng.Connections
					parConns := ng.Connections / 10
					if parConns < 1 { parConns = 1 }
					if parConns > 5 { parConns = 5 }
					if ng.Connections > 5 { mediaConns = ng.Connections - parConns }
					args = append(args, "-n", fmt.Sprintf("%d", mediaConns))
				}
				if ng.Groups != "" {
					args = append(args, "-g", ng.Groups)
				}
				// Obfuscation (safe for pipeline): randomize article metadata only.
				// Keep filename/yenc-name stable so downstream import/mount keeps working.
				args = append(args,
					"--subject", "${rand(40)} yEnc ({part}/{parts})",
					"--nzb-subject", `"{filename}" yEnc ({part}/{parts})`,
					"--message-id", "${rand(24)}-${rand(12)}@nyuu",
					"--from", "poster <poster@example.com>",
				)
				// NZB output (staging)
				args = append(args, "-o", stagingNZB, "-O")
				// Ask nyuu to emit periodic progress lines we can capture in job logs.
				args = append(args, "--progress", "log:15s")
				// Auth
				args = append(args, "-u", ng.User, "-p", ng.Pass)
				// Input file/dir (nyuu supports directories; keep subdirs)
				args = append(args, "-r", "keep")
				args = append(args, combinedInputPath)

				emitPhase("Subiendo a Usenet (Uploading)")
				emitProgress(40)
				_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("nyuu: %s %s", r.NyuuPath, strings.Join(args[:min(10, len(args))], " ")))
				err := runCommand(ctx, func(line string) {
					clean := sanitizeLine(line, ng.Pass)
					_ = r.jobs.AppendLog(ctx, j.ID, clean)
					if m := rePercent.FindStringSubmatch(clean); len(m) == 2 {
						if n, e := strconv.Atoi(m[1]); e == nil && n >= 0 && n <= 100 {
							scaleProgress(40, 98, n)
						}
					}
					if m := reArticlesProgress.FindStringSubmatch(clean); len(m) == 3 {
						if done, e1 := strconv.Atoi(m[1]); e1 == nil {
							if total, e2 := strconv.Atoi(m[2]); e2 == nil && total > 0 {
								emitProgress((done * 100) / total)
							}
						}
					}
					if m := reArticlePostingProgress.FindStringSubmatch(clean); len(m) == 3 {
						if readN, e1 := strconv.Atoi(m[1]); e1 == nil {
							if postedN, e2 := strconv.Atoi(m[2]); e2 == nil {
								den := readN
								if den < postedN {
									den = postedN
								}
								if den > 0 {
									p := (postedN * 100) / den
									if p >= 0 && p <= 100 {
										scaleProgress(40, 98, p)
									}
								}
							}
						}
					}
					if m := reFilesProgress.FindStringSubmatch(clean); len(m) == 3 {
						if done, e1 := strconv.Atoi(m[1]); e1 == nil {
							if total, e2 := strconv.Atoi(m[2]); e2 == nil && total > 0 {
								p := (done * 100) / total
								if p > 0 {
									scaleProgress(40, 98, p)
								}
							}
						}
					}
				}, r.NyuuPath, args...)
				if err != nil {
					msg := err.Error()
					_ = r.jobs.AppendLog(ctx, j.ID, "ERROR: "+msg)
					_ = r.jobs.SetFailed(ctx, j.ID, msg)
					return
				}
				if provider == "nyuu" {
					// Move staging NZB into the watched NZB inbox only after the uploader has finished.
					emitPhase("Moviendo NZB a NZB inbox (Move to NZB inbox)")
					emitProgress(99)
					_, err = moveNZBStagingToFinal(stagingNZB, finalNZB)
					if err != nil {
						msg := err.Error()
						_ = r.jobs.AppendLog(ctx, j.ID, "ERROR: move nzb: "+msg)
						_ = r.jobs.SetFailed(ctx, j.ID, msg)
						return
					}
					emitProgress(100)
					cleanupCombined()

					_ = r.jobs.SetDone(ctx, j.ID)
					// Import is handled by the NZB watcher (watch.nzb). We just drop the NZB into the inbox.
					return
				}
			}
			if ng.Enabled && provider == "nyuu" {
				_ = r.jobs.AppendLog(ctx, j.ID, "nyuu selected but missing config fields (need host/user/pass/groups)")
			}
		}
		if provider != "nyuu" {
			// Default: ngpost
			if ng.Enabled && ng.Host != "" && ng.User != "" && ng.Pass != "" && ng.Groups != "" {
				args := []string{"-i", combinedInputPath, "-o", stagingNZB, "-h", ng.Host, "-P", fmt.Sprintf("%d", ng.Port)}
				if ng.SSL {
					args = append(args, "-s")
				}
				if ng.Connections > 0 {
					mediaConns := ng.Connections
					parConns := ng.Connections / 10
					if parConns < 1 { parConns = 1 }
					if parConns > 5 { parConns = 5 }
					if ng.Connections > 5 { mediaConns = ng.Connections - parConns }
					args = append(args, "-n", fmt.Sprintf("%d", mediaConns))
				}
				if ng.Threads > 0 {
					args = append(args, "-t", fmt.Sprintf("%d", ng.Threads))
				}
				if ng.Groups != "" {
					args = append(args, "-g", ng.Groups)
				}
				if ng.Obfuscate {
					args = append(args, "-x")
				}
				if ng.TmpDir != "" {
					args = append(args, "--tmp_dir", ng.TmpDir)
				}
				args = append(args, "-u", ng.User, "-p", ng.Pass, "--disp_progress", "files")

				emitPhase("Subiendo a Usenet (Uploading)")
				emitProgress(1)
				_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("ngpost: %s %s", r.NgPostPath, strings.Join(args[:min(10, len(args))], " ")))

				// ngpost sometimes auto-renames the NZB if the requested output already exists.
				// We capture the actual nzb path from its output (line like: "nzb file: /path/file_2.nzb").
				actualNZB := ""
				err := runCommand(ctx, func(line string) {
					clean := sanitizeLine(line, ng.Pass)
					_ = r.jobs.AppendLog(ctx, j.ID, clean)
					if m := rePercent.FindStringSubmatch(clean); len(m) == 2 {
						if n, e := strconv.Atoi(m[1]); e == nil && n >= 0 && n <= 100 {
							emitProgress(n)
						}
					}
					l := strings.ToLower(clean)
					if idx := strings.Index(l, "nzb file:"); idx >= 0 {
						p := strings.TrimSpace(clean[idx+len("nzb file:"):])
						if strings.HasSuffix(strings.ToLower(p), ".nzb") {
							actualNZB = p
						}
					}
				}, r.NgPostPath, args...)
				if err != nil {
					msg := err.Error()
					_ = r.jobs.AppendLog(ctx, j.ID, "ERROR: "+msg)
					_ = r.jobs.SetFailed(ctx, j.ID, msg)
					return
				}
				// ngpost sometimes auto-renames the NZB. Prefer the actual produced staging path.
				produced := stagingNZB
				if actualNZB != "" {
					produced = actualNZB
				}
				emitPhase("Moviendo NZB a NZB inbox (Move to NZB inbox)")
				emitProgress(99)
				_, err = moveNZBStagingToFinal(produced, finalNZB)
				if err != nil {
					msg := err.Error()
					_ = r.jobs.AppendLog(ctx, j.ID, "ERROR: move nzb: "+msg)
					_ = r.jobs.SetFailed(ctx, j.ID, msg)
					return
				}
				emitProgress(100)
				cleanupCombined()
				_ = r.jobs.SetDone(ctx, j.ID)
				// Import is handled by the NZB watcher (watch.nzb). We just drop the NZB into the inbox.
				return
			}
			if ng.Enabled {
				_ = r.jobs.AppendLog(ctx, j.ID, "ngpost enabled but missing config fields (need host/user/pass/groups)")
			}
		}

		_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("exec upload (dev dummy): %s", p.Path))
		err := runCommand(ctx, func(line string) {
			_ = r.jobs.AppendLog(ctx, j.ID, line)
		}, "bash", "-lc", fmt.Sprintf("echo uploading '%s'; sleep 2; echo done upload", p.Path))
		if err != nil {
			msg := err.Error()
			_ = r.jobs.AppendLog(ctx, j.ID, "ERROR: "+msg)
			_ = r.jobs.SetFailed(ctx, j.ID, msg)
			return
		}
		_ = r.jobs.SetDone(ctx, j.ID)
		return
	}

	_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("(stub) would upload media via ngpost: %s", p.Path))
	_ = r.jobs.SetDone(ctx, j.ID)
}

// moveNZBStagingToFinal moves a staging NZB into the RAW directory only after it is complete.
// It tries to behave atomically at the destination by writing to a temp file then renaming.
func moveNZBStagingToFinal(stagingPath, finalPath string) (string, error) {
	if strings.TrimSpace(stagingPath) == "" || strings.TrimSpace(finalPath) == "" {
		return "", fmt.Errorf("staging and final paths required")
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return "", err
	}

	// Choose a unique final path if it already exists.
	dest := finalPath
	if _, err := os.Stat(dest); err == nil {
		ext := filepath.Ext(finalPath)
		base := strings.TrimSuffix(finalPath, ext)
		for i := 2; i < 1000; i++ {
			cand := fmt.Sprintf("%s_%d%s", base, i, ext)
			if _, err := os.Stat(cand); os.IsNotExist(err) {
				dest = cand
				break
			}
		}
	}

	// Best effort atomic move. If cross-device, do copy+rename.
	if err := os.Rename(stagingPath, dest); err == nil {
		return dest, nil
	} else {
		// Copy to tmp in destination dir, then rename.
		tmp := dest + ".tmp"
		_ = os.Remove(tmp)
		if err := copyFile(stagingPath, tmp); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, dest); err != nil {
			return "", err
		}
		_ = os.Remove(stagingPath)
		return dest, nil
	}
}

func cloneTreeFlat(src, dst string) error {
		return cloneTreeFlatWithProgress(src, dst, nil)
}

func cloneTreeFlatWithProgress(src, dst string, onProgress func(doneBytes, totalBytes int64)) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if !st.IsDir() {
		_, err := copyFileWithProgress(src, filepath.Join(dst, filepath.Base(src)), onProgress)
		return err
	}
	seen := map[string]string{}
	var files []string
	var totalBytes int64
	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		if prev, ok := seen[name]; ok {
			return fmt.Errorf("flat payload collision for %q (%s, %s)", name, prev, path)
		}
		seen[name] = path
		files = append(files, path)
		totalBytes += info.Size()
		return nil
	}); err != nil {
		return err
	}
	var doneBytes int64
	for _, path := range files {
		name := filepath.Base(path)
		written, err := copyFileWithProgress(path, filepath.Join(dst, name), func(written, _ int64) {
			if onProgress != nil {
				onProgress(doneBytes+written, totalBytes)
			}
		})
		if err != nil {
			return err
		}
		doneBytes += written
		if onProgress != nil {
			onProgress(doneBytes, totalBytes)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	_, err := copyFileWithProgress(src, dst, nil)
	return err
}

func copyFileWithProgress(src, dst string, onProgress func(written, total int64)) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	st, err := in.Stat()
	if err != nil {
		return 0, err
	}
	total := st.Size()

	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = out.Close()
	}()

	buf := make([]byte, 1024*1024)
	var written int64
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			wn, writeErr := out.Write(buf[:n])
			written += int64(wn)
			if onProgress != nil {
				onProgress(written, total)
			}
			if writeErr != nil {
				return written, writeErr
			}
			if wn != n {
				return written, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return written, readErr
		}
	}
	if err := out.Sync(); err != nil {
		return written, err
	}
	return written, out.Close()
}

func detectSeasonFromName(name string) int {
	m := reSeasonNum.FindStringSubmatch(name)
	if len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 0 {
			return n
		}
	}
	return -1
}

func stripSeasonFromName(name string) string {
	clean := reSeasonNum.ReplaceAllString(name, "")
	clean = strings.ReplaceAll(clean, "()", "")
	clean = strings.Join(strings.Fields(clean), " ")
	clean = strings.Trim(clean, " -_.")
	return clean
}

func detectSeasonFromDir(path string) int {
	base := filepath.Base(path)
	if n := detectSeasonFromName(base); n >= 0 {
		return n
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return -1
	}
	for _, e := range entries {
		if e.IsDir() {
			if n := detectSeasonFromName(e.Name()); n >= 0 {
				return n
			}
			continue
		}
		if n := detectSeasonFromName(e.Name()); n >= 0 {
			return n
		}
		if m := reEpisodeNum.FindString(e.Name()); m != "" {
			if strings.Contains(strings.ToLower(m), "x") {
				parts := strings.Split(strings.ToLower(m), "x")
				if len(parts) == 2 {
					if n, err := strconv.Atoi(parts[0]); err == nil && n >= 0 {
						return n
					}
				}
			} else if strings.HasPrefix(strings.ToLower(m), "s") {
				m = strings.TrimPrefix(strings.ToLower(m), "s")
				if idx := strings.Index(m, "e"); idx > 0 {
					if n, err := strconv.Atoi(m[:idx]); err == nil && n >= 0 {
						return n
					}
				}
			}
		}
	}
	return -1
}

func buildRawNZBPath(cfg config.Config, inputPath, rawRoot, qualityHint string) string {
	if strings.TrimSpace(rawRoot) == "" {
		rawRoot = "/host/inbox/nzb"
	}
	base := filepath.Base(inputPath)
	g := library.GuessFromFilename(base)
	// normalize quality to 1080/2160
	q := strings.ToLower(strings.TrimSpace(qualityHint))
	if q == "" {
		q = strings.ToLower(strings.TrimSpace(g.Quality))
	}
	quality := "1080"
	if q == "4k" || strings.Contains(q, "2160") {
		quality = "2160"
	}

	// helpers
	safe := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.ReplaceAll(s, string(filepath.Separator), "-")
		s = strings.ReplaceAll(s, ":", "-")
		s = strings.Join(strings.Fields(s), " ")
		return s
	}

	l := cfg.Library.Defaults()

	// If inputPath is a directory, treat it as series content (season pack or series folder).
	isDir := false
	if st, err := os.Stat(inputPath); err == nil {
		isDir = st.IsDir()
	}
	if isDir {
		g.IsSeries = true
	}

	if g.IsSeries {
		seriesTitle := strings.TrimSpace(g.Title)
		if isDir {
			baseName := filepath.Base(inputPath)
			seriesTitle = strings.TrimSpace(stripSeasonFromName(baseName))
			if seriesTitle == "" {
				parent := filepath.Base(filepath.Dir(inputPath))
				pg := library.GuessFromFilename(parent)
				seriesTitle = strings.TrimSpace(pg.Title)
				if g.Year <= 0 && pg.Year > 0 {
					g.Year = pg.Year
				}
			}
		}
		if seriesTitle == "" {
			seriesTitle = g.Title
		}
		// avoid duplicated year in nzb names, e.g. "3 caminos (2021) (2021)"
		if g.Year > 0 {
			seriesTitle = strings.TrimSpace(strings.ReplaceAll(seriesTitle, fmt.Sprintf("(%d)", g.Year), ""))
		}
		seriesName := safe(seriesTitle)
		year := g.Year
		if year <= 0 {
			res := library.NewResolver(cfg)
			if tv, ok := res.ResolveTV(context.Background(), seriesName, 0); ok {
				if y := tv.FirstAirYear(); y > 0 {
					year = y
				}
			}
		}
		yearPart := ""
		if year > 0 {
			if !strings.Contains(strings.ToLower(seriesName), fmt.Sprintf("(%d)", year)) {
				yearPart = fmt.Sprintf(" (%d)", year)
			}
		}
		initial := library.InitialFolder(seriesName)
		if strings.TrimSpace(initial) == "" || len([]rune(initial)) != 1 || (initial[0] < 'A' || initial[0] > 'Z') {
			initial = "#"
		}
		seriesFolder := safe(seriesName + yearPart)

		fileName := ""
		if isDir {
			season := detectSeasonFromDir(inputPath)
			if season < 0 {
				season = detectSeasonFromName(filepath.Base(inputPath))
			}
			if season >= 0 {
				fileName = fmt.Sprintf("%s%s Temporada %02d.nzb", safe(seriesName), yearPart, season)
			} else {
				fileName = fmt.Sprintf("%s%s Pack.nzb", safe(seriesName), yearPart)
			}
		} else if g.Season > 0 && g.Episode > 0 {
			fileName = fmt.Sprintf("%s%s %02dx%02d.nzb", safe(seriesName), yearPart, g.Season, g.Episode)
		} else {
			fileName = fmt.Sprintf("%s%s.nzb", safe(seriesName), yearPart)
		}

		// NZB layout for series: SERIES/A/.../Serie (Año)/<file>.nzb
		rel := filepath.Join(l.SeriesRoot, initial, seriesFolder, fileName)
		if cfg.Library.UppercaseFolders {
			rel = library.ApplyUppercaseFolders(rel)
		}
		return filepath.Join(rawRoot, rel)
	}

	movieTitle := safe(g.Title)
	year := g.Year
	yearPart := ""
	if year > 0 {
		// Avoid duplicating year when title already includes "(YYYY)".
		if !strings.Contains(strings.ToLower(movieTitle), fmt.Sprintf("(%d)", year)) {
			yearPart = fmt.Sprintf(" (%d)", year)
		}
	}
	movieFolder := safe(movieTitle + yearPart)
	fileName := movieFolder + ".nzb"

	initial := library.InitialFolder(movieTitle)
	if strings.TrimSpace(initial) == "" || len([]rune(initial)) != 1 || (initial[0] < 'A' || initial[0] > 'Z') {
		initial = "#"
	}
	// NZB files: keep them directly under .../<Initial>/ (no extra movie folder).
	// The FUSE/library view can still expose movie folders for MKVs.
	rel := filepath.Join(l.MoviesRoot, quality, initial, fileName)
	if cfg.Library.UppercaseFolders {
		rel = library.ApplyUppercaseFolders(rel)
	}
	return filepath.Join(rawRoot, rel)
}
