package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/db"
)

type Type string

type State string

const (
	TypeImport       Type = "import_nzb"
	TypeUpload       Type = "upload_media"
	TypeHealthRepair Type = "health_repair_nzb"
	TypeHealthScan   Type = "health_scan_nzb"

	StateQueued  State = "queued"
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
)

type Job struct {
	ID        string          `json:"id"`
	Type      Type            `json:"type"`
	State     State           `json:"state"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Payload   json.RawMessage `json:"payload"`
	Error     *string         `json:"error,omitempty"`
}

type Store struct {
	db *db.DB
}

func NewStore(d *db.DB) *Store { return &Store{db: d} }

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) Enqueue(ctx context.Context, t Type, payload any) (*Job, error) {
	if t == "" {
		return nil, errors.New("job type required")
	}
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// Dedupe active jobs by (type + payload.path) to avoid double enqueue
	// when the same file is picked by watcher and manual action at once.
	if path := payloadPath(p); path != "" {
		rows, err := s.db.SQL.QueryContext(ctx,
			`SELECT id,type,state,created_at,updated_at,payload_json,error FROM jobs WHERE type=? AND state IN (?,?) ORDER BY created_at DESC LIMIT 100`,
			string(t), string(StateQueued), string(StateRunning),
		)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var (
					id, typ, st, payloadJSON string
					created, updated         int64
					errStr                   *string
				)
				if err := rows.Scan(&id, &typ, &st, &created, &updated, &payloadJSON, &errStr); err != nil {
					continue
				}
				if strings.EqualFold(payloadPath([]byte(payloadJSON)), path) {
					return &Job{
						ID:        id,
						Type:      Type(typ),
						State:     State(st),
						CreatedAt: time.Unix(created, 0),
						UpdatedAt: time.Unix(updated, 0),
						Payload:   json.RawMessage(payloadJSON),
						Error:     errStr,
					}, nil
				}
			}
		}
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	_, err = s.db.SQL.ExecContext(ctx, `INSERT INTO jobs(id,type,state,created_at,updated_at,payload_json) VALUES(?,?,?,?,?,?)`,
		id, string(t), string(StateQueued), now.Unix(), now.Unix(), string(p))
	if err != nil {
		return nil, err
	}
	return &Job{ID: id, Type: t, State: StateQueued, CreatedAt: now, UpdatedAt: now, Payload: p}, nil
}

func payloadPath(payloadJSON []byte) string {
	var m map[string]any
	if err := json.Unmarshal(payloadJSON, &m); err != nil {
		return ""
	}
	v, _ := m["path"].(string)
	return strings.TrimSpace(v)
}

func (s *Store) List(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT id,type,state,created_at,updated_at,payload_json,error FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

func (s *Store) ListByState(ctx context.Context, state State, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT id,type,state,created_at,updated_at,payload_json,error FROM jobs WHERE state=? ORDER BY updated_at DESC LIMIT ?`, string(state), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

func scanJobs(rows interface{ Next() bool; Scan(dest ...any) error; Err() error }) ([]Job, error) {
	out := make([]Job, 0)
	for rows.Next() {
		var (
			id, typ, st, payload string
			created, updated     int64
			errStr               *string
		)
		if err := rows.Scan(&id, &typ, &st, &created, &updated, &payload, &errStr); err != nil {
			return nil, err
		}
		out = append(out, Job{
			ID:        id,
			Type:      Type(typ),
			State:     State(st),
			CreatedAt: time.Unix(created, 0),
			UpdatedAt: time.Unix(updated, 0),
			Payload:   json.RawMessage(payload),
			Error:     errStr,
		})
	}
	return out, rows.Err()
}

func (s *Store) AppendLog(ctx context.Context, jobID, line string) error {
	_, err := s.db.SQL.ExecContext(ctx, `INSERT INTO job_logs(job_id,ts,line) VALUES(?,?,?)`, jobID, time.Now().Unix(), line)
	return err
}

func (s *Store) GetLogs(ctx context.Context, jobID string, limit int) ([]string, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT line FROM job_logs WHERE job_id=? ORDER BY ts DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	return out, rows.Err()
}
