package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/config"
	"github.com/avogabo/AlfredEDR/internal/jobs"
	"github.com/avogabo/AlfredEDR/internal/library"
	"github.com/avogabo/AlfredEDR/internal/meta/tmdb"
	"github.com/avogabo/AlfredEDR/internal/nzb"
	"github.com/avogabo/AlfredEDR/internal/subject"
	"github.com/google/uuid"
)

type Importer struct {
	jobs *jobs.Store
}

func New(j *jobs.Store) *Importer { return &Importer{jobs: j} }

func (i *Importer) ImportNZB(ctx context.Context, jobID string, path string) (files int, totalBytes int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	doc, err := nzb.Parse(f)
	if err != nil {
		return 0, 0, err
	}

	files = len(doc.Files)
	for _, nf := range doc.Files {
		for _, s := range nf.Segments {
			totalBytes += s.Bytes
		}
	}

	importID := jobID
	if importID == "" {
		importID = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	groupsToJSON := func(groups []string) string {
		b, _ := json.Marshal(groups)
		return string(b)
	}

	// Persist import summary + per-file rows
	db := i.jobs.DB().SQL

	// Deduplicate by NZB path: if this path was already imported, skip creating a second import.
	var existingID string
	var existingFiles int
	var existingBytes int64
	if err := db.QueryRowContext(ctx, `SELECT id,files_count,total_bytes FROM nzb_imports WHERE path=? ORDER BY imported_at DESC LIMIT 1`, path).Scan(&existingID, &existingFiles, &existingBytes); err == nil {
		return existingFiles, existingBytes, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	now := time.Now().Unix()
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO nzb_imports(id,path,imported_at,files_count,total_bytes) VALUES(?,?,?,?,?)`,
		importID, path, now, files, totalBytes)
	if err != nil {
		return 0, 0, err
	}

	stmtFile, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO nzb_files(import_id,idx,subject,filename,poster,date,groups_json,segments_count,total_bytes) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, 0, err
	}
	defer stmtFile.Close()
	stmtSeg, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO nzb_segments(import_id,file_idx,number,bytes,message_id) VALUES(?,?,?,?,?)`)
	if err != nil {
		return 0, 0, err
	}
	defer stmtSeg.Close()

	for idx, nf := range doc.Files {
		var fb int64
		for _, s := range nf.Segments {
			fb += s.Bytes
		}
		fn, ok := subject.FilenameFromSubject(nf.Subject)
		if !ok || fn == "" {
			fn = fmt.Sprintf("file_%04d.bin", idx)
		}
		_, err := stmtFile.ExecContext(ctx,
			importID, idx, nf.Subject, fn, nf.Poster, nf.Date, groupsToJSON(nf.Groups), len(nf.Segments), fb)
		if err != nil {
			return 0, 0, err
		}

		// segments
		for _, seg := range nf.Segments {
			mid := strings.TrimSpace(seg.ID)
			if mid == "" {
				continue
			}
			_, err := stmtSeg.ExecContext(ctx,
				importID, idx, seg.Number, seg.Bytes, mid)
			if err != nil {
				return 0, 0, err
			}
		}
	}

	// Seed Manual tree from NZB path (idempotent):
	// /host/inbox/nzb/PELICULAS/1080/A/Avatar (2009).nzb ->
	// root/PELICULAS/1080/A/Avatar (2009) + manual_items for file_idx
	if err := seedManualFromNZB(ctx, tx, importID, path); err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return files, totalBytes, nil
}

func seedManualFromNZB(ctx context.Context, tx *sql.Tx, importID, nzbPath string) error {
	// already seeded somewhere in manual tree
	var exists int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM manual_items WHERE import_id=?`, importID).Scan(&exists)
	if exists > 0 {
		return nil
	}

	cleanPath := filepath.ToSlash(filepath.Clean(nzbPath))
	prefixes := []string{"/host/inbox/nzb/", "/host/NZB/"}
	rel := cleanPath
	for _, prefix := range prefixes {
		if strings.HasPrefix(cleanPath, prefix) {
			rel = strings.TrimPrefix(cleanPath, prefix)
			break
		}
	}
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "." {
		return nil
	}
	base := strings.TrimSuffix(rel, filepath.Ext(rel))
	parts := []string{}
	for _, p := range strings.Split(filepath.ToSlash(base), "/") {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return nil
	}

	ensureDir := func(parent, name string) (string, error) {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM manual_dirs WHERE parent_id=? AND name=? LIMIT 1`, parent, name).Scan(&id)
		if err == nil && strings.TrimSpace(id) != "" {
			return id, nil
		}
		id = uuid.NewString()
		if _, err := tx.ExecContext(ctx, `INSERT INTO manual_dirs(id,parent_id,name) VALUES(?,?,?)`, id, parent, name); err != nil {
			return "", err
		}
		return id, nil
	}

	leaf := "root"
	for _, name := range parts {
		nextID, err := ensureDir(leaf, name)
		if err != nil {
			return err
		}
		leaf = nextID
	}

	rows, err := tx.QueryContext(ctx, `SELECT idx, COALESCE(filename,'') FROM nzb_files WHERE import_id=? ORDER BY idx`, importID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var idx int
		var fn string
		if err := rows.Scan(&idx, &fn); err != nil {
			continue
		}
		if strings.TrimSpace(fn) == "" {
			fn = fmt.Sprintf("file_%04d.bin", idx)
		}
		itemID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `INSERT INTO manual_items(id,dir_id,label,import_id,file_idx) VALUES(?,?,?,?,?)`, itemID, leaf, fn, importID, idx); err != nil {
			continue
		}
	}
	return nil
}

// EnrichLibraryResolvedByPath resolves/stores library metadata for fast FUSE path building.
func (i *Importer) EnrichLibraryResolvedByPath(ctx context.Context, cfg config.Config, nzbPath string) error {
	db := i.jobs.DB().SQL
	var importID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM nzb_imports WHERE path=? ORDER BY imported_at DESC LIMIT 1`, nzbPath).Scan(&importID); err != nil {
		return err
	}
	return i.EnrichLibraryResolved(ctx, cfg, importID)
}

func (i *Importer) EnrichLibraryResolved(ctx context.Context, cfg config.Config, importID string) error {
	db := i.jobs.DB().SQL
	rows, err := db.QueryContext(ctx, `SELECT idx, COALESCE(filename,''), subject FROM nzb_files WHERE import_id=? ORDER BY idx`, importID)
	if err != nil {
		return err
	}
	defer rows.Close()
	res := library.NewResolver(cfg)
	l := cfg.Library.Defaults()
	now := time.Now().Unix()
	for rows.Next() {
		var idx int
		var fn, subj string
		if err := rows.Scan(&idx, &fn, &subj); err != nil {
			continue
		}
		name := strings.TrimSpace(fn)
		if name == "" {
			name = filepath.Base(subj)
		}
		fileCtx, cancel := context.WithTimeout(ctx, 12*time.Second)

		// Check for overrides first
		var ovKind, ovTitle, ovQuality string
		var ovYear, ovTMDB int
		hasOverride := false
		_ = db.QueryRowContext(fileCtx, `SELECT kind,title,year,quality,tmdb_id FROM library_overrides WHERE import_id=? AND file_idx=?`, importID, idx).Scan(&ovKind, &ovTitle, &ovYear, &ovQuality, &ovTMDB)
		if strings.TrimSpace(ovTitle) != "" {
			hasOverride = true
		}

		g := library.GuessFromFilename(name)
		
		if hasOverride {
			g.Title = ovTitle
			if ovYear > 0 { g.Year = ovYear }
			if ovQuality != "" { g.Quality = ovQuality }
			if ovKind == "series" { g.IsSeries = true } else { g.IsSeries = false }
		}

		fbTMDB := 0
		if !hasOverride {
			if fb, ok := library.ResolveWithFileBot(fileCtx, cfg, name); ok {
				if strings.TrimSpace(fb.Title) != "" {
					g.Title = fb.Title
				}
				if fb.Year > 0 {
					g.Year = fb.Year
				}
				if fb.TMDB > 0 {
					fbTMDB = fb.TMDB
				}
			}
		}

		kind := "movie"
		title := g.Title
		year := g.Year
		quality := g.Quality
		tmdbID := 0
		if hasOverride && ovTMDB > 0 {
			tmdbID = ovTMDB
		}

		seriesStatus := l.EmisionFolder
		season := g.Season
		episode := g.Episode
		episodeTitle := "Episode"

		if g.IsSeries {
			kind = "series"
			if !hasOverride {
				if fbTMDB > 0 {
					tmdbID = fbTMDB
				}
				if tv, ok := res.ResolveTV(fileCtx, title, year); ok {
					if strings.TrimSpace(tv.Name) != "" {
						title = tv.Name
					}
					if y := tv.FirstAirYear(); y > 0 {
						year = y
					}
					tmdbID = tv.ID
					b := tmdb.MapTVStatusToBucket(tv.Status)
					if b == tmdb.SeriesBucketFinalizada {
						seriesStatus = l.FinalizadasFolder
					} else {
						seriesStatus = l.EmisionFolder
					}
					if season > 0 && episode > 0 {
						if ep, ok := res.ResolveEpisodeTitle(fileCtx, tv.ID, season, episode); ok && strings.TrimSpace(ep) != "" {
							episodeTitle = ep
						}
					}
				}
			} else {
				// If overridden as series, just keep what we have and maybe fetch episode title if we have a TMDB ID
				if tmdbID > 0 && season > 0 && episode > 0 {
					if ep, ok := res.ResolveEpisodeTitle(fileCtx, tmdbID, season, episode); ok && strings.TrimSpace(ep) != "" {
						episodeTitle = ep
					}
				}
			}
		} else {
			if !hasOverride {
				if fbTMDB > 0 {
					tmdbID = fbTMDB
				}
				if mv, ok := res.ResolveMovie(fileCtx, title, year); ok {
					if strings.TrimSpace(mv.Title) != "" {
						title = mv.Title
					}
					if y := mv.ReleaseYear(); y > 0 {
						year = y
					}
					tmdbID = mv.ID
				}
			}
		}
		if strings.TrimSpace(title) == "" {
			title = g.Title
		}
		if strings.TrimSpace(quality) == "" {
			quality = g.Quality
		}
		if strings.TrimSpace(episodeTitle) == "" {
			episodeTitle = "Episode"
		}

		ext := g.Ext
		if ext == "" {
			ext = filepath.Ext(name)
		}
		initial := library.InitialFolder(title)
		if initial == "" {
			initial = library.InitialFolder(g.Title)
		}
		vars := map[string]string{
			"movies_root":        l.MoviesRoot,
			"series_root":        l.SeriesRoot,
			"emision_folder":     l.EmisionFolder,
			"finalizadas_folder": l.FinalizadasFolder,
			"quality":            quality,
			"initial":            initial,
			"ext":                ext,
			"title":              title,
			"tmdb_id":            fmt.Sprintf("%d", tmdbID),
			"series":             title,
			"series_status":      seriesStatus,
			"episode_title":      episodeTitle,
		}
		nums := map[string]int{"year": year, "season": season, "episode": episode}
		virtualDir := ""
		virtualName := ""
		virtualPath := ""
		if kind == "series" {
			baseDir := library.CleanPath(library.Render(l.SeriesDirTemplate, vars, nums))
			seasonDirName := library.CleanPath(library.Render(l.SeasonFolderTemplate, vars, nums))
			virtualDir = filepath.Join(baseDir, seasonDirName)
			virtualName = library.CleanPath(library.Render(l.SeriesFileTemplate, vars, nums))
		} else {
			virtualDir = library.CleanPath(library.Render(l.MovieDirTemplate, vars, nums))
			virtualName = library.CleanPath(library.Render(l.MovieFileTemplate, vars, nums))
		}
		virtualPath = filepath.Join(virtualDir, virtualName)
		if l.UppercaseFolders {
			virtualPath = library.ApplyUppercaseFolders(virtualPath)
			virtualDir = filepath.Dir(virtualPath)
			virtualName = filepath.Base(virtualPath)
		}

		if _, err := db.ExecContext(fileCtx, `
			INSERT INTO library_resolved(import_id,file_idx,kind,title,year,quality,tmdb_id,series_status,season,episode,episode_title,virtual_dir,virtual_name,virtual_path,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(import_id,file_idx) DO UPDATE SET
			  kind=excluded.kind,
			  title=excluded.title,
			  year=excluded.year,
			  quality=excluded.quality,
			  tmdb_id=excluded.tmdb_id,
			  series_status=excluded.series_status,
			  season=excluded.season,
			  episode=excluded.episode,
			  episode_title=excluded.episode_title,
			  virtual_dir=excluded.virtual_dir,
			  virtual_name=excluded.virtual_name,
			  virtual_path=excluded.virtual_path,
			  updated_at=excluded.updated_at
		`, importID, idx, kind, title, year, quality, tmdbID, seriesStatus, season, episode, episodeTitle, virtualDir, virtualName, virtualPath, now); err != nil {
			cancel()
			continue
		}
		cancel()
	}
	return nil
}
