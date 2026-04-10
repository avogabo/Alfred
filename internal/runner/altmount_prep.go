package runner

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/config"
	"github.com/avogabo/AlfredEDR/internal/importer"
	"github.com/avogabo/AlfredEDR/internal/jobs"
	"github.com/avogabo/AlfredEDR/internal/library"
)

func prepareRelativePathForAltMount(ctx context.Context, cfg config.Config, store *jobs.Store, jobID, nzbPath string) (*string, error) {
	if store == nil || store.DB() == nil || store.DB().SQL == nil {
		return nil, nil
	}
	imp := importer.New(store)
	if _, _, err := imp.ImportNZB(ctx, jobID, nzbPath); err != nil {
		return nil, err
	}
	enrichCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if err := imp.EnrichLibraryResolved(enrichCtx, cfg, jobID); err != nil {
		return nil, err
	}
	var virtualDir string
	err := store.DB().SQL.QueryRowContext(ctx, `SELECT COALESCE(virtual_dir,'') FROM library_resolved WHERE import_id=? ORDER BY file_idx ASC LIMIT 1`, jobID).Scan(&virtualDir)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	virtualDir = strings.TrimSpace(virtualDir)
	if virtualDir == "" || virtualDir == "." {
		return nil, nil
	}
	virtualDir = strings.ReplaceAll(virtualDir, `\\`, `/`)
	virtualDir = strings.TrimPrefix(virtualDir, "/")
	virtualDir = strings.TrimSpace(virtualDir)
	if virtualDir == "" {
		return nil, nil
	}

	virtualDir = normalizeAltMountRelativePath(virtualDir)
	if virtualDir == "" {
		return nil, nil
	}
	return &virtualDir, nil
}

func normalizeAltMountRelativePath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, `\\`, `/`))
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return ""
	}

	parts := strings.Split(p, "/")
	if len(parts) >= 4 {
		low0 := strings.ToLower(strings.TrimSpace(parts[0]))
		low1 := strings.ToLower(strings.TrimSpace(parts[1]))
		low2 := strings.TrimSpace(parts[2])
		if strings.HasPrefix(low0, "edr_nzb") && (low1 == "1080p" || low1 == "2160p" || low1 == "4k") {
			quality := "1080"
			if low1 == "2160p" || low1 == "4k" {
				quality = "2160"
			}
			category := "PELICULAS"
			if strings.Contains(low0, "series") {
				category = "SERIES"
			}
			initial := strings.TrimSpace(low2)
			if initial == "" || initial == "0" || initial == "#" {
				initial = "#"
			} else {
				initial = library.InitialFolder(initial)
			}
			rest := parts[3:]
			return filepath.ToSlash(filepath.Join(append([]string{category, quality, initial}, rest...)...))
		}
	}

	return filepath.ToSlash(p)
}

func cleanupPreparedImport(ctx context.Context, store *jobs.Store, jobID string) error {
	if store == nil || store.DB() == nil || store.DB().SQL == nil {
		return nil
	}
	stmts := []string{
		`DELETE FROM nzb_segments WHERE import_id=?`,
		`DELETE FROM nzb_files WHERE import_id=?`,
		`DELETE FROM library_overrides WHERE import_id=?`,
		`DELETE FROM library_review_dismissed WHERE import_id=?`,
		`DELETE FROM library_resolved WHERE import_id=?`,
		`DELETE FROM manual_items WHERE import_id=?`,
		`DELETE FROM nzb_imports WHERE id=?`,
	}
	for _, q := range stmts {
		if _, err := store.DB().SQL.ExecContext(ctx, q, jobID); err != nil {
			return fmt.Errorf("cleanup prepared import: %w", err)
		}
	}
	return nil
}
