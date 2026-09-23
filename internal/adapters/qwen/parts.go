package qwen

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// part is one Gemini API Part as Qwen Code stores it in message content.
type part struct {
	Text             *string           `json:"text"`
	Thought          json.RawMessage   `json:"thought"` // true on reasoning text; never archived
	FunctionCall     *functionCall     `json:"functionCall"`
	FunctionResponse *functionResponse `json:"functionResponse"`
	InlineData       *blob             `json:"inlineData"`
	FileData         *blob             `json:"fileData"`
}

type functionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type functionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// blob is an attachment; only its type is read (never the base64 data).
type blob struct {
	MimeType string `json:"mimeType"`
}

func (p part) isThought() bool { return string(bytes.TrimSpace(p.Thought)) == "true" }

const (
	// refFilesMarker starts the @file contents Qwen appends to a prompt as
	// separate parts (atCommandProcessor); what follows is file text, not the prompt.
	refFilesMarker = "--- Content from referenced files ---"
	hookOpen       = "<qwen:user-prompt-submit-context>"
	hookClose      = "</qwen:user-prompt-submit-context>"
	maxTitleRunes  = 100
	maxArgsRunes   = 200
)

// toolSummaryKeys are the tool arguments that describe a call best, in order.
var toolSummaryKeys = []string{"command", "file_path", "absolute_path", "path", "pattern", "url", "query", "description", "prompt"}

// parsePartList decodes a PartListUnion: a string, one part, or a list of
// strings and parts. Undecodable content yields nil.
func parsePartList(raw json.RawMessage) []part {
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
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		var out []part
		for _, it := range items {
			out = append(out, parsePartList(it)...)
		}
		return out
	}
	return nil
}

// userText returns the text the user typed from a user message's parts:
// thought parts, @file contents after refFilesMarker and a trailing
// hook-context part are dropped. attachments holds one placeholder per
// image or file part ("[image]" / "[file]").
func userText(parts []part) (text string, attachments []string) {
	var b strings.Builder
	for i, p := range parts {
		switch {
		case p.InlineData != nil:
			attachments = append(attachments, placeholder(p.InlineData.MimeType))
		case p.FileData != nil:
			attachments = append(attachments, placeholder(p.FileData.MimeType))
		case p.Text == nil || p.isThought():
		case strings.TrimSpace(*p.Text) == refFilesMarker:
			return b.String(), attachments
		case i > 0 && i == len(parts)-1 && isHookContext(*p.Text):
		default:
			b.WriteString(*p.Text)
		}
	}
	return b.String(), attachments
}

func placeholder(mime string) string {
	if strings.HasPrefix(strings.ToLower(mime), "image/") {
		return "[image]"
	}
	return "[file]"
}

func isHookContext(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, hookOpen) && strings.HasSuffix(s, hookClose)
}

// withAttachments appends attachment placeholders to a message text.
func withAttachments(text string, attachments []string) string {
	if len(attachments) == 0 {
		return text
	}
	a := strings.Join(attachments, " ")
	if strings.TrimSpace(text) == "" {
		return a
	}
	return text + "\n" + a
}

// assistantMessages turns model parts into messages in order: consecutive
// text parts become one text message, each function call a tool_call.
// Thought parts are dropped.
func assistantMessages(parts []part, at time.Time) []model.Message {
	var out []model.Message
	var b strings.Builder
	flush := func() {
		if strings.TrimSpace(b.String()) != "" {
			out = append(out, model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: at, Text: b.String()})
		}
		b.Reset()
	}
	for _, p := range parts {
		switch {
		case p.FunctionCall != nil:
			flush()
			out = append(out, toolCall(p.FunctionCall.Name, p.FunctionCall.Args, at))
		case p.FunctionResponse != nil:
			flush()
			if m, ok := toolOutput(toolOutputText(p.FunctionResponse.Response), at); ok {
				out = append(out, m)
			}
		case p.Text != nil && !p.isThought():
			b.WriteString(*p.Text)
		}
	}
	flush()
	return out
}

// toolOutputs returns one tool_output message per function response part.
func toolOutputs(parts []part, at time.Time) []model.Message {
	var out []model.Message
	for _, p := range parts {
		if p.FunctionResponse == nil {
			continue
		}
		if m, ok := toolOutput(toolOutputText(p.FunctionResponse.Response), at); ok {
			out = append(out, m)
		}
	}
	return out
}

func toolCall(name string, args json.RawMessage, at time.Time) model.Message {
	return model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at, Text: toolCallText(name, args)}
}

func toolOutput(text string, at time.Time) (model.Message, bool) {
	if strings.TrimSpace(text) == "" {
		return model.Message{}, false
	}
	return model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, At: at, Text: truncateBytes(text, model.MaxToolOutput)}, true
}

// toolCallText is "name: <command, path, pattern, ... or the arguments JSON>".
func toolCallText(name string, args json.RawMessage) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) == nil {
		for _, k := range toolSummaryKeys {
			if s := jsonString(m[k]); strings.TrimSpace(s) != "" {
				return name + ": " + truncateBytes(s, model.MaxToolOutput)
			}
		}
		if len(m) == 0 {
			return name
		}
	}
	c := compactJSON(args)
	if c == "" || c == "null" {
		return name
	}
	return name + ": " + truncateRunes(c, maxArgsRunes)
}

// toolOutputText extracts the readable output of a function response
// ({"output": …}, {"error": …}, a plain string, or the JSON itself).
func toolOutputText(resp json.RawMessage) string {
	resp = bytes.TrimSpace(resp)
	if len(resp) == 0 || string(resp) == "null" {
		return ""
	}
	if s, ok := asString(resp); ok {
		return s
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(resp, &m) == nil {
		for _, k := range []string{"output", "error", "llmContent", "content", "result"} {
			v, ok := m[k]
			if !ok {
				continue
			}
			if s := jsonString(v); s != "" {
				return s
			}
			var e struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(v, &e) == nil && e.Message != "" {
				return e.Message
			}
		}
	}
	return compactJSON(resp)
}

// resultText returns the text of a tool result stored as a PartListUnion.
func resultText(raw json.RawMessage) string {
	var texts []string
	for _, p := range parsePartList(raw) {
		switch {
		case p.FunctionResponse != nil:
			if s := toolOutputText(p.FunctionResponse.Response); s != "" {
				texts = append(texts, s)
			}
		case p.Text != nil && !p.isThought() && strings.TrimSpace(*p.Text) != "":
			texts = append(texts, *p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func asString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func jsonString(raw json.RawMessage) string {
	s, _ := asString(raw)
	return s
}

func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return strings.TrimSpace(string(raw))
	}
	return b.String()
}

// truncateBytes cuts s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func truncateRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// titleFrom collapses whitespace and keeps the first maxTitleRunes runes.
func titleFrom(s string) string {
	return truncateRunes(strings.Join(strings.Fields(s), " "), maxTitleRunes)
}

// parseTime parses an ISO 8601 timestamp to UTC.
func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// span tracks the first and last timestamps seen.
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
