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
		cutoff := time.Now().Add(-24 * time.Hour)
		for _, j := range all {
			if j.Type != jobs.TypeUpload {
				continue
			}
			if j.UpdatedAt.Before(cutoff) {
				continue
			}
			// payload contains {"path":"..."}
			var p struct {
				Path      string `json:"path"`
				InputPath string `json:"input_path"`
			}
			_ = json.Unmarshal(j.Payload, &p)
			jobPath := p.Path
			if strings.TrimSpace(jobPath) == "" {
				jobPath = p.InputPath
			}
			jobPath = strings.TrimSpace(jobPath)
			if strings.HasPrefix(jobPath, "/host/inbox/media/") {
				rel := strings.TrimPrefix(jobPath, "/host/inbox/media/")
				parts := strings.Split(rel, "/")
				if len(parts) >= 2 {
					jobPath = parts[0] + " / " + parts[1]
				} else if len(parts) == 1 {
					jobPath = parts[0]
				}
			}

			lines, _ := s.jobs.GetLogs(r.Context(), j.ID, 100)
			phase := ""
			progress := 0
			lastLine := ""
			if len(lines) > 0 {
				lastLine = lines[0]
				for _, raw := range lines {
					l := strings.TrimSpace(raw)
					if phase == "" && strings.HasPrefix(l, "PHASE:") {
						phase = strings.TrimSpace(strings.TrimPrefix(l, "PHASE:"))
					}
					if progress == 0 && strings.HasPrefix(l, "PROGRESS:") {
						v := strings.TrimSpace(strings.TrimPrefix(l, "PROGRESS:"))
						for j := 0; j < len(v); j++ {
							if v[j] < '0' || v[j] > '9' { v = v[:j]; break }
						}
						if v != "" {
							var n int
							_, _ = fmt.Sscanf(v, "%d", &n)
							if n >= 0 && n <= 100 { progress = n }
						}
					}
					if phase != "" && progress > 0 {
						break
					}
				}
			}

			if j.State == jobs.StateDone {
				progress = 100
				if strings.TrimSpace(phase) == "" {
					phase = "Completado"
				}
			} else if strings.TrimSpace(phase) == "" && j.Type == jobs.TypeUpload {
				phase = "UPLOAD"
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
