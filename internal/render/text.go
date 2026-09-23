package render

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxToolOutput is the size above which a tool output is cut (spec §2.4).
const maxToolOutput = 20 * 1024

// ansiEscape matches terminal escape sequences that tool outputs often carry:
// CSI (colors, cursor moves), OSC (titles, hyperlinks; BEL or ST terminated)
// and the short ESC forms (ESC 7, ESC ( B, …).
var ansiEscape = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[ -/]*[0-~])`)

// cleanText makes archived text safe and readable as HTML text: invalid UTF-8
// becomes U+FFFD, terminal escapes and control characters (except tab and
// newline) are removed and line endings become \n.
func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if strings.IndexByte(s, 0x1b) >= 0 {
		s = ansiEscape.ReplaceAllString(s, "")
	}
	if strings.IndexByte(s, '\r') >= 0 {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n':
			return r
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		}
		return r
	}, s)
}

// truncate keeps at most max bytes of s without splitting a rune and returns
// the kept text and the number of bytes cut.
func truncate(s string, max int) (string, int) {
	if len(s) <= max {
		return s, 0
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], len(s) - cut
}

// firstLine returns the first non-empty line of s, whitespace-collapsed and
// cut to max runes with "…". Only the first few KB of a long line are looked
// at, so a multi-megabyte single-line text stays cheap.
func firstLine(s string, max int) string {
	for s != "" {
		var line string
		line, s, _ = strings.Cut(s, "\n")
		long := false
		if lim := 8 * max; len(line) > lim {
			line, _ = truncate(line, lim)
			long = true
		}
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > max {
			return string([]rune(line)[:max]) + "…"
		}
		if long {
			return line + "…"
		}
		return line
	}
	return ""
}

// kb formats a byte count for humans: "512 B", "1.5 KB", "12 KB".
func kb(n int) string {
	switch {
	case n < 1024:
		return itoa(n) + " B"
	case n < 10*1024:
		tenths := (n*10 + 512) / 1024
		return itoa(tenths/10) + "." + itoa(tenths%10) + " KB"
	default:
		return itoa((n+512)/1024) + " KB"
	}
}

// ceilKB is the number of started kilobytes in n bytes.
func ceilKB(n int) int { return (n + 1023) / 1024 }
