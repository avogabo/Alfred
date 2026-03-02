package runner

import (
	"strings"

	"github.com/avogabo/AlfredEDR/internal/config"
)

func par2Command(cfg config.Config) string {
	// Repair/verify always uses par2-compatible tooling.
	switch strings.ToLower(strings.TrimSpace(cfg.Upload.Par.Engine)) {
	case "classic":
		return "/usr/bin/par2"
	default:
		// turbo build is installed under /usr/local/bin/par2
		return "par2"
	}
}

func parCreateCommand(cfg config.Config) string {
	switch strings.ToLower(strings.TrimSpace(cfg.Upload.Par.Engine)) {
	case "classic":
		return "/usr/bin/par2"
	case "parpar":
		return "/usr/local/bin/parpar"
	default:
		return "par2"
	}
}
