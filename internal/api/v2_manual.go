package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/jobs"
	"github.com/avogabo/AlfredEDR/internal/library"
)

type manualPreviewRequest struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	SeriesMode string `json:"series_mode"`
}

func (s *Server) registerV2ManualRoutes() {
	s.mux.HandleFunc("/api/v2/manual/preview", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req manualPreviewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		cfg := s.Config()
		p := strings.TrimSpace(req.Path)
		if p == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "path required"})
			return
		}
		if !strings.HasPrefix(p, "/host/") {
			p = filepath.Clean(filepath.Join(cfg.Paths.HostRoot, strings.TrimPrefix(p, "/")))
		}
		st, err := os.Stat(p)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		baseNameOnly := filepath.Base(p)
		g := library.GuessFromFilename(baseNameOnly)
		kind := strings.ToLower(strings.TrimSpace(req.Kind))
		seriesMode := strings.ToLower(strings.TrimSpace(req.SeriesMode))
		if kind == "" {
			if g.IsSeries || st.IsDir() {
				kind = "series"
			} else {
				kind = "movie"
			}
		}
		if seriesMode == "" {
			if st.IsDir() {
				seriesMode = "season"
			} else {
				seriesMode = "episode"
			}
		}

		mode := "file"
		if st.IsDir() {
			mode = "folder"
		}
		baseName := strings.TrimSuffix(baseNameOnly, filepath.Ext(baseNameOnly))
		resolvedTitle := strings.TrimSpace(g.Title)
		resolvedYear := g.Year
		if st.IsDir() && kind == "series" && seriesMode == "season" {
			parentName := filepath.Base(filepath.Dir(p))
			parentGuess := library.GuessFromFilename(parentName)
			if strings.TrimSpace(parentGuess.Title) != "" {
				resolvedTitle = strings.TrimSpace(parentGuess.Title)
			}
			if parentGuess.Year > 0 {
				resolvedYear = parentGuess.Year
			}
			if g.Season <= 0 {
				seasonGuess := library.GuessFromFilename(baseNameOnly)
				if seasonGuess.Season > 0 {
					g.Season = seasonGuess.Season
				}
			}
		}
		if !st.IsDir() {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			if fb, ok := library.ResolveWithFileBot(ctx, cfg, filepath.Base(p)); ok {
				if strings.TrimSpace(fb.Title) != "" {
					resolvedTitle = fb.Title
				}
				if fb.Year > 0 {
					resolvedYear = fb.Year
				}
			}
		}
		if strings.TrimSpace(resolvedTitle) == "" {
			resolvedTitle = baseName
		}
		yearPart := ""
		if resolvedYear > 0 && !strings.Contains(strings.ToLower(resolvedTitle), fmt.Sprintf("(%d)", resolvedYear)) {
			yearPart = fmt.Sprintf(" (%d)", resolvedYear)
		}
		cleanTitle := strings.TrimSpace(resolvedTitle + yearPart)
		namePreview := cleanTitle
		if kind == "series" {
			if seriesMode == "season" {
				if g.Season > 0 {
					namePreview = fmt.Sprintf("%s Temporada %02d", cleanTitle, g.Season)
				} else {
					namePreview = fmt.Sprintf("%s Temporada", cleanTitle)
				}
			} else if g.Season > 0 && g.Episode > 0 {
				namePreview = fmt.Sprintf("%s %02dx%02d", cleanTitle, g.Season, g.Episode)
			} else {
				namePreview = fmt.Sprintf("%s Capítulo", cleanTitle)
			}
		}
		nzbOutDir := cfg.NgPost.OutputDir
		if strings.TrimSpace(nzbOutDir) == "" {
			nzbOutDir = "/host/inbox/nzb"
		}
		nzbFile := namePreview + ".nzb"
		nzbOut := filepath.Join(nzbOutDir, nzbFile)
		if kind == "series" {
			initial := library.InitialFolder(resolvedTitle)
			if strings.TrimSpace(initial) == "" {
				initial = "#"
			}
			seriesFolder := cleanTitle
			nzbOut = filepath.Join(nzbOutDir, "SERIES", initial, seriesFolder, nzbFile)
		} else {
			quality := g.Quality
			if strings.TrimSpace(quality) == "" {
				quality = "1080"
			}
			initial := library.InitialFolder(resolvedTitle)
			if strings.TrimSpace(initial) == "" {
				initial = "#"
			}
			nzbOut = filepath.Join(nzbOutDir, "Peliculas", quality, initial, nzbFile)
		}
		parDir := cfg.Upload.Par.Dir
		if strings.TrimSpace(parDir) == "" {
			parDir = "/host/inbox/par2"
		}

		fileCount := 1
		if st.IsDir() {
			count := 0
			_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
				if err != nil || d == nil || d.IsDir() {
					return nil
				}
				count++
				return nil
			})
			if count > 0 {
				fileCount = count
			}
		}

		resp := map[string]any{
			"path":                p,
			"exists":              true,
			"mode":                mode,
			"kind":                kind,
			"series_mode":         seriesMode,
			"name_preview":        namePreview,
			"resolved_title":      resolvedTitle,
			"resolved_year":       resolvedYear,
			"season":              g.Season,
			"episode":             g.Episode,
			"nzb_output":          nzbOut,
			"combined_nzb_output": nzbOut,
			"par_keep_dir":        parDir,
			"is_dir":              st.IsDir(),
			"size":                st.Size(),
			"file_count":          fileCount,
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	s.mux.HandleFunc("/api/v2/manual/upload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.jobs == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "jobs db not configured"})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req manualPreviewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		cfg := s.Config()
		p := strings.TrimSpace(req.Path)
		if p == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "path required"})
			return
		}
		if !strings.HasPrefix(p, "/host/") {
			p = filepath.Clean(filepath.Join(cfg.Paths.HostRoot, strings.TrimPrefix(p, "/")))
		}
		job, err := s.jobs.Enqueue(r.Context(), jobs.TypeUpload, map[string]string{"path": p})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "job": job})
	})

}
