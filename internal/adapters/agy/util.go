package agy

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// titleRunes is the length of a title taken from a prompt.
const titleRunes = 100

// toolArgRunes is how much of an unrecognized tool-argument JSON is kept.
const toolArgRunes = 200

// toolArgKeys are the argument keys of agy tools that carry the command line,
// file paths, search terms or URLs, in the order they are shown.
var toolArgKeys = []string{"CommandLine", "AbsolutePath", "TargetFile", "DirectoryPath", "Query", "query", "SearchPath", "Url"}

// toolCallText renders a tool call as "name: <command line / paths / query>",
// falling back to the first 200 runes of the argument JSON.
func toolCallText(name, args string) string {
	args = strings.TrimSpace(args)
	var detail string
	var m map[string]any
	if args != "" && json.Unmarshal([]byte(args), &m) == nil {
		var parts []string
		for _, k := range toolArgKeys {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, strings.TrimSpace(v))
			}
		}
		switch {
		case len(parts) > 0:
			detail = strings.Join(parts, " ")
		case len(m) > 0:
			detail = firstRunes(args, toolArgRunes)
		}
	} else if args != "" {
		detail = firstRunes(args, toolArgRunes)
	}
	switch {
	case detail == "":
		return name
	case name == "":
		return detail
	}
	return name + ": " + detail
}

// uriToPath turns a file:// URI (as agy stores workspaces) into a Windows path:
// file:///C:/a%20b → C:\a b, file://server/share → \\server\share. The drive
// letter is upper-cased and a trailing separator dropped (except at a drive root).
// Anything that is not a file URI is returned unchanged.
func uriToPath(s string) string {
	if len(s) < 5 || !strings.EqualFold(s[:5], "file:") {
		return s
	}
	var host, p string
	if u, err := url.Parse(s); err == nil {
		host, p = u.Host, u.Path
	} else {
		p = strings.TrimPrefix(s[5:], "//")
		if i := strings.IndexByte(p, '/'); i > 0 {
			host, p = p[:i], p[i:]
		} else if i < 0 {
			host, p = p, ""
		}
	}
	if host != "" && !strings.EqualFold(host, "localhost") {
		return `\\` + host + strings.ReplaceAll(p, "/", `\`)
	}
	if len(p) >= 3 && p[0] == '/' && isDriveLetter(p[1]) && p[2] == ':' {
		p = p[1:]
	}
	if len(p) >= 2 && isDriveLetter(p[0]) && p[1] == ':' {
		p = strings.ToUpper(p[:1]) + strings.ReplaceAll(p[1:], "/", `\`)
		p = strings.TrimRight(p, `\`)
		if len(p) == 2 {
			p += `\`
		}
	}
	return p
}

func isDriveLetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

// shortTitle collapses whitespace and keeps the first 100 runes.
func shortTitle(s string) string {
	return firstRunes(strings.Join(strings.Fields(s), " "), titleRunes)
}

// firstRunes returns at most n runes of s.
func firstRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// msTime converts epoch milliseconds to UTC; zero for non-positive values.
func msTime(ms float64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(ms)).UTC()
}

// widen extends the session's StartedAt/UpdatedAt to include t.
func widen(s *model.Session, t time.Time) {
	if t.IsZero() {
		return
	}
	if s.StartedAt.IsZero() || t.Before(s.StartedAt) {
		s.StartedAt = t
	}
	if t.After(s.UpdatedAt) {
		s.UpdatedAt = t
	}
}
