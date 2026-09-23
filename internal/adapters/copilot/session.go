package copilot

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// sessionData is the part of a serialized chat session (ISerializableChatData
// version 3, or the replayed state of a .jsonl log) that Athar uses.
type sessionData struct {
	SessionID       string            `json:"sessionId"`
	CustomTitle     string            `json:"customTitle"`
	CreationDate    json.RawMessage   `json:"creationDate"`
	LastMessageDate json.RawMessage   `json:"lastMessageDate"`
	Requests        []json.RawMessage `json:"requests"`
}

// request is one turn: the user's message and the response parts shown for it.
type request struct {
	Message   json.RawMessage   `json:"message"`
	Response  []json.RawMessage `json:"response"`
	Timestamp json.RawMessage   `json:"timestamp"`
}

// part is one response part. Plain markdown parts have no kind.
type part struct {
	Kind              string          `json:"kind"`
	Value             json.RawMessage `json:"value"`   // markdown: string; thinking: string or list
	Content           json.RawMessage `json:"content"` // markdownContent: {value}
	InlineReference   json.RawMessage `json:"inlineReference"`
	Name              string          `json:"name"`
	ToolID            string          `json:"toolId"`
	InvocationMessage json.RawMessage `json:"invocationMessage"`
	PastTenseMessage  json.RawMessage `json:"pastTenseMessage"`
	ToolSpecificData  json.RawMessage `json:"toolSpecificData"`
	Presentation      string          `json:"presentation"` // "hidden": not shown by VS Code
	URI               json.RawMessage `json:"uri"`          // textEditGroup
}

// convertRequests turns requests[] into messages. skipped counts requests or
// response parts that could not be decoded.
func convertRequests(reqs []json.RawMessage) (msgs []model.Message, last time.Time, skipped int) {
	for _, raw := range reqs {
		var r request
		if json.Unmarshal(raw, &r) != nil {
			skipped++
			continue
		}
		at := msTime(r.Timestamp)
		if at.After(last) {
			last = at
		}
		if text := messageText(r.Message); strings.TrimSpace(text) != "" {
			msgs = append(msgs, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: text})
		}
		out, sk := convertResponse(r.Response, at)
		msgs = append(msgs, out...)
		skipped += sk
	}
	return msgs, last, skipped
}

// messageText reads request.message: {text, parts[{text}]} (or a bare string
// in old files). text already holds the whole prompt; parts are the fallback.
func messageText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var m struct {
		Text  string `json:"text"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	var b strings.Builder
	for _, p := range m.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// convertResponse turns the response parts of one request into assistant
// text (markdown with inline references) and tool calls, in order.
func convertResponse(parts []json.RawMessage, at time.Time) (msgs []model.Message, skipped int) {
	var text strings.Builder
	flush := func() {
		if t := strings.TrimSpace(cleanLinks(text.String(), false)); t != "" {
			msgs = append(msgs, model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: at, Text: t})
		}
		text.Reset()
	}
	edited := map[string]bool{}
	for _, raw := range parts {
		var p part
		if json.Unmarshal(raw, &p) != nil {
			skipped++
			continue
		}
		switch p.Kind {
		case "", "markdownContent", "markdownVuln":
			v := p.Value
			if p.Kind != "" {
				v = p.Content
			}
			text.WriteString(markdownValue(v))
		case "inlineReference":
			text.WriteString(referenceName(p))
		case "toolInvocationSerialized", "toolInvocation":
			if strings.HasPrefix(p.Presentation, "hidden") {
				// The edit tools (copilot_insertEdit, vscode_editFile_internal,
				// …) are hidden and name no file; their textEditGroup parts do.
				continue
			}
			flush()
			msgs = append(msgs, model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at, Text: toolCallText(p)})
		case "textEditGroup", "notebookEditGroup":
			path := uriPath(p.URI)
			if path == "" || edited[path] {
				continue
			}
			edited[path] = true
			flush()
			msgs = append(msgs, model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at, Text: "edit: " + path})
		default:
			// thinking, codeblockUri, undoStop, prepareToolInvocation,
			// progressMessage, progressTask, confirmation, warning, command,
			// elicitation, …: UI state, not conversation text.
		}
	}
	flush()
	return msgs, skipped
}

// markdownValue reads a string or an IMarkdownString {value}.
func markdownValue(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var m struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return m.Value
}

// referenceName is what VS Code shows for an inline reference: a symbol's
// name, else the referenced file's base name.
func referenceName(p part) string {
	if p.Name != "" {
		return p.Name
	}
	var ref struct {
		uriObject
		Name     string          `json:"name"`
		URI      json.RawMessage `json:"uri"`
		Location struct {
			URI json.RawMessage `json:"uri"`
		} `json:"location"`
	}
	if json.Unmarshal(p.InlineReference, &ref) != nil {
		return ""
	}
	switch {
	case ref.Name != "":
		return ref.Name
	case ref.readable() != "":
		return baseName(ref.readable())
	case len(ref.URI) > 0:
		return baseName(uriPath(ref.URI))
	}
	return baseName(uriPath(ref.Location.URI))
}

// toolCallText is "<toolId>: <detail>": the terminal command for terminal
// tools, else the past-tense message ("Read C:\x\y.go, lines 1 to 40"), else
// the invocation message, with file links reduced to paths.
func toolCallText(p part) string {
	name := strings.TrimSpace(p.ToolID)
	if name == "" {
		name = "tool"
	}
	detail := terminalCommand(p.ToolSpecificData)
	if detail == "" {
		detail = markdownValue(p.PastTenseMessage)
	}
	if strings.TrimSpace(detail) == "" {
		detail = markdownValue(p.InvocationMessage)
	}
	detail = strings.TrimSpace(cleanLinks(detail, true))
	if detail == "" {
		return name
	}
	return name + ": " + detail
}

// terminalCommand reads toolSpecificData {kind:"terminal", command} or the
// newer {kind:"terminal", commandLine{original, toolEdited, userEdited}}.
func terminalCommand(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var d struct {
		Kind        string `json:"kind"`
		Command     string `json:"command"`
		CommandLine struct {
			Original   string `json:"original"`
			ToolEdited string `json:"toolEdited"`
			UserEdited string `json:"userEdited"`
		} `json:"commandLine"`
	}
	if json.Unmarshal(raw, &d) != nil || d.Kind != "terminal" {
		return ""
	}
	for _, c := range []string{d.CommandLine.UserEdited, d.CommandLine.ToolEdited, d.CommandLine.Original, d.Command} {
		if strings.TrimSpace(c) != "" {
			return c
		}
	}
	return ""
}

// msTime reads epoch milliseconds (a number or a numeric string). Zero when
// missing or not positive.
func msTime(raw json.RawMessage) time.Time {
	s := strings.Trim(string(bytes.TrimSpace(raw)), `"`)
	if s == "" || s == "null" {
		return time.Time{}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(f)).UTC()
}

// promptTitle is the first prompt on one line, cut to 100 characters.
func promptTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= 100 {
		return s
	}
	return string([]rune(s)[:100])
}
