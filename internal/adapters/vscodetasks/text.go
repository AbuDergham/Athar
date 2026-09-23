package vscodetasks

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// titleRunes is the length of a title taken from a prompt.
const titleRunes = 100

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

// decode unmarshals data into v and reports whether v is usable. A value of an
// unexpected JSON type only leaves that field unset.
func decode(data []byte, v any) bool {
	if len(bytes.TrimSpace(data)) == 0 {
		return false
	}
	err := json.Unmarshal(data, v)
	var typeErr *json.UnmarshalTypeError
	return err == nil || errors.As(err, &typeErr)
}

// isObject reports whether raw is a JSON object.
func isObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{'
}

// rawString returns a JSON string's value, or "" for any other JSON value.
func rawString(raw json.RawMessage) string {
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// Plausible epoch-millisecond range (2001..2286), to reject seconds and garbage.
const (
	minEpochMs = 1_000_000_000_000
	maxEpochMs = 9_999_999_999_999
)

// msTime turns an epoch-milliseconds JSON number (or numeric string) into UTC.
// An ISO 8601 string is accepted too. Anything else gives the zero time.
func msTime(raw json.RawMessage) time.Time {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}
	}
	s := string(raw)
	if raw[0] == '"' {
		s = rawString(raw)
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC()
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || f < minEpochMs || f > maxEpochMs {
		return time.Time{}
	}
	return time.UnixMilli(int64(f)).UTC()
}

// idTime derives a creation time from a task id: Cline ids are epoch
// milliseconds, Kilo ids are UUIDv7 (48-bit millisecond prefix).
func idTime(id string) time.Time {
	if len(id) == 13 {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil && n >= minEpochMs {
			return time.UnixMilli(n).UTC()
		}
	}
	if len(id) == 36 && id[14] == '7' {
		hex := strings.ReplaceAll(id[:13], "-", "")
		if n, err := strconv.ParseInt(hex, 16, 64); err == nil && n >= minEpochMs && n <= maxEpochMs {
			return time.UnixMilli(n).UTC()
		}
	}
	return time.Time{}
}

// span tracks the earliest and latest of a set of times.
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
