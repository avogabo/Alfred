package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/jobs"
)

type uploadSummary struct {
	ID        string      `json:"id"`
	State     jobs.State  `json:"state"`
	UpdatedAt string      `json:"updated_at"`
	Path      string      `json:"path"`
	Phase     string      `json:"phase"`
	Progress  int         `json:"progress"`
	LastLine  string      `json:"last_line"`
	Error     *string     `json:"error,omitempty"`
}

func (s *Server) registerUploadSummaryRoutes() {
	s.mux.HandleFunc("/api/v1/uploads/summary", func(w http.ResponseWriter, r *http.Request) {
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

		all, err := s.jobs.List(r.Context(), 200)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		out := make([]uploadSummary, 0)
		for _, j := range all {
			if j.Type != jobs.TypeUpload && j.Type != jobs.TypeUploadParNZB {
				continue
			}
			// payload contains {"path":"..."} or PAR2 fields
			var p struct {
				Path      string `json:"path"`
				InputPath string `json:"input_path"`
			}
			_ = json.Unmarshal(j.Payload, &p)
			jobPath := p.Path
			if strings.TrimSpace(jobPath) == "" {
				jobPath = p.InputPath
			}

			lines, _ := s.jobs.GetLogs(r.Context(), j.ID, 20)
			phase := ""
			progress := 0
			lastLine := ""
			if len(lines) > 0 {
				lastLine = lines[0]
				for _, ln := range lines {
					l := strings.TrimSpace(ln)
					if strings.HasPrefix(l, "PHASE:") {
						phase = strings.TrimSpace(strings.TrimPrefix(l, "PHASE:"))
						break
					}
				}
				for _, ln := range lines {
					l := strings.TrimSpace(ln)
					if strings.HasPrefix(l, "PROGRESS:") {
						v := strings.TrimSpace(strings.TrimPrefix(l, "PROGRESS:"))
						// best-effort parse int
						for i := 0; i < len(v); i++ {
							if v[i] < '0' || v[i] > '9' { v = v[:i]; break }
						}
						if v != "" {
							var n int
							_, _ = fmt.Sscanf(v, "%d", &n)
							if n >= 0 && n <= 100 { progress = n }
						}
						break
					}
				}
			}

			out = append(out, uploadSummary{
				ID:        j.ID,
				State:     j.State,
				UpdatedAt: j.UpdatedAt.Format(time.RFC3339),
				Path:      jobPath,
				Phase:     phase,
				Progress:  progress,
				LastLine:  lastLine,
				Error:     j.Error,
			})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
	})
}
