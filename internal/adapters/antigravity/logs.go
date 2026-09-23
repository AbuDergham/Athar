package antigravity

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// Step types and sources of the step logs.
const (
	stepUserInput = "USER_INPUT"
	stepPlanner   = "PLANNER_RESPONSE"
	sourceSystem  = "SYSTEM"
	toolNotify    = "notify_user" // the agent's message to the user, shown as text
)

// logRecord is one line of a step log.
type logRecord struct {
	StepIndex *int64          `json:"step_index"`
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	CreatedAt string          `json:"created_at"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool_calls"`
}

// toolArgKeys are the tool arguments shown after the tool name, in order.
var toolArgKeys = []string{
	"CommandLine", "AbsolutePath", "TargetFile", "DirectoryPath", "SearchPath",
	"SearchDirectory", "Query", "query", "Pattern", "Url", "url", "TaskName",
}

// logMessages reads the first step log (in b.logs order) that yields messages.
// skipped counts the unparsable lines of that log.
func logMessages(logs []string) ([]model.Message, int) {
	for _, p := range logs {
		msgs, skipped := readLog(p)
		if len(msgs) > 0 {
			return msgs, skipped
		}
	}
	return nil, 0
}

// readLog parses one step log. A later record with the same step_index
// replaces the messages of the earlier one in place.
func readLog(path string) ([]model.Message, int) {
	var groups [][]model.Message
	byStep := map[int64]int{}
	skipped := 0
	err := fsread.Lines(path, func(line []byte) error {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			return nil
		}
		var r logRecord
		if err := json.Unmarshal(line, &r); err != nil {
			skipped++
			return nil
		}
		g := recordMessages(r)
		if r.StepIndex != nil {
			if i, ok := byStep[*r.StepIndex]; ok {
				groups[i] = g
				return nil
			}
			byStep[*r.StepIndex] = len(groups)
		}
		groups = append(groups, g)
		return nil
	})
	if err != nil {
		skipped++
	}
	var out []model.Message
	for _, g := range groups {
		out = append(out, g...)
	}
	return out, skipped
}

// recordMessages maps one step to messages (see the package comment).
func recordMessages(r logRecord) []model.Message {
	if strings.EqualFold(r.Source, sourceSystem) {
		return nil
	}
	at := parseTime(r.CreatedAt)
	content := rawString(r.Content)
	var out []model.Message
	add := func(role model.Role, kind model.Kind, text string) {
		if strings.TrimSpace(text) != "" {
			out = append(out, model.Message{Role: role, Kind: kind, At: at, Text: text})
		}
	}
	switch strings.ToUpper(r.Type) {
	case stepUserInput:
		add(model.RoleUser, model.KindText, userText(content))
	case stepPlanner:
		add(model.RoleAssistant, model.KindText, strings.TrimSpace(content))
		for _, tc := range r.ToolCalls {
			if tc.Name == toolNotify {
				if msg := stringArg(tc.Args, "Message"); msg != "" {
					add(model.RoleAssistant, model.KindText, msg)
					continue
				}
			}
			add(model.RoleAssistant, model.KindToolCall, toolCallText(tc.Name, tc.Args))
		}
	default:
		add(model.RoleTool, model.KindToolOutput, truncate(strings.TrimSpace(content), model.MaxToolOutput))
	}
	return out
}

// userText returns the text of a USER_INPUT step: the <USER_REQUEST> blocks
// when present, otherwise the content without the editor-state blocks
// (<ADDITIONAL_METADATA>, <USER_SETTINGS_CHANGE>).
func userText(s string) string {
	const open, closeTag = "<USER_REQUEST>", "</USER_REQUEST>"
	if strings.Contains(s, open) {
		var parts []string
		rest := s
		for {
			i := strings.Index(rest, open)
			if i < 0 {
				break
			}
			rest = rest[i+len(open):]
			end := len(rest)
			if j := strings.Index(rest, closeTag); j >= 0 {
				end = j
			} else if j := strings.Index(rest, "<ADDITIONAL_METADATA>"); j >= 0 {
				end = j
			}
			if p := strings.TrimSpace(rest[:end]); p != "" {
				parts = append(parts, p)
			}
			rest = rest[end:]
		}
		return strings.Join(parts, "\n\n")
	}
	for _, tag := range []string{"ADDITIONAL_METADATA", "USER_SETTINGS_CHANGE"} {
		s = stripBlock(s, tag)
	}
	return strings.TrimSpace(s)
}

// stripBlock removes every <tag>…</tag> block (an unclosed block runs to the end).
func stripBlock(s, tag string) string {
	open, closeTag := "<"+tag+">", "</"+tag+">"
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], closeTag)
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len(closeTag):]
	}
}

// toolCallText is "name: <command | path | query | first 200 runes of the
// arguments JSON>", at most model.MaxToolOutput bytes.
func toolCallText(name string, args json.RawMessage) string {
	if name == "" {
		name = "tool"
	}
	arg := ""
	for _, k := range toolArgKeys {
		if v := stringArg(args, k); v != "" {
			arg = v
			break
		}
	}
	if arg == "" && len(args) > 0 {
		var buf bytes.Buffer
		if json.Compact(&buf, args) == nil {
			if js := buf.String(); js != "{}" && js != "null" && js != `""` {
				arg = firstRunes(js, 200)
			}
		}
	}
	if arg == "" {
		return name
	}
	return truncate(name+": "+arg, model.MaxToolOutput)
}

// stringArg returns the trimmed string value of key in a JSON object, or "".
func stringArg(args json.RawMessage, key string) string {
	var fields map[string]json.RawMessage
	if len(args) == 0 || json.Unmarshal(args, &fields) != nil {
		return ""
	}
	return strings.TrimSpace(rawString(fields[key]))
}

// rawString decodes a JSON string; anything else gives "".
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '"' {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
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
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}
