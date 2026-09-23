package imports

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

const (
	// titleRunes is the length of a title taken from a prompt.
	titleRunes = 100
	// toolArgRunes bounds the argument summary of a tool call.
	toolArgRunes = 200
)

// parseTime reads the timestamp styles the two tools store: RFC 3339 with any
// number of fraction digits and a Z or ±hh:mm zone, SQLite's "YYYY-MM-DD
// HH:MM:SS", and epoch seconds or milliseconds. Unknown values give zero.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		switch {
		case n > 1e17: // nanoseconds
			return time.Unix(0, n).UTC()
		case n > 1e14: // microseconds
			return time.UnixMicro(n).UTC()
		case n > 1e11: // milliseconds
			return time.UnixMilli(n).UTC()
		default:
			return time.Unix(n, 0).UTC()
		}
	}
	return time.Time{}
}

// oneLine collapses all whitespace runs to single spaces and trims.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// promptTitle turns a prompt into a title: whitespace collapsed, first 100 runes.
func promptTitle(s string) string {
	s = oneLine(s)
	if utf8.RuneCountInString(s) <= titleRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:titleRunes]))
}

// stripThinking removes AgentsView's inline "[Thinking]\n…\n[/Thinking]" blocks
// (thinking is never archived) and trims the rest. An unterminated block is
// removed to the end of the text.
func stripThinking(s string) string {
	const open, closeTag = "[Thinking]", "[/Thinking]"
	if !strings.Contains(s, open) {
		return strings.TrimSpace(s)
	}
	var b strings.Builder
	for {
		i := indexAtLineStart(s, open)
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		rest := s[i+len(open):]
		j := strings.Index(rest, closeTag)
		if j < 0 {
			break
		}
		s = strings.TrimLeft(rest[j+len(closeTag):], "\r\n")
	}
	return strings.TrimSpace(b.String())
}

// indexAtLineStart finds sub at the start of s or right after a newline.
func indexAtLineStart(s, sub string) int {
	off := 0
	for {
		i := strings.Index(s[off:], sub)
		if i < 0 {
			return -1
		}
		i += off
		if i == 0 || s[i-1] == '\n' {
			return i
		}
		off = i + len(sub)
	}
}

// wrapperPrefixes start user "messages" that the agent injected rather than the
// user typed (Claude Code command/hook wrappers, Codex context blocks). The
// live adapters drop them; the indexes kept them.
var wrapperPrefixes = []string{
	"<command-name>", "<command-message>", "<command-args>", "<local-command-",
	"<system-reminder>", "<task-notification>", "<bash-stdout>", "<bash-stderr>",
	"Caveat: The messages below", "[Request interrupted",
	"<environment_context>", "<user_instructions>",
}

// userText cleans a user message: injected wrappers give ok=false, and
// "<bash-input>X</bash-input>" (a shell command typed in Claude Code) becomes "!X".
func userText(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, p := range wrapperPrefixes {
		if strings.HasPrefix(s, p) {
			return "", false
		}
	}
	const in, out = "<bash-input>", "</bash-input>"
	if strings.HasPrefix(s, in) {
		if j := strings.Index(s, out); j > 0 {
			return "!" + strings.TrimSpace(s[len(in):j]), true
		}
	}
	return s, s != ""
}

// truncBytes keeps at most n bytes of s, cut on a rune boundary.
func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// truncRunes keeps at most n runes of s.
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// toolOutput trims a tool result to model.MaxToolOutput bytes.
func toolOutput(s string) string {
	return truncBytes(strings.TrimSpace(s), model.MaxToolOutput)
}

// toolCallText renders a tool call as "name: <command, path or pattern>", or
// the first 200 runes of the argument JSON when none of those is present.
func toolCallText(name, input string) string {
	name = strings.TrimSpace(name)
	input = strings.TrimSpace(input)
	if input == "" || input == "{}" || input == "null" {
		return name
	}
	var m map[string]any
	if json.Unmarshal([]byte(input), &m) == nil {
		for _, k := range []string{"command", "cmd", "file_path", "path", "filePath", "pattern", "query", "url"} {
			switch v := m[k].(type) {
			case string:
				if v = strings.TrimSpace(v); v != "" {
					return name + ": " + truncRunes(oneLine(v), toolArgRunes)
				}
			case []any: // e.g. codex shell {"command":["bash","-lc","…"]}
				parts := make([]string, 0, len(v))
				for _, p := range v {
					if s, ok := p.(string); ok {
						parts = append(parts, s)
					}
				}
				if len(parts) > 0 {
					return name + ": " + truncRunes(oneLine(strings.Join(parts, " ")), toolArgRunes)
				}
			}
		}
	}
	return name + ": " + truncRunes(oneLine(input), toolArgRunes)
}
