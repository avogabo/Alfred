package runner

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/config"
	"github.com/avogabo/AlfredEDR/internal/importer"
	"github.com/avogabo/AlfredEDR/internal/jobs"
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
	return &virtualDir, nil
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
