package library

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/avogabo/AlfredEDR/internal/config"
)

type FileBotResult struct {
	Title string
	Year  int
	TMDB  int
}

var reFBTo = regexp.MustCompile(`(?i)\bto\s+\[(.+)\]`)
var reTMDB = regexp.MustCompile(`(?i)tmdb-([0-9]+)`)
var reFBYear = regexp.MustCompile(`\((\d{4})\)`)

func ResolveWithFileBot(ctx context.Context, cfg config.Config, filename string) (FileBotResult, bool) {
	rn := cfg.Rename
	if strings.ToLower(strings.TrimSpace(rn.Provider)) != "filebot" || !rn.FileBot.Enabled {
		return FileBotResult{}, false
	}
	bin := strings.TrimSpace(rn.FileBot.Binary)
	if bin == "" {
		bin = "/usr/local/bin/filebot"
	}
	if _, err := os.Stat(bin); err != nil {
		return FileBotResult{}, false
	}

	g := GuessFromFilename(filename)
	format := "{n} ({y}) tmdb-{id}"
	if g.IsSeries {
		format = "{n} ({y}) tmdb-{id}"
	}
	lang := strings.TrimSpace(rn.FileBot.Language)
	if lang == "" {
		lang = "es"
	}
	db := strings.TrimSpace(rn.FileBot.DB)
	if db == "" {
		db = "TheMovieDB"
	}

	tmpDir, err := os.MkdirTemp("", "edr-fb-*")
	if err != nil {
		return FileBotResult{}, false
	}
	defer os.RemoveAll(tmpDir)

	fake := filepath.Join(tmpDir, filename)
	_ = os.MkdirAll(filepath.Dir(fake), 0o755)
	if err := os.WriteFile(fake, []byte("x"), 0o644); err != nil {
		return FileBotResult{}, false
	}

	// Do not run --license on each resolve call.
	// License activation is handled at container startup and should persist under /config/filebot.
	out, _ := runFB(ctx, bin, "-rename", fake, "--db", db, "--lang", lang, "--format", format, "--action", "test")
	m := reFBTo.FindStringSubmatch(out)
	if len(m) != 2 {
		return FileBotResult{}, false
	}
	base := filepath.Base(strings.TrimSpace(m[1]))
	baseNoExt := strings.TrimSuffix(base, filepath.Ext(base))

	res := FileBotResult{}
	if mm := reTMDB.FindStringSubmatch(strings.ToLower(baseNoExt)); len(mm) == 2 {
		if id, e := strconv.Atoi(mm[1]); e == nil {
			res.TMDB = id
		}
	}
	if ym := reFBYear.FindStringSubmatch(baseNoExt); len(ym) == 2 {
		if y, e := strconv.Atoi(ym[1]); e == nil {
			res.Year = y
		}
	}
	// title = remove year/tmdb tail
	t := reTMDB.ReplaceAllString(baseNoExt, "")
	t = reFBYear.ReplaceAllString(t, "")
	res.Title = strings.TrimSpace(strings.Join(strings.Fields(t), " "))
	if res.Title == "" {
		res.Title = g.Title
	}
	return res, true
}

func runFB(ctx context.Context, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Env = append(os.Environ(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "JAVA_TOOL_OPTIONS=-Dfile.encoding=UTF-8 -Dsun.jnu.encoding=UTF-8")
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	err := cmd.Run()
	return b.String(), err
}
