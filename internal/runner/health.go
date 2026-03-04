package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/config"
	"github.com/avogabo/AlfredEDR/internal/importer"
	"github.com/avogabo/AlfredEDR/internal/nntp"
	"github.com/avogabo/AlfredEDR/internal/nzb"
	"github.com/avogabo/AlfredEDR/internal/yenc"
)

type healthRepairPayload struct {
	Path string `json:"path"`
}

func (r *Runner) runHealthRepair(ctx context.Context, jobID string, cfg config.Config, payload healthRepairPayload) (retErr error) {
	if !cfg.Health.Enabled {
		return errors.New("health repair: disabled by config (health.enabled=false)")
	}

	nzbPath := strings.TrimSpace(payload.Path)
	if nzbPath == "" {
		return errors.New("health repair: payload.path required")
	}
	_ = r.upsertHealthState(ctx, nzbPath, "repairing", time.Now().Unix(), 0, "", jobID)
	defer func() {
		if retErr != nil {
			_ = r.upsertHealthState(ctx, nzbPath, "error", 0, 0, retErr.Error(), jobID)
			return
		}
		now := time.Now().Unix()
		_ = r.upsertHealthState(ctx, nzbPath, "repaired", now, now, "", jobID)
	}()

	// Cross-node coordination: lock file next to NZB (sidecar), so shared RAW trees don't double-repair.
	lockPath := nzbPath + ".health.lock"
	ttl := time.Duration(cfg.Health.Lock.LockTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	if err := acquireHealthLock(lockPath, ttl); err != nil {
		return fmt.Errorf("health repair: %w", err)
	}
	defer func() { _ = os.Remove(lockPath) }()

	workDir := filepath.Join("/cache", "health", jobID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}

	baseName := filepath.Base(nzbPath)
	if !strings.HasSuffix(strings.ToLower(baseName), ".nzb") {
		baseName += ".nzb"
	}

	_ = r.jobs.AppendLog(ctx, jobID, fmt.Sprintf("health: workdir=%s", workDir))
	_ = r.jobs.AppendLog(ctx, jobID, fmt.Sprintf("health: nzb=%s", nzbPath))

	// Copy NZB into workdir (avoid symlink surprises when we later replace it)
	workNZB := filepath.Join(workDir, baseName)
	_ = os.Remove(workNZB)
	if b, err := os.ReadFile(nzbPath); err == nil {
		_ = os.WriteFile(workNZB, b, 0o644)
	} else {
		return fmt.Errorf("read nzb: %w", err)
	}

	// Parse NZB (we only handle a single MKV for now)
	f, err := os.Open(workNZB)
	if err != nil {
		return err
	}
	doc, err := nzb.Parse(f)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("parse nzb: %w", err)
	}

	// Pick first file that looks like an MKV.
	fileIdx := -1
	for i := range doc.Files {
		subj := strings.ToLower(doc.Files[i].Subject)
		if strings.Contains(subj, ".mkv") {
			fileIdx = i
			break
		}
	}
	if fileIdx < 0 {
		return errors.New("health repair: no MKV file found in NZB")
	}
	file := doc.Files[fileIdx]

	// Extract original filename from subject.
	// Common forms include: "\"name.mkv\" yEnc". Fall back to a safe name.
	mkvName := "recovered.mkv"
	if m := regexp.MustCompile(`"([^"]+\.mkv)"`).FindStringSubmatch(file.Subject); len(m) == 2 {
		mkvName = m[1]
	} else if m := regexp.MustCompile(`([^\s]+\.mkv)`).FindStringSubmatch(file.Subject); len(m) == 2 {
		mkvName = filepath.Base(m[1])
	}

	// Link/copy PAR2 set into workdir (keep-local). This is mandatory for B2.
	parRoot := filepath.Join("/host", "inbox", "par2")
	stem := strings.TrimSuffix(baseName, filepath.Ext(baseName))

	norm := func(s string) string {
		s = strings.ToLower(s)
		b := make([]byte, 0, len(s))
		dash := false
		for i := 0; i < len(s); i++ {
			c := s[i]
			ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
			if ok {
				b = append(b, c)
				dash = false
				continue
			}
			if !dash {
				b = append(b, '-')
				dash = true
			}
		}
		out := strings.Trim(string(b), "-")
		return out
	}

	// Allow test suffixes like ".FORCE" to still match existing PAR2 filenames.
	stemMatch := stem
	low := strings.ToLower(stemMatch)
	if strings.HasSuffix(low, ".force") {
		stemMatch = stemMatch[:len(stemMatch)-len(".force")]
	}

	want := norm(stemMatch)
	parCount := 0
	_ = filepath.WalkDir(parRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		n := strings.ToLower(d.Name())
		if !strings.HasSuffix(n, ".par2") {
			return nil
		}
		base := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		if !strings.HasPrefix(norm(base), want) {
			return nil
		}
		dst := filepath.Join(workDir, d.Name())
		_ = os.Remove(dst)
		// Prefer hardlink/copy (not symlink): par2 auto-discovery of volume files is more reliable with regular entries.
		if err := os.Link(p, dst); err == nil {
			parCount++
			return nil
		}
		if b, err := os.ReadFile(p); err == nil {
			if err := os.WriteFile(dst, b, 0o644); err == nil {
				parCount++
			}
		}
		return nil
	})
	_ = r.jobs.AppendLog(ctx, jobID, fmt.Sprintf("health: linked par2 file(s)=%d", parCount))
	if parCount == 0 {
		return errors.New("health repair: no local PAR2 found for this NZB (B2 requires keep-local par2)")
	}

	// Download segments (or zero-fill missing) into a local file so par2 can repair it.
	// This is intentionally simple: sequential download, one NNTP client.
	pool := nntp.NewPool(nntp.Config{Host: cfg.Download.Host, Port: cfg.Download.Port, SSL: cfg.Download.SSL, User: cfg.Download.User, Pass: cfg.Download.Pass, Timeout: 30 * time.Second}, cfg.Download.Connections)
	cl, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("health: nntp acquire: %w", err)
	}
	defer pool.Release(cl)

	// Sort segments by number
	segs := make([]nzb.Segment, 0, len(file.Segments))
	segs = append(segs, file.Segments...)
	sort.Slice(segs, func(i, j int) bool { return segs[i].Number < segs[j].Number })

	outFile := filepath.Join(workDir, mkvName)
	_ = os.Remove(outFile)
	wf, err := os.OpenFile(outFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = wf.Close() }()

	missing := 0
	for i, s := range segs {
		id := strings.TrimSpace(s.ID)
		if i%200 == 0 {
			_ = r.jobs.AppendLog(ctx, jobID, fmt.Sprintf("health: downloading segments... %d/%d (missing=%d)", i, len(segs), missing))
		}
		lines, err := cl.BodyByMessageID(id)
		if err != nil {
			missing++
			// zero-fill
			_, _ = wf.Write(make([]byte, int(s.Bytes)))
			continue
		}
		data, _, _, _, err := yenc.DecodePart(lines)
		if err != nil {
			missing++
			_, _ = wf.Write(make([]byte, int(s.Bytes)))
			continue
		}
		_, _ = wf.Write(data)
	}
	_ = wf.Sync()
	_ = wf.Close()

	if missing == 0 {
		_ = r.jobs.AppendLog(ctx, jobID, "health: no missing segments detected; leaving NZB unchanged")
		return nil
	}
	_ = r.jobs.AppendLog(ctx, jobID, fmt.Sprintf("health: missing segments=%d (attempting PAR2 repair)", missing))

	// Find the main .par2 file (prefer one that is not a volume file)
	parMain := ""
	entries, _ := os.ReadDir(workDir)
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if strings.HasSuffix(n, ".par2") && !strings.Contains(n, ".vol") {
			parMain = filepath.Join(workDir, e.Name())
			break
		}
	}
	if parMain == "" {
		// fallback: any par2
		for _, e := range entries {
			if strings.HasSuffix(strings.ToLower(e.Name()), ".par2") {
				parMain = filepath.Join(workDir, e.Name())
				break
			}
		}
	}
	if parMain == "" {
		return errors.New("health: PAR2 files were linked but main .par2 not found")
	}

	// par2 may expect the original relative target path embedded in PAR2 (e.g. host/inbox/media/...).
	// Mirror that target in workdir pointing to our reconstructed outFile to avoid "Target ... missing".
	expectedRel := filepath.Join("host", "inbox", "media", filepath.Base(outFile))
	expectedAbs := filepath.Join(workDir, expectedRel)
	_ = os.MkdirAll(filepath.Dir(expectedAbs), 0o755)
	_ = os.Remove(expectedAbs)
	if err := os.Symlink(outFile, expectedAbs); err != nil {
		_ = copyFilePerm(outFile, expectedAbs, 0o644)
	}
	_ = r.jobs.AppendLog(ctx, jobID, fmt.Sprintf("health: par2 target mapped: %s -> %s", expectedRel, filepath.Base(outFile)))

	// par2 repair in-place
	_ = r.jobs.AppendLog(ctx, jobID, fmt.Sprintf("health: par2 repair: %s r %s", "/usr/bin/par2", filepath.Base(parMain)))
	// IMPORTANT: do not pass an alternate target filename here; let PAR2 use its own indexed target paths.
	cmd := exec.CommandContext(ctx, "par2", "r", parMain)
	cmd.Dir = workDir
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	scanPipe := func(prefix string, rc io.ReadCloser) {
		defer func() { _ = rc.Close() }()
		s := bufio.NewScanner(rc)
		for s.Scan() {
			_ = r.jobs.AppendLog(ctx, jobID, prefix+s.Text())
		}
	}
	if stdout != nil {
		go scanPipe("", stdout)
	}
	if stderr != nil {
		go scanPipe("ERR: ", stderr)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("health: par2 repair failed: %w", err)
	}

	// Now re-upload the repaired MKV and generate a CLEAN NZB (no PAR2 included).
	repairedNZBTmp := filepath.Join(workDir, stem+".repaired.nzb")
	_ = os.Remove(repairedNZBTmp)
	if err := r.healthUploadCleanNZB(ctx, jobID, cfg, outFile, repairedNZBTmp); err != nil {
		return err
	}

	// Replace original NZB (backup original first)
	bakRoot := strings.TrimSpace(cfg.Health.BackupDir)
	if bakRoot == "" {
		bakRoot = "/cache/health-bak"
	}
	outRoot := strings.TrimSpace(cfg.NgPost.OutputDir)
	if outRoot == "" {
		outRoot = "/host/inbox/nzb"
	}
	rel, _ := filepath.Rel(outRoot, nzbPath)
	if strings.HasPrefix(rel, "..") {
		rel = filepath.Base(nzbPath)
	}
	bakPath := filepath.Join(bakRoot, rel)
	if err := os.MkdirAll(filepath.Dir(bakPath), 0o755); err != nil {
		return err
	}

	destTmp := nzbPath + ".health.tmp"
	if err := copyFilePerm(repairedNZBTmp, destTmp, 0o644); err != nil {
		return fmt.Errorf("copy repaired nzb: %w", err)
	}

	_ = os.Remove(bakPath)
	if err := copyFilePerm(nzbPath, bakPath, 0o644); err != nil {
		_ = os.Remove(destTmp)
		return fmt.Errorf("backup original: %w", err)
	}
	if err := os.Remove(nzbPath); err != nil {
		_ = os.Remove(destTmp)
		return fmt.Errorf("remove original after backup: %w", err)
	}
	if rerr := os.Rename(destTmp, nzbPath); rerr != nil {
		_ = copyFilePerm(bakPath, nzbPath, 0o644)
		_ = os.Remove(destTmp)
		return fmt.Errorf("replace nzb: %w", rerr)
	}

	if err := r.healthRefreshImportDB(ctx, cfg, jobID, nzbPath); err != nil {
		_ = r.jobs.AppendLog(ctx, jobID, "health: db refresh WARN: "+err.Error())
	}
	if err := r.healthRegeneratePAR2(ctx, cfg, jobID, nzbPath, outFile); err != nil {
		_ = r.jobs.AppendLog(ctx, jobID, "health: par2 refresh WARN: "+err.Error())
	}

	if err := os.RemoveAll(workDir); err == nil {
		_ = r.jobs.AppendLog(ctx, jobID, "health: cleaned workdir")
	}
	_ = r.jobs.AppendLog(ctx, jobID, fmt.Sprintf("health: repaired OK (backup=%s)", bakPath))
	return nil
}

func (r *Runner) upsertHealthState(ctx context.Context, path, status string, lastCheckedAt, lastRepairedAt int64, lastError, repairJobID string) error {
	if r.jobs == nil || r.jobs.DB() == nil || r.jobs.DB().SQL == nil {
		return errors.New("jobs db not configured")
	}
	_, err := r.jobs.DB().SQL.ExecContext(ctx, `INSERT INTO health_nzb_state(path,status,last_checked_at,last_error,last_repair_job_id,last_repaired_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET
		status=excluded.status,
		last_checked_at=CASE WHEN excluded.last_checked_at>0 THEN excluded.last_checked_at ELSE health_nzb_state.last_checked_at END,
		last_error=excluded.last_error,
		last_repair_job_id=CASE WHEN excluded.last_repair_job_id<>'' THEN excluded.last_repair_job_id ELSE health_nzb_state.last_repair_job_id END,
		last_repaired_at=CASE WHEN excluded.last_repaired_at>0 THEN excluded.last_repaired_at ELSE health_nzb_state.last_repaired_at END`,
		path, status, lastCheckedAt, lastError, repairJobID, lastRepairedAt)
	return err
}

func (r *Runner) healthRefreshImportDB(ctx context.Context, cfg config.Config, jobID, nzbPath string) error {
	if r.jobs == nil || r.jobs.DB() == nil {
		return errors.New("jobs db not configured")
	}
	db := r.jobs.DB().SQL
	var importID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM nzb_imports WHERE path=? ORDER BY imported_at DESC LIMIT 1`, nzbPath).Scan(&importID); err == nil {
		tx, err := db.BeginTx(ctx, nil)
		if err == nil {
			stmts := []string{
				`DELETE FROM nzb_segments WHERE import_id=?`,
				`DELETE FROM nzb_files WHERE import_id=?`,
				`DELETE FROM library_overrides WHERE import_id=?`,
				`DELETE FROM library_review_dismissed WHERE import_id=?`,
				`DELETE FROM library_resolved WHERE import_id=?`,
				`DELETE FROM manual_items WHERE import_id=?`,
				`DELETE FROM nzb_imports WHERE id=?`,
			}
			for _, s := range stmts {
				if _, e := tx.ExecContext(ctx, s, importID); e != nil {
					_ = tx.Rollback()
					return e
				}
			}
			if e := tx.Commit(); e != nil {
				return e
			}
			_ = r.jobs.AppendLog(ctx, jobID, "health: db old import removed: "+importID)
		}
	}

	imp := importer.New(r.jobs)
	if _, _, err := imp.ImportNZB(ctx, jobID, nzbPath); err != nil {
		return err
	}
	if err := imp.EnrichLibraryResolved(ctx, cfg, jobID); err != nil {
		return err
	}
	_ = r.jobs.AppendLog(ctx, jobID, "health: db reimport+resolved refreshed")
	return nil
}

func (r *Runner) healthRegeneratePAR2(ctx context.Context, cfg config.Config, jobID, nzbPath, mediaPath string) error {
	if !cfg.Upload.Par.Enabled || cfg.Upload.Par.RedundancyPercent <= 0 {
		return errors.New("par2 disabled in config")
	}
	parRoot := strings.TrimSpace(cfg.Upload.Par.Dir)
	if parRoot == "" {
		parRoot = "/host/inbox/par2"
	}
	outRoot := strings.TrimSpace(cfg.NgPost.OutputDir)
	if outRoot == "" {
		outRoot = "/host/inbox/nzb"
	}

	norm := func(s string) string {
		s = strings.ToLower(s)
		b := make([]byte, 0, len(s))
		dash := false
		for i := 0; i < len(s); i++ {
			c := s[i]
			ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
			if ok {
				b = append(b, c)
				dash = false
				continue
			}
			if !dash {
				b = append(b, '-')
				dash = true
			}
		}
		return strings.Trim(string(b), "-")
	}

	stem := strings.TrimSuffix(filepath.Base(nzbPath), filepath.Ext(nzbPath))
	want := norm(stem)
	relDir, _ := filepath.Rel(outRoot, filepath.Dir(nzbPath))
	if strings.HasPrefix(relDir, "..") {
		relDir = ""
	}
	keepDir := filepath.Join(parRoot, relDir)
	_ = os.MkdirAll(keepDir, 0o755)

	stagingDir := filepath.Join("/cache", "health", jobID, "par-new")
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return err
	}
	parBase := filepath.Join(stagingDir, stem+".par2")
	args := []string{"c", fmt.Sprintf("-r%d", cfg.Upload.Par.RedundancyPercent), "-B/", parBase, mediaPath}
	_ = r.jobs.AppendLog(ctx, jobID, fmt.Sprintf("health: par2 regenerate: par2 %s", strings.Join(args, " ")))
	if err := runCommand(ctx, func(line string) {
		clean := strings.TrimSpace(line)
		if clean != "" {
			_ = r.jobs.AppendLog(ctx, jobID, clean)
		}
	}, "par2", args...); err != nil {
		return err
	}

	removed := 0
	entries, _ := os.ReadDir(keepDir)
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if e.IsDir() || !strings.HasSuffix(n, ".par2") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if !strings.HasPrefix(norm(base), want) {
			continue
		}
		if err := os.Remove(filepath.Join(keepDir, e.Name())); err == nil {
			removed++
		}
	}

	moved := 0
	newEntries, _ := os.ReadDir(stagingDir)
	for _, e := range newEntries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".par2") {
			continue
		}
		src := filepath.Join(stagingDir, e.Name())
		dst := filepath.Join(keepDir, e.Name())
		_ = os.Remove(dst)
		if err := os.Rename(src, dst); err == nil {
			moved++
			continue
		}
		if err := copyFilePerm(src, dst, 0o644); err == nil {
			_ = os.Remove(src)
			moved++
		}
	}
	_ = os.RemoveAll(stagingDir)
	_ = r.jobs.AppendLog(ctx, jobID, fmt.Sprintf("health: par2 refreshed (removed=%d new=%d dir=%s)", removed, moved, keepDir))
	return nil
}

func copyFilePerm(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return out.Close()
}

// healthUploadCleanNZB uploads a single media file and writes an NZB that contains ONLY the media.
// This intentionally does NOT upload PAR2.
func (r *Runner) healthUploadCleanNZB(ctx context.Context, jobID string, cfg config.Config, mediaPath string, outNZB string) error {
	// Force ngpost for health re-upload: nyuu-generated NZB segment bytes can differ from decoded part size
	// and break streaming range math (EOF). ngpost keeps compatible per-segment sizing for our pipeline.
	provider := "ngpost"
	ng := cfg.NgPost
	if !ng.Enabled {
		return errors.New("health: upload provider config missing (ngpost.enabled=false)")
	}
	if ng.Host == "" || ng.User == "" || ng.Pass == "" || ng.Groups == "" {
		return errors.New("health: upload config incomplete (need host/user/pass/groups)")
	}

	sanitize := func(s string) string {
		if ng.Pass == "" {
			return s
		}
		return strings.ReplaceAll(s, ng.Pass, "***")
	}

	if provider == "nyuu" {
		args := []string{"-h", ng.Host, "-P", fmt.Sprintf("%d", ng.Port)}
		if ng.SSL {
			args = append(args, "-S")
		}
		if ng.Connections > 0 {
			args = append(args, "-n", fmt.Sprintf("%d", ng.Connections))
		}
		args = append(args, "-g", ng.Groups)
		// Safe obfuscation: metadata only (same strategy as normal upload path).
		args = append(args,
			"--subject", "${rand(40)} yEnc ({part}/{parts})",
			"--nzb-subject", `"{filename}" yEnc ({part}/{parts})`,
			"--message-id", "${rand(24)}-${rand(12)}@nyuu",
			"--from", "poster <poster@example.com>",
		)
		args = append(args, "-o", outNZB, "-O")
		args = append(args, "-u", ng.User, "-p", ng.Pass)
		args = append(args, "-r", "keep")
		args = append(args, mediaPath)

		_ = r.jobs.AppendLog(ctx, jobID, "health: uploading repaired media (clean NZB, no PAR2)")
		_ = r.jobs.AppendLog(ctx, jobID, sanitize(fmt.Sprintf("health: nyuu: %s %s", r.NyuuPath, strings.Join(args[:min(10, len(args))], " "))))
		err := runCommand(ctx, func(line string) {
			_ = r.jobs.AppendLog(ctx, jobID, sanitize(line))
		}, r.NyuuPath, args...)
		if err == nil {
			return nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "illegal instruction") {
			_ = r.jobs.AppendLog(ctx, jobID, "health: nyuu illegal instruction; fallback to ngpost")
		} else {
			return err
		}
	}

	// ngpost
	args := []string{"-i", mediaPath, "-o", outNZB, "-h", ng.Host, "-P", fmt.Sprintf("%d", ng.Port)}
	if ng.SSL {
		args = append(args, "-s")
	}
	if ng.Connections > 0 {
		args = append(args, "-n", fmt.Sprintf("%d", ng.Connections))
	}
	if ng.Threads > 0 {
		args = append(args, "-t", fmt.Sprintf("%d", ng.Threads))
	}
	args = append(args, "-g", ng.Groups)
	if ng.Obfuscate {
		args = append(args, "-x")
	}
	if ng.TmpDir != "" {
		args = append(args, "--tmp_dir", ng.TmpDir)
	}
	args = append(args, "-u", ng.User, "-p", ng.Pass, "--disp_progress", "files")

	_ = r.jobs.AppendLog(ctx, jobID, "health: uploading repaired media (clean NZB, no PAR2)")
	_ = r.jobs.AppendLog(ctx, jobID, sanitize(fmt.Sprintf("health: ngpost: %s %s", r.NgPostPath, strings.Join(args[:min(10, len(args))], " "))))
	return runCommand(ctx, func(line string) {
		_ = r.jobs.AppendLog(ctx, jobID, sanitize(line))
	}, r.NgPostPath, args...)
}
