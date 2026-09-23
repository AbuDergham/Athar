package continuedev

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// historyItem is one entry of a session's history[] (ChatHistoryItem).
// contextItems, editorState, promptLogs and reasoning are deliberately absent.
type historyItem struct {
	Message        json.RawMessage `json:"message"`
	ToolCallStates json.RawMessage `json:"toolCallStates"`
}

// chatMessage is ChatHistoryItem.message.
type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []toolCall      `json:"toolCalls"`
	ToolCallID string          `json:"toolCallId"`
}

type toolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"` // a JSON string, possibly cut short
	} `json:"function"`
}

// toolCallState is ChatHistoryItem.toolCallStates[]: the UI's view of a call.
type toolCallState struct {
	ToolCallID string                     `json:"toolCallId"`
	ParsedArgs map[string]json.RawMessage `json:"parsedArgs"`
	Output     []struct {
		Content string `json:"content"`
	} `json:"output"`
}

// parsedItem is a history item whose message decoded.
type parsedItem struct {
	msg    chatMessage
	states map[string]toolCallState // by toolCallId
}

// convertHistory turns history[] into messages. skipped counts items (or item
// contents) that could not be decoded; ok is false when history is not a list.
func convertHistory(raw json.RawMessage) (msgs []model.Message, skipped int, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, 0, true
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, 0, false
	}

	parsed := make([]parsedItem, 0, len(items))
	answered := map[string]bool{} // tool call ids that have a role "tool" message
	for _, it := range items {
		var h historyItem
		if json.Unmarshal(it, &h) != nil {
			skipped++
			continue
		}
		var m chatMessage
		if len(h.Message) == 0 || json.Unmarshal(h.Message, &m) != nil || m.Role == "" {
			skipped++
			continue
		}
		p := parsedItem{msg: m}
		var states []toolCallState
		if len(h.ToolCallStates) > 0 && json.Unmarshal(h.ToolCallStates, &states) == nil {
			for _, s := range states {
				if s.ToolCallID != "" {
					if p.states == nil {
						p.states = map[string]toolCallState{}
					}
					p.states[s.ToolCallID] = s
				}
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
		parsed = append(parsed, p)
	}

	for _, p := range parsed {
		m := p.msg
		switch m.Role {
		case "user":
			text, ok := contentText(m.Content)
			if !ok {
				skipped++
				continue
			}
			if strings.TrimSpace(text) != "" {
				msgs = append(msgs, model.Message{Role: model.RoleUser, Kind: model.KindText, Text: text})
			}
		case "assistant":
			text, ok := contentText(m.Content)
			if !ok {
				skipped++ // the tool calls below may still be readable
			}
			if strings.TrimSpace(text) != "" {
				msgs = append(msgs, model.Message{Role: model.RoleAssistant, Kind: model.KindText, Text: text})
			}
			for _, tc := range m.ToolCalls {
				name := strings.TrimSpace(tc.Function.Name)
				if name == "" {
					name = "tool"
				}
				st := p.states[tc.ID]
				msgs = append(msgs, model.Message{
					Role: model.RoleAssistant, Kind: model.KindToolCall,
					Text: toolCallText(name, rawText(tc.Function.Arguments), st.ParsedArgs),
				})
				if tc.ID != "" && !answered[tc.ID] {
					if out := stateOutput(st); out != "" {
						msgs = append(msgs, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, Text: clip(out, model.MaxToolOutput)})
					}
				}
			}
		case "tool":
			text, ok := contentText(m.Content)
			if !ok {
				skipped++
				continue
			}
			if strings.TrimSpace(text) != "" {
				msgs = append(msgs, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, Text: clip(text, model.MaxToolOutput)})
			}
		case "thinking", "system":
			// never indexed or shown
		default:
			skipped++
		}
	}
	return msgs, skipped, true
}

// contentText flattens MessageContent (a string or a list of parts). Image
// parts become "[image]". ok is false for any other JSON shape.
func contentText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	case '[':
		var parts []json.RawMessage
		if json.Unmarshal(raw, &parts) != nil {
			return "", false
		}
		var out []string
		for _, pr := range parts {
			var part struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(pr, &part) != nil {
				continue
			}
			switch part.Type {
			case "imageUrl", "image_url", "image":
				out = append(out, "[image]")
			default:
				if part.Text != "" {
					out = append(out, part.Text)
				}
			}
		}
		return strings.Join(out, "\n"), true
	}
	return "", false
}

// stateOutput joins the output context items of a tool call state.
func stateOutput(st toolCallState) string {
	var parts []string
	for _, o := range st.Output {
		if strings.TrimSpace(o.Content) != "" {
			parts = append(parts, o.Content)
		}
	}
	return strings.Join(parts, "\n")
}

// argKeys are the arguments that best describe a tool call, in order
// (Continue's built-in tools: run_terminal_command, read_file, ls, …).
var argKeys = []string{"command", "filepath", "filePath", "file_path", "path", "dirPath", "directory_path", "pattern", "query", "url", "name"}

// toolCallText is "name: <command, file path, pattern, … or the first 200
// runes of the arguments>", or just the name when there are no arguments.
// parsed (toolCallStates[].parsedArgs) wins over args, which may be cut short.
func toolCallText(name, args string, parsed map[string]json.RawMessage) string {
	if parsed == nil {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(args), &m) == nil {
			parsed = m
		}
	}
	for _, k := range argKeys {
		var s string
		if v, ok := parsed[k]; ok && json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
			return name + ": " + s
		}
	}
	summary := strings.TrimSpace(args)
	var buf bytes.Buffer
	if json.Compact(&buf, []byte(summary)) == nil {
		summary = buf.String()
	}
	if summary == "{}" || summary == "null" || summary == "" {
		summary = ""
		if len(parsed) > 0 {
			if b, err := json.Marshal(parsed); err == nil {
				summary = string(b)
			}
		}
	}
	if summary == "" {
		return name
	}
	return name + ": " + cutRunes(summary, 200)
}

// promptTitle is a prompt with whitespace collapsed, cut to 100 runes.
func promptTitle(s string) string {
	return cutRunes(strings.Join(strings.Fields(s), " "), 100)
}

// cutRunes keeps the first n runes of s.
func cutRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// clip keeps at most max bytes of s without splitting a UTF-8 sequence.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// rawText returns a JSON string's value, a number's literal, or "".
func rawText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	case 'n': // null
		return ""
	case '{', '[':
		return string(raw) // arguments sent as an object
	}
	return string(raw) // a number (or true/false)
}
