package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/config"
	"github.com/avogabo/AlfredEDR/internal/jobs"
)

func prepareParLocalInput(inputPath string, localRoot string, onProgress func(doneBytes, totalBytes int64)) (string, int, error) {
	st, err := os.Stat(inputPath)
	if err != nil {
		return "", 0, err
	}
	if !st.IsDir() {
		dst := filepath.Join(localRoot, filepath.Base(inputPath))
		if _, err := copyFileWithProgress(inputPath, dst, func(written, total int64) {
			if onProgress != nil {
				onProgress(written, total)
			}
		}); err != nil {
			return "", 0, err
		}
		return dst, 1, nil
	}
	filesCopied := 0
	baseName := filepath.Base(inputPath)
	if baseName == "." || baseName == "/" || baseName == "" {
		baseName = "input"
	}
	dstRoot := filepath.Join(localRoot, baseName)
	var files []string
	var totalBytes int64
	if err := filepath.WalkDir(inputPath, func(src string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil {
			return walkErr
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			rel, err := filepath.Rel(inputPath, src)
			if err != nil {
				return err
			}
			return os.MkdirAll(filepath.Join(dstRoot, rel), 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, src)
		totalBytes += info.Size()
		return nil
	}); err != nil {
		return "", filesCopied, err
	}
	var doneBytes int64
	for _, src := range files {
		rel, err := filepath.Rel(inputPath, src)
		if err != nil {
			return "", filesCopied, err
		}
		dst := filepath.Join(dstRoot, rel)
		written, err := copyFileWithProgress(src, dst, func(written, _ int64) {
			if onProgress != nil {
				onProgress(doneBytes+written, totalBytes)
			}
		})
		if err != nil {
			return "", filesCopied, err
		}
		doneBytes += written
		filesCopied++
		if onProgress != nil {
			onProgress(doneBytes, totalBytes)
		}
	}
	return dstRoot, filesCopied, nil
}

func copyFilePreserve(src string, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(dst, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dst, time.Now(), st.ModTime())
}

func effectiveMediaPathMode(inputPath string, configured string) string {
	mode := strings.ToLower(strings.TrimSpace(configured))
	if mode != "" && mode != "auto" {
		return mode
	}
	if st, err := os.Stat(inputPath); err == nil {
		_ = st
		if out, err := runCommandOutput("stat", "-f", "-c", "%T", inputPath); err == nil {
			fsType := strings.ToLower(strings.TrimSpace(out))
			if strings.Contains(fsType, "fuse") || strings.Contains(fsType, "rclone") {
				return "rclone"
			}
		}
	}
	return "local"
}

func runCommandOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	b, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func collectParStagingFiles(parStagingDir string, baseName string) ([]string, error) {
	entries, err := os.ReadDir(parStagingDir)
	if err != nil {
		return nil, err
	}
	var parFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), baseName) && strings.HasSuffix(e.Name(), ".par2") {
			parFiles = append(parFiles, filepath.Join(parStagingDir, e.Name()))
		}
	}
	return parFiles, nil
}

func generateParFiles(ctx context.Context, jobsStore *jobs.Store, jobID string, cfg config.Config, inputPath string, baseName string, emitProgress func(int)) (string, []string, error) {
	cacheDir := cfg.Paths.CacheDir
	if strings.TrimSpace(cacheDir) == "" {
		cacheDir = "/cache"
	}
	parStagingDir := filepath.Join(cacheDir, "par-staging", jobID)
	if err := os.MkdirAll(parStagingDir, 0o755); err != nil {
		return "", nil, err
	}
	parBase := filepath.Join(parStagingDir, baseName)
	args := []string{"c", fmt.Sprintf("-r%d", cfg.Upload.Par.RedundancyPercent), "-B", parStagingDir}

	workInputPath := inputPath
	cleanupPath := ""
	mediaPathMode := effectiveMediaPathMode(inputPath, cfg.Upload.Par.MediaPathMode)
	if strings.EqualFold(mediaPathMode, "rclone") {
		localRoot := filepath.Join(cacheDir, "par-input", jobID)
		_ = os.MkdirAll(localRoot, 0o755)
		cleanupPath = localRoot
		if jobsStore != nil {
			_ = jobsStore.AppendLog(ctx, jobID, "media_path_mode=rclone(auto); copiando input a cache local antes de generar PAR")
		}
		lastCopyProgress := -1
		copiedPath, copiedCount, copyErr := prepareParLocalInput(inputPath, localRoot, func(doneBytes, totalBytes int64) {
			if totalBytes > 0 {
				p := int((doneBytes * 100) / totalBytes)
				if p != lastCopyProgress {
					lastCopyProgress = p
					if emitProgress != nil {
						emitProgress(p)
					} else if jobsStore != nil {
						_ = jobsStore.AppendLog(ctx, jobID, fmt.Sprintf("PROGRESS: %d", p))
					}
				}
			}
		})
		if copyErr != nil {
			_ = os.RemoveAll(localRoot)
			return "", nil, fmt.Errorf("failed to prepare local PAR input: %w", copyErr)
		}
		workInputPath = copiedPath
		if jobsStore != nil {
			_ = jobsStore.AppendLog(ctx, jobID, fmt.Sprintf("copied %d file(s) to local cache: %s", copiedCount, copiedPath))
			_ = jobsStore.AppendLog(ctx, jobID, "PHASE: Generando PAR (Generating PAR)")
		}
	}
	if cleanupPath != "" {
		defer os.RemoveAll(cleanupPath)
	}

	if st, err := os.Stat(workInputPath); err == nil && st.IsDir() {
		files := make([]string, 0, 64)
		_ = filepath.WalkDir(workInputPath, func(fp string, d os.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			dst := filepath.Join(parStagingDir, filepath.Base(fp))
			if err := copyFile(fp, dst); err != nil {
				return err
			}
			files = append(files, dst)
			return nil
		})
		if len(files) == 0 {
			return "", nil, fmt.Errorf("par2: no files found in directory")
		}
		args = append(args, parBase+".par2")
		args = append(args, files...)
	} else {
		dst := filepath.Join(parStagingDir, filepath.Base(workInputPath))
		if err := copyFile(workInputPath, dst); err != nil {
			return "", nil, err
		}
		args = append(args, parBase+".par2", dst)
	}

	tickDone := make(chan struct{})
	go func() {
		t := time.NewTicker(8 * time.Second)
		defer t.Stop()
		last := -1
		for {
			select {
			case <-tickDone:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if emitProgress != nil && last >= 0 && last < 99 {
					next := last + 1
					last = next
					emitProgress(next)
				}
			}
		}
	}()

	err := runCommand(ctx, func(line string) {
		clean := strings.TrimSpace(line)
		if m := rePercent.FindStringSubmatch(clean); len(m) == 2 {
			if n, e := strconv.Atoi(m[1]); e == nil && n >= 0 && n <= 100 {
				if emitProgress != nil {
					emitProgress(n)
				}
			}
			return
		}
		if clean != "" && jobsStore != nil {
			_ = jobsStore.AppendLog(ctx, jobID, clean)
		}
	}, "par2", args...)
	close(tickDone)
	if err != nil {
		return parStagingDir, nil, fmt.Errorf("par2create failed: %w", err)
	}
	parFiles, err := collectParStagingFiles(parStagingDir, baseName)
	if err != nil {
		return parStagingDir, nil, err
	}
	if len(parFiles) == 0 {
		return parStagingDir, nil, fmt.Errorf("no par2 files generated")
	}
	return parStagingDir, parFiles, nil
}

func (r *Runner) runUploadParNZB(ctx context.Context, j *jobs.Job) {
	_ = r.jobs.AppendLog(ctx, j.ID, "starting PAR2 generation and upload job")
	var p struct {
		InputPath string `json:"input_path"`
		BaseName  string `json:"base_name"`
		FinalDir  string `json:"final_dir"`
	}
	_ = json.Unmarshal(j.Payload, &p)

	if p.InputPath == "" || p.BaseName == "" || p.FinalDir == "" {
		_ = r.jobs.SetFailed(ctx, j.ID, "input_path, base_name and final_dir required")
		return
	}

	cfg := config.Default()
	if r.GetConfig != nil {
		cfg = r.GetConfig()
	}

	parEnabled := cfg.Upload.Par.Enabled && cfg.Upload.Par.RedundancyPercent > 0
	if !parEnabled {
		_ = r.jobs.AppendLog(ctx, j.ID, "par generation is disabled in config")
		_ = r.jobs.SetDone(ctx, j.ID)
		return
	}

	cacheDir := cfg.Paths.CacheDir
	if strings.TrimSpace(cacheDir) == "" {
		cacheDir = "/cache"
	}

	// Phase 1: Generate PAR2
	_ = r.jobs.AppendLog(ctx, j.ID, "PHASE: Generando PAR (Generating PAR)")
	parStagingDir, parFiles, err := generateParFiles(ctx, r.jobs, j.ID, cfg, p.InputPath, p.BaseName, func(raw int) {
		_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("PROGRESS: %d", raw))
	})
	defer os.RemoveAll(parStagingDir)
	if err != nil {
		_ = r.jobs.SetFailed(ctx, j.ID, err.Error())
		return
	}

	// Phase 2: Upload PAR2
	_ = r.jobs.AppendLog(ctx, j.ID, "PHASE: Subiendo PAR (Uploading PAR)")
	ng := cfg.NgPost
	if !ng.Enabled || ng.Host == "" || ng.User == "" || ng.Pass == "" || ng.Groups == "" {
		_ = r.jobs.SetFailed(ctx, j.ID, "ngpost/nyuu config missing or disabled")
		return
	}

	if len(parFiles) == 0 {
		_ = r.jobs.AppendLog(ctx, j.ID, "no par2 files generated")
		_ = r.jobs.SetDone(ctx, j.ID)
		return
	}

	stagingNZB := filepath.Join(cacheDir, "nzb-staging", fmt.Sprintf("%s.par-%s.nzb", p.BaseName, j.ID))
	_ = os.MkdirAll(filepath.Dir(stagingNZB), 0o755)

	uArgs := []string{"-h", ng.Host, "-P", fmt.Sprintf("%d", ng.Port)}
	if ng.SSL {
		uArgs = append(uArgs, "-S")
	}
	if ng.Connections > 0 {
		parConns := ng.Connections / 10
		if parConns < 1 {
			parConns = 1
		}
		if parConns > 5 {
			parConns = 5
		}
		uArgs = append(uArgs, "-n", fmt.Sprintf("%d", parConns))
	}
	if ng.Groups != "" {
		uArgs = append(uArgs, "-g", ng.Groups)
	}

	uArgs = append(uArgs,
		"--subject", p.BaseName+" PAR2 yEnc ({part}/{parts})",
		"--nzb-subject", `"{filename}" yEnc ({part}/{parts})`,
		"--message-id", "${rand(24)}-${rand(12)}@nyuu",
		"--from", "poster <poster@example.com>",
	)
	uArgs = append(uArgs, "-o", stagingNZB, "-O")
	uArgs = append(uArgs, "-u", ng.User, "-p", ng.Pass)

	// Pass the staging directory directly to nyuu so it uploads all files inside it
	uArgs = append(uArgs, "-r", "keep")
	uArgs = append(uArgs, parStagingDir)

	// Let's add extra logging to see EXACTLY what it's running
	_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("Nyuu args: %v", uArgs))

	err = runCommand(ctx, func(line string) {
		clean := sanitizeLine(line, ng.Pass)
		if m := rePercent.FindStringSubmatch(clean); len(m) == 2 {
			if n, e := strconv.Atoi(m[1]); e == nil && n >= 0 && n <= 100 {
				_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("PROGRESS: %d", 50+(n/2)))
			}
			return
		}
		if strings.TrimSpace(clean) != "" {
			_ = r.jobs.AppendLog(ctx, j.ID, clean)
		}
	}, r.NyuuPath, uArgs...)

	if err != nil {
		_ = r.jobs.SetFailed(ctx, j.ID, "nyuu upload failed: "+err.Error())
		return
	}

	// Phase 3: Move NZB and Cleanup
	_ = r.jobs.AppendLog(ctx, j.ID, "PHASE: Moviendo NZB de PAR (Move PAR NZB)")

	_ = os.MkdirAll(p.FinalDir, 0o755)
	finalNZB := filepath.Join(p.FinalDir, p.BaseName+".par.nzb")

	_, err = moveNZBStagingToFinal(stagingNZB, finalNZB)
	if err != nil {
		_ = r.jobs.SetFailed(ctx, j.ID, "failed to move par nzb: "+err.Error())
		return
	}

	_ = r.jobs.AppendLog(ctx, j.ID, "created "+finalNZB)

	deleted := 0
	for _, pf := range parFiles {
		if err := os.Remove(pf); err == nil {
			deleted++
		}
	}
	_ = os.RemoveAll(parStagingDir)
	_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("deleted %d local par2 files and staging dir", deleted))

	_ = r.jobs.AppendLog(ctx, j.ID, "PROGRESS: 100")
	_ = r.jobs.SetDone(ctx, j.ID)
}
