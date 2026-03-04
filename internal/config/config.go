package config

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

type Paths struct {
	HostRoot   string `json:"host_root"`
	MountPoint string `json:"mount_point"`
	NzbInbox   string `json:"nzb_inbox"`
	MediaInbox string `json:"media_inbox"`
	CacheDir   string `json:"cache_dir"`

	// CacheMaxBytes is a best-effort size limit for /cache contents.
	CacheMaxBytes int64 `json:"cache_max_bytes"`
}

type Server struct {
	Addr string `json:"addr"`
}

type Runner struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"` // "stub" or "exec" (dev)
}

type UploadPar struct {
	Enabled           bool   `json:"enabled"`
	RedundancyPercent int    `json:"redundancy_percent"` // e.g. 20
	KeepParityFiles   bool   `json:"keep_parity_files"`
	Dir               string `json:"dir"` // where to store parity files if KeepParityFiles=true (e.g. /host/inbox/par2)
}

type Upload struct {
	Provider string    `json:"provider"` // "ngpost" | "nyuu"
	Par      UploadPar `json:"par"`
}

type FileBot struct {
	Enabled      bool   `json:"enabled"`
	Binary       string `json:"binary"`       // e.g. /usr/local/bin/filebot
	LicensePath  string `json:"license_path"` // e.g. /config/filebot/license.psm
	DB           string `json:"db"`           // e.g. TheMovieDB
	Language     string `json:"language"`
	MovieFormat  string `json:"movie_format"`
	SeriesFormat string `json:"series_format"`
	Action       string `json:"action"` // test|move|copy|symlink
}

type Rename struct {
	Provider string  `json:"provider"` // builtin|filebot
	FileBot  FileBot `json:"filebot"`
}

type WatchKind struct {
	Enabled   bool   `json:"enabled"`
	Dir       string `json:"dir"`
	Recursive bool   `json:"recursive"`
}

type Watch struct {
	NZB   WatchKind `json:"nzb"`
	Media WatchKind `json:"media"`
}

type Backups struct {
	Enabled     bool   `json:"enabled"`
	Dir         string `json:"dir"`          // inside container, e.g. "/backups" (mount a volume)
	EveryMins   int    `json:"every_mins"`   // 0 disables scheduling
	Keep        int    `json:"keep"`         // rotation count
	CompressGZ  bool   `json:"compress_gz"`  // store .gz
	AutoRestore bool   `json:"auto_restore"` // reserved
}

type WebDAVMount struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	User      string `json:"user"`
	Pass      string `json:"pass"`
	MountPath string `json:"mount_path"`
}

type Config struct {
	Server Server `json:"server"`
	Paths  Paths  `json:"paths"`
	Runner Runner `json:"runner"`

	NgPost      NgPost           `json:"ngpost"`
	Download    DownloadProvider `json:"download"`
	WebDAVMount WebDAVMount      `json:"webdav_mount"`

	Library  Library      `json:"library"`
	Metadata Metadata     `json:"metadata"`
	Plex     Plex         `json:"plex"`
	Upload   Upload       `json:"upload"`
	Rename   Rename       `json:"rename"`
	Watch    Watch        `json:"watch"`
	Backups  Backups      `json:"backups"`
	Health   HealthConfig `json:"health"`
}

func Default() Config {
	return Config{
		Server: Server{Addr: ":1516"},
		Paths: Paths{
			HostRoot:      "/host",
			MountPoint:    "/host/mount",
			NzbInbox:      "/host/inbox/nzb",
			MediaInbox:    "/host/inbox/media",
			CacheDir:      "/cache",
			CacheMaxBytes: 50 * 1024 * 1024 * 1024,
		},
		Runner: Runner{Enabled: true, Mode: "exec"}, // default: real execution (not stub)

		NgPost:      NgPost{Enabled: false, Port: 563, SSL: true, Connections: 20, Threads: 2, OutputDir: "/host/inbox/nzb", Obfuscate: true},
		Download:    DownloadProvider{Enabled: false, Port: 563, SSL: true, Connections: 20, PrefetchSegments: 50},
		WebDAVMount: WebDAVMount{Enabled: false, URL: "http://127.0.0.1:1516/webdav", MountPath: "/host/mount/library"},
		Library:     (Library{Enabled: true, UppercaseFolders: true}).withDefaults(),
		Metadata: (Metadata{}).withDefaults(),
		Plex:     (Plex{}).withDefaults(),
		Upload:   Upload{Provider: "nyuu", Par: UploadPar{Enabled: true, RedundancyPercent: 20, KeepParityFiles: true, Dir: "/host/inbox/par2"}},
		Rename: Rename{Provider: "filebot", FileBot: FileBot{
			Enabled:      true,
			Binary:       "/usr/local/bin/filebot",
			LicensePath:  "/config/filebot/license.psm",
			DB:           "TheMovieDB",
			Language:     "es",
			MovieFormat:  "{n} ({y})",
			SeriesFormat: "{n} - {s00e00} - {t}",
			Action:       "test",
		}},
		Watch: Watch{
			NZB:   WatchKind{Enabled: true, Dir: "/host/inbox/nzb", Recursive: true},
			Media: WatchKind{Enabled: true, Dir: "/host/inbox/media", Recursive: true},
		},
		Backups: (Backups{Enabled: false, Dir: "/backups", EveryMins: 0, Keep: 30, CompressGZ: true}),
		Health: HealthConfig{
			Enabled:   true,
			BackupDir: "/cache/health-bak",
			Scan: HealthScanConfig{
				Enabled:            true,
				IntervalHours:      24,
				ChunkEveryHours:    24,
				MaxDurationMinutes: 180,
				AutoRepair:         true,
			},
			Lock: HealthLockConfig{LockTTLHours: 6},
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	// Detect whether runner.enabled was explicitly set in the JSON.
	// This preserves backward compatibility: older configs won't have it, and we want
	// Runner.Enabled to default to true.
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	runnerEnabledPresent := false
	if r, ok := raw["runner"].(map[string]any); ok {
		if _, ok := r["enabled"]; ok {
			runnerEnabledPresent = true
		}
	}

	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	// Fill defaults for nested configs that may be missing
	cfg.Library = cfg.Library.withDefaults()
	// UX: library-auto is a core feature and should be enabled by default.
	// We currently treat it as always-on to match expected workflow.
	cfg.Library.Enabled = true
	cfg.Metadata = cfg.Metadata.withDefaults()
	cfg.Plex = cfg.Plex.withDefaults()
	if cfg.Runner.Mode == "" {
		cfg.Runner.Mode = "exec"
	}
	if !runnerEnabledPresent {
		cfg.Runner.Enabled = true
	}
	if cfg.Upload.Provider == "" {
		cfg.Upload.Provider = "nyuu"
	}
	if strings.TrimSpace(cfg.WebDAVMount.MountPath) == "" {
		cfg.WebDAVMount.MountPath = "/host/mount/library"
	}
	// FileBot is mandatory for both rename phases.
	cfg.Rename.Provider = "filebot"
	cfg.Rename.FileBot.Enabled = true
	if strings.TrimSpace(cfg.Rename.FileBot.Binary) == "" {
		cfg.Rename.FileBot.Binary = "/usr/local/bin/filebot"
	}
	if strings.TrimSpace(cfg.Rename.FileBot.LicensePath) == "" {
		cfg.Rename.FileBot.LicensePath = "/config/filebot/license.psm"
	}
	if strings.TrimSpace(cfg.Rename.FileBot.DB) == "" {
		cfg.Rename.FileBot.DB = "TheMovieDB"
	}
	if strings.TrimSpace(cfg.Rename.FileBot.Language) == "" {
		cfg.Rename.FileBot.Language = "es"
	}
	cfg.Rename.FileBot.Action = "test"
	// Upload PAR defaults
	if cfg.Upload.Par.RedundancyPercent <= 0 {
		cfg.Upload.Par.RedundancyPercent = 20
	}
	if cfg.Upload.Par.KeepParityFiles && cfg.Upload.Par.Dir == "" {
		cfg.Upload.Par.Dir = "/host/inbox/par2"
	}
	// Health defaults
	if strings.TrimSpace(cfg.Health.BackupDir) == "" {
		cfg.Health.BackupDir = "/cache/health-bak"
	}
	if cfg.Health.Scan.IntervalHours <= 0 {
		cfg.Health.Scan.IntervalHours = 24
	}
	if cfg.Health.Scan.ChunkEveryHours <= 0 {
		cfg.Health.Scan.ChunkEveryHours = 24
	}
	if cfg.Health.Scan.MaxDurationMinutes <= 0 {
		cfg.Health.Scan.MaxDurationMinutes = 180
	}
	// AutoRepair default: true
	if !cfg.Health.Scan.AutoRepair {
		// leave as-is; user can disable
	}
	if cfg.Health.Lock.LockTTLHours <= 0 {
		cfg.Health.Lock.LockTTLHours = 6
	}

	// Watch defaults
	if cfg.Watch.NZB.Dir == "" {
		cfg.Watch.NZB.Dir = cfg.Paths.NzbInbox
	}
	if cfg.Watch.Media.Dir == "" {
		cfg.Watch.Media.Dir = cfg.Paths.MediaInbox
	}
	// Backward compat: if watch.enabled fields are missing, keep previous behavior when runner.enabled=true.
	// (Older configs had no watch section.)
	// We detect presence via raw map keys.
	if _, ok := raw["watch"]; !ok {
		cfg.Watch.NZB.Enabled = cfg.Runner.Enabled
		cfg.Watch.Media.Enabled = cfg.Runner.Enabled
		cfg.Watch.NZB.Recursive = true
		cfg.Watch.Media.Recursive = true
	}
	if cfg.Backups.Dir == "" {
		cfg.Backups.Dir = "/backups"
	}
	if cfg.Backups.Keep <= 0 {
		cfg.Backups.Keep = 30
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Server.Addr == "" {
		return errors.New("server.addr required")
	}
	if c.Paths.MountPoint == "" {
		return errors.New("paths.mount_point required")
	}
	// Runner
	switch c.Runner.Mode {
	case "", "stub", "exec":
		// ok
	default:
		return errors.New("runner.mode must be stub|exec")
	}
	// Upload provider
	switch c.Upload.Provider {
	case "", "ngpost", "nyuu":
		// ok
	default:
		return errors.New("upload.provider must be ngpost|nyuu")
	}
	// Rename provider (mandatory: filebot)
	if strings.TrimSpace(c.Rename.Provider) != "" && c.Rename.Provider != "filebot" {
		return errors.New("rename.provider must be filebot")
	}

	// Plex
	if c.Plex.Enabled {
		if c.Plex.BaseURL == "" {
			return errors.New("plex.base_url required when plex.enabled")
		}
		if c.Plex.Token == "" {
			return errors.New("plex.token required when plex.enabled")
		}
		if c.Plex.PlexRoot == "" {
			return errors.New("plex.plex_root required when plex.enabled")
		}
	}

	// Health
	if strings.TrimSpace(c.Health.BackupDir) == "" {
		return errors.New("health.backup_dir required")
	}

	// Backups
	if c.Backups.Dir == "" {
		return errors.New("backups.dir required")
	}
	if c.Backups.Keep < 0 {
		return errors.New("backups.keep must be >= 0")
	}
	if c.Backups.EveryMins < 0 {
		return errors.New("backups.every_mins must be >= 0")
	}
	return nil
}
