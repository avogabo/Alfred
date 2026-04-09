package watch

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/config"
	"github.com/avogabo/AlfredEDR/internal/jobs"
)

type Watcher struct {
	jobs *jobs.Store

	NZB   config.WatchKind
	Media config.WatchKind

	Interval time.Duration
}

func New(j *jobs.Store, nzb, media config.WatchKind) *Watcher {
	return &Watcher{jobs: j, NZB: nzb, Media: media, Interval: 5 * time.Second}
}

func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.Interval)
	defer t.Stop()

	// Initial scan
	_ = w.scanOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = w.scanOnce(ctx)
		}
	}
}

func (w *Watcher) scanOnce(ctx context.Context) error {
	if w.jobs == nil {
		return nil
	}
	if w.NZB.Enabled {
		if err := w.scanNZB(ctx); err != nil {
			_ = w.jobs.AppendLog(ctx, "watch", fmt.Sprintf("watch scanNZB error: %v", err))
		}
	}
	if w.Media.Enabled {
		if err := w.scanMedia(ctx); err != nil {
			_ = w.jobs.AppendLog(ctx, "watch", fmt.Sprintf("watch scanMedia error: %v", err))
		}
	}
	return nil
}

func (w *Watcher) scanNZB(ctx context.Context) error {
	root := w.NZB.Dir
	if root == "" {
		return nil
	}

	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if !w.NZB.Recursive {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		lowName := strings.ToLower(name)
		if !strings.HasSuffix(lowName, ".nzb") {
			return nil
		}
		if strings.Contains(strings.ToLower(filepath.ToSlash(path)), "/.par2/") || strings.Contains(lowName, "[par2]") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if ok, _ := w.markSeen(ctx, path, "nzb", info); ok {
			_, _ = w.jobs.Enqueue(ctx, jobs.TypeImport, map[string]string{"path": path})
		}
		return nil
	}

	return filepath.WalkDir(root, walkFn)
}

func (w *Watcher) scanMedia(ctx context.Context) error {
	root := w.Media.Dir
	if root == "" {
		return nil
	}
	// Avoid processing incomplete files while they are being copied into the inbox.
	// Require the file to be unchanged for this duration before enqueueing.
	stableFor := 60 * time.Second
	seasonStableFor := 3 * time.Minute

	isVideo := func(name string) bool {
		low := strings.ToLower(name)
		return strings.HasSuffix(low, ".mkv") || strings.HasSuffix(low, ".mp4") || strings.HasSuffix(low, ".avi") || strings.HasSuffix(low, ".m4v")
	}
	isSeasonDir := func(name string) bool {
		low := strings.ToLower(strings.TrimSpace(name))
		return strings.HasPrefix(low, "temporada") || strings.HasPrefix(low, "season")
	}
	episodeLikeRE := regexp.MustCompile(`(?i)(\b\d{1,2}x\d{1,3}\b|\bs\d{1,2}e\d{1,3}\b|\bt\d{1,2}e\d{1,3}\b|\bcap(?:itulo)?\s*\d{1,3}\b|\bep\s*\d{1,3}\b)`)
	isEpisodeLike := func(name string) bool {
		base := strings.TrimSuffix(name, filepath.Ext(name))
		return episodeLikeRE.MatchString(base)
	}

	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if !w.Media.Recursive {
				return fs.SkipDir
			}

			// If this looks like a season folder, enqueue it as ONE upload job (pack) only
			// after the *contents* are stable (not just the folder mtime).
			if isSeasonDir(d.Name()) {
				vidCount := 0
				var totalBytes int64 = 0
				var maxMtime int64 = 0
				hasTemp := false
				_ = filepath.WalkDir(path, func(p string, dd fs.DirEntry, e error) error {
					if e != nil || dd == nil {
						return nil
					}
					if dd.IsDir() {
						return nil
					}
					low := strings.ToLower(dd.Name())
					if strings.HasSuffix(low, ".part") || strings.HasSuffix(low, ".partial") || strings.HasSuffix(low, ".tmp") || strings.HasSuffix(low, ".crdownload") {
						hasTemp = true
					}
					if !isVideo(dd.Name()) {
						return nil
					}
					info, ie := dd.Info()
					if ie != nil {
						return nil
					}
					vidCount++
					totalBytes += info.Size()
					mt := info.ModTime().Unix()
					if mt > maxMtime {
						maxMtime = mt
					}
					return nil
				})

				if vidCount >= 2 && !hasTemp {
					// encode content fingerprint in size, and use latest file mtime as state mtime.
					stateSize := totalBytes + int64(vidCount)
					if ok, _ := w.markStableState(ctx, path, "media_pack_pending", "media_pack", stateSize, maxMtime, seasonStableFor); ok {
						_, _ = w.jobs.Enqueue(ctx, jobs.TypeUpload, map[string]string{"path": path})
					}
					return fs.SkipDir
				}
			}

			return nil
		}

		if !isVideo(d.Name()) {
			return nil
		}
		// If this file is inside a season folder, let the season-pack logic handle enqueueing.
		if isSeasonDir(filepath.Base(filepath.Dir(path))) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		pendingKind := "media_movie_pending"
		readyKind := "media_movie"
		if isEpisodeLike(d.Name()) {
			pendingKind = "media_episode_pending"
			readyKind = "media_episode"
		}
		if ok, _ := w.markStable(ctx, path, pendingKind, readyKind, info, stableFor); ok {
			_, _ = w.jobs.Enqueue(ctx, jobs.TypeUpload, map[string]string{"path": path})
		}
		return nil
	}
	return filepath.WalkDir(root, walkFn)
}

// markSeen returns ok=true if this path is new or changed and should be processed.
func (w *Watcher) markSeen(ctx context.Context, path, kind string, info fs.FileInfo) (bool, error) {
	d := w.jobs.DB().SQL
	size := info.Size()
	mtime := info.ModTime().Unix()

	var oldSize int64
	var oldMtime int64
	err := d.QueryRowContext(ctx, `SELECT size,mtime FROM ingest_seen WHERE path=?`, path).Scan(&oldSize, &oldMtime)
	if err != nil {
		if err == sql.ErrNoRows {
			_, err2 := d.ExecContext(ctx, `INSERT INTO ingest_seen(path,kind,size,mtime,seen_at) VALUES(?,?,?,?,?)`, path, kind, size, mtime, time.Now().Unix())
			return err2 == nil, err2
		}
		return false, err
	}

	if oldSize == size && oldMtime == mtime {
		return false, nil
	}
	_, err = d.ExecContext(ctx, `UPDATE ingest_seen SET kind=?, size=?, mtime=?, seen_at=? WHERE path=?`, kind, size, mtime, time.Now().Unix(), path)
	return err == nil, err
}

// markStable returns ok=true once the item has been unchanged for at least stableFor.
// We store seen_at as "last_changed_at" for pending kinds.
func (w *Watcher) markStable(ctx context.Context, path, pendingKind, readyKind string, info fs.FileInfo, stableFor time.Duration) (bool, error) {
	return w.markStableState(ctx, path, pendingKind, readyKind, info.Size(), info.ModTime().Unix(), stableFor)
}

// markStableState is like markStable, but uses explicit state fields (size/mtime-like fingerprint).
func (w *Watcher) markStableState(ctx context.Context, path, pendingKind, readyKind string, size, mtime int64, stableFor time.Duration) (bool, error) {
	d := w.jobs.DB().SQL
	now := time.Now().Unix()
	stableSecs := int64(stableFor.Seconds())
	if stableSecs < 1 {
		stableSecs = 1
	}

	var oldKind string
	var oldSize int64
	var oldMtime int64
	var lastChangedAt int64
	err := d.QueryRowContext(ctx, `SELECT kind,size,mtime,seen_at FROM ingest_seen WHERE path=?`, path).Scan(&oldKind, &oldSize, &oldMtime, &lastChangedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			_, err2 := d.ExecContext(ctx, `INSERT INTO ingest_seen(path,kind,size,mtime,seen_at) VALUES(?,?,?,?,?)`, path, pendingKind, size, mtime, now)
			return false, err2
		}
		return false, err
	}

	if oldKind == readyKind {
		return false, nil
	}

	if oldSize != size || oldMtime != mtime {
		_, err = d.ExecContext(ctx, `UPDATE ingest_seen SET kind=?, size=?, mtime=?, seen_at=? WHERE path=?`, pendingKind, size, mtime, now, path)
		return false, err
	}

	if oldKind == pendingKind {
		if now-lastChangedAt >= stableSecs {
			_, err = d.ExecContext(ctx, `UPDATE ingest_seen SET kind=?, size=?, mtime=?, seen_at=? WHERE path=?`, readyKind, size, mtime, now, path)
			return err == nil, err
		}
		return false, nil
	}

	_, err = d.ExecContext(ctx, `UPDATE ingest_seen SET kind=?, size=?, mtime=?, seen_at=? WHERE path=?`, pendingKind, size, mtime, now, path)
	return false, err
}
