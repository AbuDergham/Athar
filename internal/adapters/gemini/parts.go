package gemini

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// part is one element of a Gemini PartListUnion (content, displayContent,
// toolCalls[].result). Only the fields Athar uses are decoded.
type part struct {
	Text             *string           `json:"text"`
	Thought          bool              `json:"thought"`
	FunctionCall     *functionCall     `json:"functionCall"`
	FunctionResponse *functionResponse `json:"functionResponse"`
}

type functionCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type functionResponse struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// toolCall is one entry of a gemini message's toolCalls[].
type toolCall struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Args          json.RawMessage `json:"args"`
	Result        json.RawMessage `json:"result"`
	ResultDisplay json.RawMessage `json:"resultDisplay"`
	Timestamp     string          `json:"timestamp"`
}

// parseParts decodes a PartListUnion: a string, one part, or an array of
// strings and parts. Undecodable elements are ignored.
func parseParts(raw json.RawMessage) []part {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		return []part{{Text: &s}}
	case '{':
		var p part
		if json.Unmarshal(raw, &p) != nil {
			return nil
		}
		return []part{p}
	case '[':
		var elems []json.RawMessage
		if json.Unmarshal(raw, &elems) != nil {
			return nil
		}
		var out []part
		for _, e := range elems {
			out = append(out, parseParts(e)...)
		}
		return out
	}
	return nil
}

// partsText concatenates the visible text parts (thoughts excluded), as the CLI does.
func partsText(parts []part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Text != nil && !p.Thought {
			b.WriteString(*p.Text)
		}
	}
	return b.String()
}

// ignoredUser mirrors the CLI's isIgnoredUserContent: slash commands, "?"
// help, and injected context are not part of the conversation.
func ignoredUser(trimmed string) bool {
	return trimmed == "" ||
		strings.HasPrefix(trimmed, "/") ||
		strings.HasPrefix(trimmed, "?") ||
		strings.HasPrefix(trimmed, "<session_context>") ||
		strings.HasPrefix(trimmed, "<hook_context>")
}

// callKeys are the argument names that best describe a tool call (command
// line or path), in order of preference.
var callKeys = []string{"command", "file_path", "absolute_path", "dir_path", "path", "pattern", "query", "url", "prompt"}

// callText renders a tool call as "name: <command or path>", falling back to
// the first 200 characters of the arguments' JSON.
func callText(name string, args json.RawMessage) string {
	if name == "" {
		name = "tool"
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) == nil {
		for _, k := range callKeys {
			if s, ok := jsonString(m[k]); ok && strings.TrimSpace(s) != "" {
				return name + ": " + s
			}
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, args) == nil && buf.Len() > 0 && buf.String() != "null" && buf.String() != "{}" {
		return name + ": " + truncateRunes(buf.String(), 200)
	}
	return name
}

// responseText renders a functionResponse.response: its output or error
// string when present (an empty one renders as ""), else the compact JSON.
func responseText(resp json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(resp, &m) == nil {
		known := false
		for _, k := range []string{"output", "error", "content", "result"} {
			s, ok := jsonString(m[k])
			if ok && strings.TrimSpace(s) != "" {
				return s
			}
			known = known || ok
		}
		if known {
			return ""
		}
	}
	if s, ok := jsonString(resp); ok {
		return s
	}
	var buf bytes.Buffer
	if json.Compact(&buf, resp) == nil && buf.String() != "null" && buf.String() != "{}" {
		return buf.String()
	}
	return ""
}

// resultText renders a tool call's result parts, falling back to its
// resultDisplay when that is a plain string.
func resultText(tc toolCall) string {
	var outs []string
	for _, p := range parseParts(tc.Result) {
		switch {
		case p.FunctionResponse != nil:
			if s := responseText(p.FunctionResponse.Response); s != "" {
				outs = append(outs, s)
			}
		case p.Text != nil && !p.Thought && strings.TrimSpace(*p.Text) != "":
			outs = append(outs, *p.Text)
		}
	}
	if len(outs) == 0 {
		if s, ok := jsonString(tc.ResultDisplay); ok {
			outs = append(outs, s)
		}
	}
	return strings.Join(outs, "\n")
}

// jsonString decodes raw when it is a JSON string.
func jsonString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// capOutput keeps at most model.MaxToolOutput bytes, cut at a rune boundary.
func capOutput(s string) string {
	if len(s) <= model.MaxToolOutput {
		return s
	}
	cut := model.MaxToolOutput
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// promptTitle turns a prompt into a title: whitespace collapsed, at most 100 runes.
func promptTitle(s string) string {
	return truncateRunes(strings.Join(strings.Fields(s), " "), 100)
}

// parseTime parses an ISO timestamp to UTC; zero when absent or invalid.
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
