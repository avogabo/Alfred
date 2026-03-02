package runner

import (
	"strings"

	"github.com/avogabo/AlfredEDR/internal/config"
)

func par2Command(cfg config.Config) string {
	switch strings.ToLower(strings.TrimSpace(cfg.Upload.Par.Engine)) {
	case "classic":
		return "/usr/bin/par2"
	default:
		// turbo build is installed under /usr/local/bin/par2
		return "par2"
	}
}
