package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/config"
	"github.com/avogabo/AlfredEDR/internal/jobs"
)

func prepareParLocalInput(inputPath string, localRoot string) (string, int, error) {
	st, err := os.Stat(inputPath)
	if err != nil {
		return "", 0, err
	}
	if !st.IsDir() {
		dst := filepath.Join(localRoot, filepath.Base(inputPath))
		if err := copyFilePreserve(inputPath, dst); err != nil {
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
		rel, err := filepath.Rel(inputPath, src)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstRoot, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if err := copyFilePreserve(src, dst); err != nil {
			return err
		}
		filesCopied++
		return nil
	}); err != nil {
		return "", filesCopied, err
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
	parStagingDir := filepath.Join(cacheDir, "par-staging", j.ID)
	_ = os.MkdirAll(parStagingDir, 0o755)
	defer os.RemoveAll(parStagingDir)

	parBase := filepath.Join(parStagingDir, p.BaseName)
	args := []string{"c", fmt.Sprintf("-r%d", cfg.Upload.Par.RedundancyPercent)}

	workInputPath := p.InputPath
	cleanupPath := ""
	if strings.EqualFold(strings.TrimSpace(cfg.Upload.Par.MediaPathMode), "rclone") {
		localRoot := filepath.Join(cacheDir, "par-input", j.ID)
		_ = os.MkdirAll(localRoot, 0o755)
		cleanupPath = localRoot
		_ = r.jobs.AppendLog(ctx, j.ID, "media_path_mode=rclone; copiando input a cache local antes de generar PAR")
		copiedPath, copiedCount, copyErr := prepareParLocalInput(p.InputPath, localRoot)
		if copyErr != nil {
			_ = os.RemoveAll(localRoot)
			_ = r.jobs.SetFailed(ctx, j.ID, "failed to prepare local PAR input: "+copyErr.Error())
			return
		}
		workInputPath = copiedPath
		_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("copied %d file(s) to local cache: %s", copiedCount, copiedPath))
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
			files = append(files, fp)
			return nil
		})
		if len(files) == 0 {
			_ = r.jobs.SetFailed(ctx, j.ID, "par2: no files found in directory")
			return
		} else {
			args = append(args, "-B/", parBase+".par2")
			args = append(args, files...)
		}
	} else {
		args = append(args, "-B/", parBase+".par2", workInputPath)
	}

	tickDone := make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		p := 1
		for {
			select {
			case <-tickDone:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if p < 50 {
					p++
					_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("PROGRESS: %d", p/2))
				}
			}
		}
	}()

	err := runCommand(ctx, func(line string) {
		clean := strings.TrimSpace(line)
		if m := rePercent.FindStringSubmatch(clean); len(m) == 2 {
			if n, e := strconv.Atoi(m[1]); e == nil && n >= 0 && n <= 100 {
				_ = r.jobs.AppendLog(ctx, j.ID, fmt.Sprintf("PROGRESS: %d", n/2))
			}
			return
		}
		if clean != "" {
			_ = r.jobs.AppendLog(ctx, j.ID, clean)
		}
	}, "par2", args...)

	close(tickDone)

	if err != nil {
		_ = r.jobs.SetFailed(ctx, j.ID, "par2create failed: "+err.Error())
		return
	}

	// Phase 2: Upload PAR2
	_ = r.jobs.AppendLog(ctx, j.ID, "PHASE: Subiendo PAR (Uploading PAR)")
	ng := cfg.NgPost
	if !ng.Enabled || ng.Host == "" || ng.User == "" || ng.Pass == "" || ng.Groups == "" {
		_ = r.jobs.SetFailed(ctx, j.ID, "ngpost/nyuu config missing or disabled")
		return
	}

	entries, err := os.ReadDir(parStagingDir)
	if err != nil {
		_ = r.jobs.SetFailed(ctx, j.ID, "read par staging dir error: "+err.Error())
		return
	}

	var parFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), p.BaseName) && strings.HasSuffix(e.Name(), ".par2") {
			parFiles = append(parFiles, filepath.Join(parStagingDir, e.Name()))
		}
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
