package claude

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// titleRunes is the length of a title taken from a prompt.
const titleRunes = 100

// wrapperPrefixes mark user records that Claude Code generates around slash
// commands, shell escapes, reminders and notifications; they are not prompts.
var wrapperPrefixes = []string{
	"<command-name>",
	"<command-message>",
	"<command-args>",
	"<local-command-",
	"<system-reminder>",
	"<task-notification>",
	"<bash-stdout>",
	"<bash-stderr>",
	"Caveat: The messages below",
	"[Request interrupted",
}

// userText returns the text of a user prompt, or ok=false for a wrapper or an
// empty text. A shell escape <bash-input>X</bash-input> becomes "!X"
// (isShell=true: it is kept as a message but never used as the title).
func userText(s string) (text string, isShell, ok bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", false, false
	}
	if rest, found := strings.CutPrefix(t, "<bash-input>"); found {
		if i := strings.Index(rest, "</bash-input>"); i >= 0 {
			rest = rest[:i]
		}
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return "", false, false
		}
		return "!" + rest, true, true
	}
	for _, p := range wrapperPrefixes {
		if strings.HasPrefix(t, p) {
			return "", false, false
		}
	}
	return t, false, true
}

// promptTitle collapses whitespace and keeps the first titleRunes runes. It
// stops early, so a multi-megabyte prompt costs no more than a short one.
func promptTitle(s string) string {
	out := make([]rune, 0, titleRunes)
	pendingSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			pendingSpace = len(out) > 0
			continue
		}
		if pendingSpace {
			if len(out) >= titleRunes {
				break
			}
			out = append(out, ' ')
			pendingSpace = false
		}
		if len(out) >= titleRunes {
			break
		}
		out = append(out, r)
	}
	return strings.TrimRight(string(out), " ")
}

// cleanTitle collapses the whitespace of a user- or agent-given title.
func cleanTitle(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
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

// decode unmarshals data into v and reports whether v is usable. A value of
// an unexpected JSON type only leaves that field unset (encoding/json keeps
// decoding the rest), so it does not make the whole record unusable.
func decode(data []byte, v any) bool {
	if len(data) == 0 {
		return false
	}
	err := json.Unmarshal(data, v)
	var typeErr *json.UnmarshalTypeError
	return err == nil || errors.As(err, &typeErr)
}

// parseTime parses an ISO-8601 timestamp to UTC; zero when absent or invalid.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// span tracks the first and last activity of a session.
type span struct{ first, last time.Time }

func (s *span) add(t time.Time) {
	if t.IsZero() {
		return
	}
	if s.first.IsZero() || t.Before(s.first) {
		s.first = t
	}
	if t.After(s.last) {
		s.last = t
	}
}
