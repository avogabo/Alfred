package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/config"
)

type altMountImportRequest struct {
	FilePath     string `json:"file_path"`
	RelativePath string `json:"relative_path,omitempty"`
}

type altMountImportResponse struct {
	Success bool `json:"success"`
	Data    struct {
		QueueID any    `json:"queue_id"`
		Message string `json:"message"`
	} `json:"data"`
	Message string `json:"message"`
	Details string `json:"details"`
}

func (s *Server) enqueueImportToAltMount(ctx context.Context, cfg config.Config, nzbPath string) (*altMountImportResponse, error) {
	am := cfg.AltMount
	if !am.Enabled {
		return nil, fmt.Errorf("altmount integration disabled")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(am.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("altmount base_url missing")
	}
	apiKey := strings.TrimSpace(am.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("altmount api_key missing")
	}

	relativePath := ""
	localRoot := strings.TrimSpace(am.NzbRootLocal)
	if localRoot == "" {
		localRoot = strings.TrimSpace(cfg.Watch.NZB.Dir)
	}
	if localRoot == "" {
		localRoot = strings.TrimSpace(cfg.Paths.NzbInbox)
	}
	if localRoot != "" {
		if rel, err := filepath.Rel(localRoot, nzbPath); err == nil && !strings.HasPrefix(rel, "..") {
			relativePath = filepath.ToSlash(rel)
		}
	}
	remotePath := nzbPath
	remoteRoot := strings.TrimSpace(am.NzbRootRemote)
	if remoteRoot != "" && relativePath != "" {
		remotePath = filepath.ToSlash(filepath.Join(remoteRoot, relativePath))
	}

	payload := altMountImportRequest{FilePath: remotePath, RelativePath: relativePath}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(baseURL + "/api/import/file")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("apikey", apiKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var out altMountImportResponse
	_ = json.Unmarshal(respBody, &out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(out.Message)
		if msg == "" {
			msg = strings.TrimSpace(string(respBody))
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("altmount import failed: %s", msg)
	}
	return &out, nil
}
