package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/avogabo/AlfredEDR/internal/api"
	"github.com/avogabo/AlfredEDR/internal/backup"
	"github.com/avogabo/AlfredEDR/internal/config"
	"github.com/avogabo/AlfredEDR/internal/fusefs"
	"github.com/avogabo/AlfredEDR/internal/health"
	"github.com/avogabo/AlfredEDR/internal/runner"
	"github.com/avogabo/AlfredEDR/internal/watch"
)

func main() {
	var cfgPath string
	var enableFuse bool
	flag.StringVar(&cfgPath, "config", "/config/config.json", "path to config file (json)")
	flag.BoolVar(&enableFuse, "fuse", true, "enable FUSE mounts at <mount_point>/*")
	flag.Parse()

	// First-run UX: if config.json is missing, create a safe default so the service can boot.
	if err := config.EnsureConfigFile(cfgPath); err != nil {
		log.Fatalf("config bootstrap: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("config load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config validate: %v", err)
	}

	dbPath := "/config/alfrededr.db"
	// One-shot DB reset marker (created by API/UI): delete ONLY the DB files, keep config.json.
	resetMarker := "/config/.reset-db"
	if _, err := os.Stat(resetMarker); err == nil {
		_ = os.Remove(dbPath)
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
		_ = os.Remove(resetMarker)
	}

	srv, closeFn, err := api.New(cfg, api.Options{ConfigPath: cfgPath, DBPath: dbPath})
	if err != nil {
		log.Fatalf("api init: %v", err)
	}
	defer func() {
		if closeFn != nil {
			_ = closeFn()
		}
	}()

	// Start background watcher + runner.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if srvJobs := srv.Jobs(); srvJobs != nil {
		// Start watchers (NZB/media) and runner (job executor) independently.
		if cfg.Watch.NZB.Enabled || cfg.Watch.Media.Enabled {
			w := watch.New(srvJobs, cfg.Watch.NZB, cfg.Watch.Media)
			go w.Run(ctx)
		}

		if cfg.Runner.Enabled {
			r := runner.New(srvJobs)
			r.Mode = cfg.Runner.Mode
			r.GetConfig = srv.Config
			go r.Run(ctx)
		}

		// Backup scheduler (reads latest config from the API server)
		sched := &backup.Scheduler{
			DBPath: dbPath,
			Cfg: func() backup.Config {
				c := srv.Config().Backups
				return backup.Config{
					Enabled:    c.Enabled,
					Dir:        c.Dir,
					EveryMins:  c.EveryMins,
					Keep:       c.Keep,
					CompressGZ: c.CompressGZ,
				}
			},
		}
		go sched.Run(ctx)

		// Health scan scheduler (enqueues health_scan_nzb according to config)
		hs := &health.Scheduler{
			Jobs: srvJobs,
			Cfg: func() config.HealthConfig {
				return srv.Config().Health
			},
		}
		go hs.Run(ctx)

		if enableFuse {
			log.Printf("FUSE library mount disabled: import/library pipeline removed")
		}
	}

	log.Printf("AlfredEDR listening on %s", cfg.Server.Addr)
	if err := http.ListenAndServe(cfg.Server.Addr, srv.Handler()); err != nil {
		log.Fatalf("server: %v", err)
	}
}
