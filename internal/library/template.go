package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	reYear      = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reSxxExx    = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,2})\b`)
	reNxxXxx    = regexp.MustCompile(`\b(\d{1,2})x(\d{1,2})\b`)
	reSeasonDir = regexp.MustCompile(`(?i)\btemporada\s*(\d{1,2})\b|\bseason\s*(\d{1,2})\b`)
)

type Guess struct {
	IsSeries bool
	Title    string
	Year     int
	Season   int
	Episode  int
	Ext      string
	Quality  string // 1080 or 4K
}

func GuessFromFilename(name string) Guess {
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	g := Guess{Title: stem, Ext: ext, Quality: "1080"}
	low := strings.ToLower(stem)
	if strings.Contains(low, "2160") || strings.Contains(low, "4k") {
		g.Quality = "4K"
	}

	if loc := reSxxExx.FindStringSubmatchIndex(stem); len(loc) >= 6 {
		g.IsSeries = true
		g.Season, _ = strconv.Atoi(stem[loc[2]:loc[3]])
		g.Episode, _ = strconv.Atoi(stem[loc[4]:loc[5]])
		stem = strings.TrimSpace(stem[:loc[0]])
	}
	if !g.IsSeries {
		if loc := reNxxXxx.FindStringSubmatchIndex(stem); len(loc) >= 6 {
			g.IsSeries = true
			g.Season, _ = strconv.Atoi(stem[loc[2]:loc[3]])
			g.Episode, _ = strconv.Atoi(stem[loc[4]:loc[5]])
			stem = strings.TrimSpace(stem[:loc[0]])
		}
	}
	if !g.IsSeries {
		if m := reSeasonDir.FindStringSubmatch(stem); len(m) >= 3 {
			seasonText := m[1]
			if seasonText == "" {
				seasonText = m[2]
			}
			if seasonText != "" {
				g.IsSeries = true
				g.Season, _ = strconv.Atoi(seasonText)
				stem = strings.TrimSpace(reSeasonDir.ReplaceAllString(stem, ""))
			}
		}
	}

	if ym := reYear.FindStringSubmatch(stem); len(ym) == 2 {
		g.Year, _ = strconv.Atoi(ym[1])
	}

	// crude title cleanup: normalize separators
	clean := strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(stem)
	clean = strings.Join(strings.Fields(clean), " ")
	clean = strings.TrimSpace(clean)
	g.Title = clean
	return g
}

func InitialFolder(title string) string {
	if title == "" {
		return "#"
	}
	n := Normalize(title)
	r, _ := utf8FirstRune(n)
	if r == 0 {
		return "#"
	}
	if unicode.IsDigit(r) {
		return "#"
	}
	if unicode.IsLetter(r) {
		return strings.ToUpper(string(r))
	}
	return "#"
}

func Normalize(s string) string {
	// remove accents using NFD and drop marks
	ss := norm.NFD.String(s)
	b := strings.Builder{}
	b.Grow(len(ss))
	for _, r := range ss {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	out = strings.TrimSpace(out)
	return out
}

func utf8FirstRune(s string) (rune, int) {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return 0, 0
	}
	return r, size
	return 0, 0
}

func ApplyUppercaseFolders(p string) string {
	parts := strings.Split(p, string(filepath.Separator))
	for i := range parts {
		// keep filenames as-is (last part if contains dot)
		if i == len(parts)-1 && strings.Contains(parts[i], ".") {
			continue
		}
		parts[i] = strings.ToUpper(parts[i])
	}
	return strings.Join(parts, string(filepath.Separator))
}
