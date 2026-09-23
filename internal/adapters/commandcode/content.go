package commandcode

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

const (
	imageMarker = "[image]"
	titleRunes  = 100 // prompt titles keep this many runes
	argRunes    = 200 // a tool call's raw-input fallback keeps this many runes
)

// toolArgKeys are the input fields that best describe a tool call, in order
// (Command Code tools: shell_command.command, read_file/write_file/edit_file
// path or file_path, grep/glob pattern, web tools url/query, agent description).
var toolArgKeys = []string{"command", "file_path", "path", "pattern", "url", "query", "description"}

// notices are the CLI's own messages that it stores as user messages
// without a source (besides any text starting with "Error:").
var notices = map[string]bool{"Interrupted by user": true, "Insufficient credits": true}

// transcript accumulates one session while its lines are read.
type transcript struct {
	cwd         string
	msgs        []model.Message
	firstPrompt string // text blocks of the first real user message
	first, last time.Time
}

// observe widens the session's time range with t (zero is ignored).
func (t *transcript) observe(at time.Time) {
	if at.IsZero() {
		return
	}
	if t.first.IsZero() || at.Before(t.first) {
		t.first = at
	}
	if at.After(t.last) {
		t.last = at
	}
}

func (t *transcript) add(role model.Role, kind model.Kind, at time.Time, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if kind == model.KindToolOutput {
		text = truncateUTF8(text, model.MaxToolOutput)
	}
	t.msgs = append(t.msgs, model.Message{Role: role, Kind: kind, At: at, Text: text})
}

// block is one content block, Anthropic-style (v1, v3) or AI-SDK-style (v2).
type block struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`     // text, reasoning
	Name     string          `json:"name"`     // tool_use
	ToolName string          `json:"toolName"` // tool-call, tool-result
	Input    json.RawMessage `json:"input"`    // tool_use, tool-call
	Content  json.RawMessage `json:"content"`  // tool_result: string | [blocks]
	Output   json.RawMessage `json:"output"`   // tool-result: string | {type, value}
}

// parseContent reads a string or an array of blocks. Blocks that cannot be
// decoded are dropped; anything else gives no blocks.
func parseContent(raw json.RawMessage) []block {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return []block{{Type: "text", Text: s}}
		}
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		out := make([]block, 0, len(items))
		for _, it := range items {
			var b block
			if decode(it, &b) {
				out = append(out, b)
			}
		}
		return out
	}
	return nil
}

// message turns one stored message (any format) into model messages.
func (t *transcript) message(role string, content json.RawMessage, meta *wireMeta, at time.Time) {
	if meta != nil && (meta.IsMeta || meta.IsSummary) {
		return // hidden context or a compaction summary
	}
	var textRole model.Role
	textKind := model.KindText
	switch role {
	case "assistant":
		textRole = model.RoleAssistant
	case "user":
		textRole = model.RoleUser // refined per text group below
	case "tool": // v2 tool results
		textRole, textKind = model.RoleTool, model.KindToolOutput
	case "system":
		textRole = model.RoleSystem
	default:
		return
	}

	var texts, promptTexts []string
	flush := func() {
		if len(texts) == 0 {
			return
		}
		text := strings.Join(texts, "\n")
		r, k := textRole, textKind
		if role == "user" {
			r, k = userTextRole(meta, text)
		}
		t.add(r, k, at, text)
		if r == model.RoleUser && t.firstPrompt == "" {
			t.firstPrompt = strings.TrimSpace(strings.Join(promptTexts, "\n"))
		}
		texts, promptTexts = texts[:0], promptTexts[:0]
	}
	for _, b := range parseContent(content) {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				texts = append(texts, b.Text)
				promptTexts = append(promptTexts, b.Text)
			}
		case "image":
			texts = append(texts, imageMarker)
		case "tool_use":
			flush()
			t.add(model.RoleAssistant, model.KindToolCall, at, toolCallText(b.Name, b.Input))
		case "tool-call":
			flush()
			t.add(model.RoleAssistant, model.KindToolCall, at, toolCallText(b.ToolName, b.Input))
		case "tool_result":
			flush()
			t.add(model.RoleTool, model.KindToolOutput, at, toolResultText(b.Content))
		case "tool-result":
			flush()
			t.add(model.RoleTool, model.KindToolOutput, at, toolOutputText(b.Output))
		}
		// thinking, redacted_thinking, reasoning and unknown blocks are dropped.
	}
	flush()
}

// userTextRole classifies the text of a stored user message by its meta.
func userTextRole(meta *wireMeta, text string) (model.Role, model.Kind) {
	var source string
	if meta != nil {
		if meta.IsAutomated {
			return model.RoleSystem, model.KindText // e.g. stop-hook feedback
		}
		source = meta.Source
	}
	switch source {
	case "user", "followup", "steering":
		return model.RoleUser, model.KindText
	case "tool":
		return model.RoleTool, model.KindToolOutput
	case "":
		trimmed := strings.TrimSpace(text)
		if notices[trimmed] || strings.HasPrefix(trimmed, "Error:") {
			return model.RoleSystem, model.KindText
		}
		return model.RoleUser, model.KindText
	default: // "mod:<name>", "branch-summary", ...
		return model.RoleSystem, model.KindText
	}
}

// toolCallText renders a tool call as "name: <main argument>".
func toolCallText(name string, input json.RawMessage) string {
	if name == "" {
		name = "tool"
	}
	arg := ""
	var fields map[string]json.RawMessage
	if decode(input, &fields) {
		for _, k := range toolArgKeys {
			var s string
			if json.Unmarshal(fields[k], &s) == nil && strings.TrimSpace(s) != "" {
				arg = strings.TrimSpace(s)
				break
			}
		}
	}
	if arg == "" {
		if js := compactJSON(input); js != "{}" && js != "null" && js != `""` {
			arg = firstRunes(js, argRunes)
		}
	}
	if arg == "" {
		return name
	}
	return truncateUTF8(name+": "+arg, model.MaxToolOutput)
}

// toolResultText returns the text of an Anthropic tool_result content
// (a string or text blocks).
func toolResultText(raw json.RawMessage) string {
	var parts []string
	for _, b := range parseContent(raw) {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// toolOutputText returns the text of a v2 tool-result output: a string,
// {type, value: string}, or any other JSON (compacted), as the CLI does.
func toolOutputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &o) == nil && len(o.Value) > 0 {
		if json.Unmarshal(o.Value, &s) == nil {
			return s
		}
		return compactJSON(o.Value)
	}
	return compactJSON(raw)
}

// compactJSON returns raw without insignificant spaces, or "" when it is not JSON.
func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return ""
	}
	return buf.String()
}

// promptTitle collapses whitespace and keeps the first titleRunes runes.
func promptTitle(s string) string {
	return strings.TrimSpace(firstRunes(collapseSpace(s), titleRunes))
}

// collapseSpace replaces every run of whitespace with one space and trims.
func collapseSpace(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
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
