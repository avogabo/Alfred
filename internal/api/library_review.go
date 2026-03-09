package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/importer"
	"github.com/avogabo/AlfredEDR/internal/library"
)

type reviewItem struct {
	ImportID string `json:"import_id"`
	FileIdx  int    `json:"file_idx"`
	Filename string `json:"filename"`
	Bytes    int64  `json:"bytes"`

	GuessTitle   string `json:"guess_title"`
	GuessYear    int    `json:"guess_year"`
	GuessQuality string `json:"guess_quality"`
}

func (s *Server) registerLibraryReviewRoutes() {
	// List files that "fail" auto-movie matching (tmdb resolve fails) and have no override.
	s.mux.HandleFunc("/api/v1/library/review", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.jobs == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "db not configured"})
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// limit is best-effort; default small to avoid hammering TMDB.
		limit := 120

		// Pull recent-ish files (by import time) to reduce work.
		q := `
			SELECT f.import_id, f.idx, COALESCE(f.filename,''), f.subject, f.total_bytes
			FROM nzb_files f
			JOIN nzb_imports i ON i.id=f.import_id
			ORDER BY i.imported_at DESC, f.idx ASC
			LIMIT ?
		`
		rows, err := s.jobs.DB().SQL.QueryContext(r.Context(), q, limit)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()

		cfg := s.Config()
		res := library.NewResolver(cfg)

		out := make([]reviewItem, 0)
		for rows.Next() {
			var importID string
			var idx int
			var fn string
			var subj string
			var bytes int64
			if err := rows.Scan(&importID, &idx, &fn, &subj, &bytes); err != nil {
				continue
			}
			filename := strings.TrimSpace(fn)
			if filename == "" {
				// fallback
				filename = strings.TrimSpace(filepath.Base(subj))
			}
			lowfn := strings.ToLower(filename)
			// Only review likely video files (avoid .txt test files, etc.).
			if !(strings.HasSuffix(lowfn, ".mkv") || strings.HasSuffix(lowfn, ".mp4") || strings.HasSuffix(lowfn, ".avi") || strings.HasSuffix(lowfn, ".m4v")) {
				continue
			}

			g := library.GuessFromFilename(filename)
			if g.IsSeries {
				// For now, review only movies (per user request). TV next.
				continue
			}

			// Skip if dismissed
			{
				var dummy int
				err := s.jobs.DB().SQL.QueryRowContext(r.Context(), `SELECT 1 FROM library_review_dismissed WHERE import_id=? AND file_idx=?`, importID, idx).Scan(&dummy)
				if err == nil {
					continue
				}
			}

			// Skip if an override exists
			var dummy string
			err := s.jobs.DB().SQL.QueryRowContext(r.Context(), `SELECT kind FROM library_overrides WHERE import_id=? AND file_idx=?`, importID, idx).Scan(&dummy)
			if err == nil {
				continue
			}
			if err != nil && err != sql.ErrNoRows {
				continue
			}

			// "Fail" means neither TMDB resolver nor FileBot-based resolver can resolve a movie id.
			_, ok := res.ResolveMovie(r.Context(), g.Title, g.Year)
			if !ok {
				if fb, fbOK := library.ResolveWithFileBot(r.Context(), cfg, filename); fbOK && fb.TMDB > 0 {
					ok = true
				}
			}
			if ok {
				continue
			}

			out = append(out, reviewItem{
				ImportID:     importID,
				FileIdx:      idx,
				Filename:     filename,
				Bytes:        bytes,
				GuessTitle:   g.Title,
				GuessYear:    g.Year,
				GuessQuality: g.Quality,
			})
			if len(out) >= 50 {
				break
			}
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"items": out, "ts": time.Now().Unix()})
	})

	// Dismiss a review item (hide from warning list)
	s.mux.HandleFunc("/api/v1/library/review/dismiss", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.jobs == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "db not configured"})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ImportID string `json:"import_id"`
			FileIdx  int    `json:"file_idx"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		req.ImportID = strings.TrimSpace(req.ImportID)
		if req.ImportID == "" || req.FileIdx < 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "import_id and file_idx required"})
			return
		}
		_, err := s.jobs.DB().SQL.ExecContext(r.Context(), `INSERT INTO library_review_dismissed(import_id,file_idx,dismissed_at) VALUES(?,?,?) ON CONFLICT(import_id,file_idx) DO UPDATE SET dismissed_at=excluded.dismissed_at`, req.ImportID, req.FileIdx, time.Now().Unix())
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	// Save an override for a movie file.
	s.mux.HandleFunc("/api/v1/library/override", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.jobs == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "db not configured"})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ImportID string `json:"import_id"`
			FileIdx  int    `json:"file_idx"`
			Kind     string `json:"kind"` // "movie"
			Title    string `json:"title"`
			Year     int    `json:"year"`
			Quality  string `json:"quality"`
			TMDBID   int    `json:"tmdb_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		req.ImportID = strings.TrimSpace(req.ImportID)
		req.Kind = strings.TrimSpace(req.Kind)
		req.Title = strings.TrimSpace(req.Title)
		req.Quality = strings.TrimSpace(req.Quality)
		if req.Kind == "" {
			req.Kind = "movie"
		}
		if req.ImportID == "" || req.FileIdx < 0 || req.Title == "" || req.Quality == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "import_id, file_idx, title, quality required"})
			return
		}
		if req.Year < 0 {
			req.Year = 0
		}
		// default tmdb_id to 0 if unknown
		if req.TMDBID < 0 {
			req.TMDBID = 0
		}

		_, err := s.jobs.DB().SQL.ExecContext(r.Context(), `
			INSERT INTO library_overrides(import_id,file_idx,kind,title,year,quality,tmdb_id,updated_at)
			VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(import_id,file_idx) DO UPDATE SET
				kind=excluded.kind,
				title=excluded.title,
				year=excluded.year,
				quality=excluded.quality,
				tmdb_id=excluded.tmdb_id,
				updated_at=excluded.updated_at
		`, req.ImportID, req.FileIdx, req.Kind, req.Title, req.Year, req.Quality, req.TMDBID, time.Now().Unix())
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		// also remove any dismissed flag for this file
		_, _ = s.jobs.DB().SQL.ExecContext(r.Context(), `DELETE FROM library_review_dismissed WHERE import_id=? AND file_idx=?`, req.ImportID, req.FileIdx)

		// force immediate re-resolve so UI changes instantly
		_ = importer.New(s.jobs).EnrichLibraryResolved(r.Context(), s.Config(), req.ImportID)

		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "import_id": req.ImportID, "file_idx": req.FileIdx})
	})

	// Batch override: apply same movie fix to all movie-like video files in this import.
	s.mux.HandleFunc("/api/v1/library/override/import", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.jobs == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "db not configured"})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ImportID string `json:"import_id"`
			Title    string `json:"title"`
			Year     int    `json:"year"`
			Quality  string `json:"quality"`
			TMDBID   int    `json:"tmdb_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		req.ImportID = strings.TrimSpace(req.ImportID)
		req.Title = strings.TrimSpace(req.Title)
		req.Quality = strings.TrimSpace(req.Quality)
		if req.ImportID == "" || req.Title == "" || req.Quality == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "import_id, title, quality required"})
			return
		}
		if req.Year < 0 {
			req.Year = 0
		}
		if req.TMDBID < 0 {
			req.TMDBID = 0
		}

		rows, err := s.jobs.DB().SQL.QueryContext(r.Context(), `SELECT idx, COALESCE(filename,''), subject FROM nzb_files WHERE import_id=? ORDER BY idx`, req.ImportID)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var idx int
			var fn string
			var subj string
			if err := rows.Scan(&idx, &fn, &subj); err != nil {
				continue
			}
			name := strings.TrimSpace(fn)
			if name == "" {
				name = strings.TrimSpace(filepath.Base(subj))
			}
			low := strings.ToLower(name)
			if !(strings.HasSuffix(low, ".mkv") || strings.HasSuffix(low, ".mp4") || strings.HasSuffix(low, ".avi") || strings.HasSuffix(low, ".m4v")) {
				continue
			}
			g := library.GuessFromFilename(name)
			if g.IsSeries {
				continue
			}

			_, err := s.jobs.DB().SQL.ExecContext(r.Context(), `
				INSERT INTO library_overrides(import_id,file_idx,kind,title,year,quality,tmdb_id,updated_at)
				VALUES(?,?,?,?,?,?,?,?)
				ON CONFLICT(import_id,file_idx) DO UPDATE SET
					kind=excluded.kind,
					title=excluded.title,
					year=excluded.year,
					quality=excluded.quality,
					tmdb_id=excluded.tmdb_id,
					updated_at=excluded.updated_at
			`, req.ImportID, idx, "movie", req.Title, req.Year, req.Quality, req.TMDBID, time.Now().Unix())
			if err == nil {
				count++
				_, _ = s.jobs.DB().SQL.ExecContext(r.Context(), `DELETE FROM library_review_dismissed WHERE import_id=? AND file_idx=?`, req.ImportID, idx)
			}
		}

		if count > 0 {
			// force immediate re-resolve so UI changes instantly
			_ = importer.New(s.jobs).EnrichLibraryResolved(r.Context(), s.Config(), req.ImportID)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "import_id": req.ImportID, "updated": count})
	})
}
