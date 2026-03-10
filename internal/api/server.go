package api

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"strconv"
	"strings"
	"sync"

	"github.com/avogabo/AlfredEDR/internal/config"
	"github.com/avogabo/AlfredEDR/internal/db"
	"github.com/avogabo/AlfredEDR/internal/jobs"
	"github.com/avogabo/AlfredEDR/internal/streamer"
	"github.com/avogabo/AlfredEDR/internal/version"
)

//go:embed webui/*
var uiFS embed.FS

type Server struct {
	cfgMu   sync.RWMutex
	cfg     config.Config
	cfgPath string
	mux     *http.ServeMux
	jobs    *jobs.Store

	webdavIdxMu      sync.RWMutex
	webdavNodes      map[string]webdavNode
	webdavDirs       map[string][]webdavNode
	webdavIdxBuiltAt time.Time
	webdavIdxTTL     time.Duration

	streamMu sync.Mutex
	stream   *streamer.Streamer
}

func (s *Server) Config() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func (s *Server) setConfig(next config.Config) {
	s.cfgMu.Lock()
	s.cfg = next
	s.cfgMu.Unlock()
}

type Options struct {
	ConfigPath string
	DBPath     string
}

func New(cfg config.Config, opts Options) (*Server, func() error, error) {
	s := &Server{cfg: cfg, cfgPath: opts.ConfigPath, mux: http.NewServeMux(), webdavIdxTTL: 20 * time.Second}

	closers := []func() error{}
	if opts.DBPath != "" {
		d, err := db.Open(opts.DBPath)
		if err != nil {
			return nil, nil, err
		}
		closers = append(closers, d.Close)
		s.jobs = jobs.NewStore(d)
	}

	closeFn := func() error {
		var first error
		for i := len(closers) - 1; i >= 0; i-- {
			if err := closers[i](); err != nil && first == nil {
				first = err
			}
		}
		return first
	}

	// Health
	s.mux.HandleFunc("/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"time":    time.Now().Format(time.RFC3339),
			"version": version.Version,
			"commit":  version.Commit,
		})
	})

	// Native WebDAV endpoint (read-only) backed by Alfred virtual Library.
	s.registerLibraryWebDAVRoutes()

	// Basic API (UI consumes this)
	s.mux.HandleFunc("/api/v1/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(s.Config())
		case http.MethodPut:
			b, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			var next config.Config
			if err := json.Unmarshal(b, &next); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			if err := next.Validate(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			
			// Hard-override to nyuu to prevent ngpost misconfiguration
			next.Upload.Provider = "nyuu"
			
			// Persist to disk and apply in-memory
			if err := config.Save(s.cfgPath, next); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			s.setConfig(next)
			_ = json.NewEncoder(w).Encode(s.Config())
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	// Restart (dev UX): exit 0 after responding so Docker can restart the container.
	s.mux.HandleFunc("/api/v1/restart", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": "restarting"})
		// Give the response a moment to flush.
		go func() {
			time.Sleep(300 * time.Millisecond)
			os.Exit(0)
		}()
	})

	// DB admin
	s.mux.HandleFunc("/api/v1/db/reset", s.handleDBReset)

	// Jobs API
	s.mux.HandleFunc("/api/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.jobs == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "jobs db not configured"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			limit := 40
			if q := strings.TrimSpace(r.URL.Query().Get("limit")); q != "" {
				if n, err := strconv.Atoi(q); err == nil {
					if n > 0 && n <= 500 {
						limit = n
					}
				}
			}
			state := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("state")))
			var (
				items []jobs.Job
				err   error
			)
			switch state {
			case "queued", "running", "done", "failed":
				items, err = s.jobs.ListByState(r.Context(), jobs.State(state), limit)
			default:
				items, err = s.jobs.List(r.Context(), limit)
			}
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(items)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	// Watcher admin: clear media ingest seen-state to force a temporary requeue cycle.
	// This does NOT delete media files; it only resets dedupe markers in ingest_seen.
	s.mux.HandleFunc("/api/v1/watch/media/requeue", func(w http.ResponseWriter, r *http.Request) {
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
		d := s.jobs.DB().SQL
		res, err := d.ExecContext(r.Context(), `DELETE FROM ingest_seen WHERE kind IN ('media_pending','media','media_pack_pending','media_pack')`)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		n, _ := res.RowsAffected()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "deleted": n, "note": "media files were not deleted"})
	})

	s.mux.HandleFunc("/api/v1/jobs/enqueue/import", func(w http.ResponseWriter, r *http.Request) {
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
		var payload struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		job, err := s.jobs.Enqueue(r.Context(), jobs.TypeImport, payload)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(job)
	})

	s.mux.HandleFunc("/api/v1/jobs/enqueue/upload", func(w http.ResponseWriter, r *http.Request) {
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
		var payload struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		job, err := s.jobs.Enqueue(r.Context(), jobs.TypeUpload, payload)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(job)
	})

	// Extra routes
	s.registerJobLogRoutes()
	s.registerProviderRoutes()
	s.registerCatalogRoutes()
	s.registerImportDeleteRoutes()
	s.registerCatalogFileRoutes()
	s.registerRawRoutes()
	s.registerManualLibraryRoutes()
	s.registerManualImportRoutes()
	s.registerManualMediaUploadRoutes()
	s.registerHostFSRoutes()
	s.registerLibraryReviewRoutes()
	s.registerLibraryAutoListRoutes()
	s.registerLibraryTemplatesRoutes()
	s.registerUploadSummaryRoutes()
	s.registerFileBotRoutes()

	// Backups
	s.registerBackupRoutes(opts.DBPath)

	// UI static
	// IMPORTANT: mobile browsers are aggressive with caching. Serve UI with no-store so
	// changes (providers/imports/UI JS) show up without a "hard refresh".
	ui := http.FS(uiFS)
	fs := http.FileServer(ui)
	s.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" || p == "/webui" || p == "/webui/" {
			http.Redirect(w, r, "/webui/index2.html", http.StatusFound)
			return
		}
		// Apply to the UI surface; avoid messing with API responses.
		if p == "/index.html" || strings.HasPrefix(p, "/webui/") {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
		}
		fs.ServeHTTP(w, r)
	}))

	if s.jobs != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			_, _, _ = s.getWebDAVIndex(ctx)
		}()
	}

	return s, closeFn, nil
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) Jobs() *jobs.Store { return s.jobs }

func (s *Server) getStreamer() *streamer.Streamer {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.stream == nil {
		cfg := s.Config()
		s.stream = streamer.New(cfg.Download, s.jobs, cfg.Paths.CacheDir, cfg.Paths.CacheMaxBytes)
	}
	return s.stream
}
