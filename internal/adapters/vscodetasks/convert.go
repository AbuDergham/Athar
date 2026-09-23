package vscodetasks

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

// apiMessage is one entry of api_conversation_history.json (Anthropic shape).
type apiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // a string or blocks
	Ts      json.RawMessage `json:"ts"`      // epoch ms; absent in old builds
}

// block is one content block. Unknown types (image, thinking, reasoning,
// redacted_thinking, document) are ignored.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// converter turns api_conversation_history entries into model messages.
type converter struct {
	msgs      []model.Message
	skipped   int
	cwd       string            // from the first environment details that name one
	toolNames map[string]string // tool_use id → tool name
}

func (c *converter) add(role model.Role, kind model.Kind, at time.Time, text string) {
	c.msgs = append(c.msgs, model.Message{Role: role, Kind: kind, At: at, Text: text})
}

// message converts one entry; undecodable entries are counted in skipped.
func (c *converter) message(raw json.RawMessage) {
	var m apiMessage
	if !decode(raw, &m) || m.Role == "" {
		c.skipped++
		return
	}
	blocks, ok := contentBlocks(m.Content)
	if !ok {
		c.skipped++
		return
	}
	at := msTime(m.Ts)
	switch m.Role {
	case "user":
		c.user(blocks, at)
	case "assistant":
		c.assistant(blocks, at)
	}
}

// contentBlocks decodes content: a plain string becomes one text block.
// Blocks that do not decode are dropped.
func contentBlocks(raw json.RawMessage) ([]block, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, false
		}
		return []block{{Type: "text", Text: s}}, true
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil, false
		}
		out := make([]block, 0, len(items))
		for _, it := range items {
			var b block
			if decode(it, &b) {
				out = append(out, b)
			}
		}
		return out, true
	}
	return nil, false
}

// conversational tools carry the dialogue: the assistant's words are in their
// input, and their "result" is the user's reply (extracted from its wrappers).
var conversational = map[string]string{ // tool → input field holding the assistant's text
	"attempt_completion":    "result",
	"ask_followup_question": "question",
	"plan_mode_respond":     "response",
	"act_mode_respond":      "response",
}

// user converts a user entry: prompts, tool results and injected context.
func (c *converter) user(blocks []block, at time.Time) {
	var prompts []string
	var out strings.Builder // text of the current "[tool] Result:" section
	tool, inResult := "", false
	flush := func() {
		if inResult {
			c.toolOutput(tool, out.String(), at)
		}
		out.Reset()
		tool, inResult = "", false
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text := c.stripEnvironment(b.Text)
			if strings.TrimSpace(text) == "" {
				continue
			}
			if name, rest, ok := resultHeader(text); ok {
				flush()
				tool, inResult, text = name, true, rest
			} else if isNotice(text) {
				flush() // a system notice ends a result section
			}
			sections, rest := userSections(text)
			prompts = append(prompts, sections...)
			switch {
			case inResult:
				if out.Len() > 0 {
					out.WriteByte('\n')
				}
				out.WriteString(strings.TrimSpace(rest))
			case len(sections) == 0 && !isNotice(rest):
				if p := strings.TrimSpace(removeSections(rest, "file_content")); p != "" {
					prompts = append(prompts, p)
				}
			}
		case "tool_result":
			flush()
			sections, rest := userSections(resultText(b.Content))
			prompts = append(prompts, sections...)
			c.toolOutput(c.toolNames[b.ToolUseID], rest, at)
		}
	}
	flush()
	for _, p := range prompts {
		c.add(model.RoleUser, model.KindText, at, p)
	}
}

// toolOutput keeps the first model.MaxToolOutput bytes of a tool's output.
// The results of conversational tools are only boilerplate around the user's
// reply, which userSections already took out.
func (c *converter) toolOutput(tool, text string, at time.Time) {
	if _, ok := conversational[tool]; ok {
		return
	}
	if text = strings.TrimSpace(text); text != "" {
		c.add(model.RoleTool, model.KindToolOutput, at, truncate(text, model.MaxToolOutput))
	}
}

// resultText joins the text parts of a tool_result's content.
func resultText(raw json.RawMessage) string {
	blocks, ok := contentBlocks(raw)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(b.Text)
		if sb.Len() >= 4*model.MaxToolOutput {
			break // enough to find the user's reply and keep the output head
		}
	}
	return sb.String()
}

// assistant converts an assistant entry: text (with Cline's XML tool calls)
// and native tool_use blocks. Thinking and reasoning blocks are dropped.
func (c *converter) assistant(blocks []block, at time.Time) {
	for _, b := range blocks {
		switch b.Type {
		case "text":
			for _, seg := range parseAssistant(b.Text) {
				if seg.tool == "" {
					if s := strings.TrimSpace(seg.text); s != "" {
						c.add(model.RoleAssistant, model.KindText, at, s)
					}
					continue
				}
				c.xmlTool(seg.tool, seg.body, at)
			}
		case "tool_use":
			if b.ID != "" {
				if c.toolNames == nil {
					c.toolNames = map[string]string{}
				}
				c.toolNames[b.ID] = b.Name
			}
			c.nativeTool(b.Name, b.Input, at)
		}
	}
}

// xmlTool handles one <tool>...</tool> block of Cline's XML tool protocol.
func (c *converter) xmlTool(name, body string, at time.Time) {
	params := toolParams(body)
	if field, ok := conversational[name]; ok {
		if s := strings.TrimSpace(params[field]); s != "" {
			c.add(model.RoleAssistant, model.KindText, at, s)
		}
		return
	}
	var args []string
	for _, k := range argKeys {
		if v := strings.TrimSpace(params[k]); v != "" {
			args = append(args, v)
		}
	}
	if len(args) == 0 {
		for _, m := range nestedPathRe.FindAllStringSubmatch(body, 8) {
			if p := strings.TrimSpace(m[1]); p != "" {
				args = append(args, p)
			}
		}
	}
	c.add(model.RoleAssistant, model.KindToolCall, at, callText(name, args, ""))
}

// nativeTool handles a tool_use block (native tool protocol).
func (c *converter) nativeTool(name string, input json.RawMessage, at time.Time) {
	var fields map[string]json.RawMessage
	decode(input, &fields)
	if field, ok := conversational[name]; ok {
		if s := strings.TrimSpace(rawString(fields[field])); s != "" {
			c.add(model.RoleAssistant, model.KindText, at, s)
		}
		return
	}
	var args []string
	for _, k := range argKeys {
		if v := strings.TrimSpace(rawString(fields[k])); v != "" {
			args = append(args, v)
		}
	}
	if len(args) == 0 {
		var files []struct {
			Path string `json:"path"`
		}
		if decode(fields["files"], &files) {
			for _, f := range files {
				if p := strings.TrimSpace(f.Path); p != "" {
					args = append(args, p)
				}
			}
		}
	}
	fallback := ""
	if len(args) == 0 && len(input) > 0 {
		var buf bytes.Buffer
		if json.Compact(&buf, input) == nil {
			if js := buf.String(); js != "{}" && js != "null" && js != `""` {
				fallback = firstRunes(js, 200)
			}
		}
	}
	c.add(model.RoleAssistant, model.KindToolCall, at, callText(name, args, fallback))
}

// argKeys are the tool parameters shown in a tool call, in order of preference.
var argKeys = []string{"command", "path", "file_path", "regex", "url", "query", "server_name", "tool_name", "uri", "action"}

// maxArgs is how many parameters a tool call line shows.
const maxArgs = 2

// callText is "name: arg arg" (or "name: fallback"), at most MaxToolOutput bytes.
func callText(name string, args []string, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	if len(args) > maxArgs {
		args = args[:maxArgs]
	}
	arg := strings.Join(args, " ")
	if arg == "" {
		arg = fallback
	}
	if arg == "" {
		return name
	}
	return truncate(name+": "+arg, model.MaxToolOutput)
}

var (
	// resultHeaderRe matches Cline's "[execute_command for 'npm test'] Result:".
	resultHeaderRe = regexp.MustCompile(`^\[([a-z][a-z0-9_]*)(?: for (?s:.*?))?\] Result:`)
	// noticeRe matches injected notices such as "[ERROR] ...", "[TASK RESUMPTION] ..."
	// and Cline's checklist reminders ("# task_progress RECOMMENDED", "# TODO LIST ...").
	noticeRe = regexp.MustCompile(`^(?:\[[A-Z][A-Z0-9 _-]*\]|# (?:task_progress|TODO LIST)\b)`)
	// cwdRe finds the working folder in environment details.
	cwdRe = regexp.MustCompile(`# Current (?:Working|Workspace) Directory \(([^)\r\n]+)\)`)
	// nestedPathRe finds <path> elements nested in XML tool arguments.
	nestedPathRe = regexp.MustCompile(`<path>([^<]{1,1000})</path>`)
)

// resultHeader recognizes a tool result header and returns the tool name and
// the text after the header.
func resultHeader(text string) (name, rest string, ok bool) {
	t := strings.TrimLeft(text, " \t\r\n")
	head := t
	if len(head) > 4096 {
		head = head[:4096]
	}
	m := resultHeaderRe.FindStringSubmatchIndex(head)
	if m == nil {
		return "", text, false
	}
	return head[m[2]:m[3]], t[m[1]:], true
}

// isNotice reports whether text starts with an injected notice.
func isNotice(text string) bool {
	return noticeRe.MatchString(strings.TrimLeft(text, " \t\r\n"))
}

// stripEnvironment removes <environment_details> sections, remembering the
// first working folder they name.
func (c *converter) stripEnvironment(text string) string {
	if !strings.Contains(text, "<environment_details>") {
		return text
	}
	if c.cwd == "" {
		if m := cwdRe.FindStringSubmatch(text); m != nil {
			c.cwd = nativePath(strings.TrimSpace(m[1]))
		}
	}
	return removeSections(text, "environment_details")
}

// nativePath turns "c:/dir/sub" (Cline writes POSIX separators) into "c:\dir\sub".
func nativePath(p string) string {
	if len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\') {
		return strings.ReplaceAll(p, "/", `\`)
	}
	return p
}

// userTags wrap what the user typed.
var userTags = []string{"task", "feedback", "answer", "user_message"}

// userSections returns the trimmed inner text of every <task>, <feedback>,
// <answer> and <user_message> section, in order, and the text around them.
func userSections(text string) (sections []string, rest string) {
	var sb strings.Builder
	i := 0
	for {
		start, tag := -1, ""
		for _, t := range userTags {
			if j := strings.Index(text[i:], "<"+t+">"); j >= 0 && (start < 0 || i+j < start) {
				start, tag = i+j, t
			}
		}
		if start < 0 {
			sb.WriteString(text[i:])
			break
		}
		sb.WriteString(text[i:start])
		inner := start + len(tag) + 2
		end := strings.Index(text[inner:], "</"+tag+">")
		next := len(text)
		if end < 0 {
			end = len(text) - inner
		} else {
			next = inner + end + len(tag) + 3
		}
		if s := strings.TrimSpace(removeSections(text[inner:inner+end], "environment_details")); s != "" {
			sections = append(sections, s)
		}
		i = next
	}
	return sections, sb.String()
}

// removeSections removes every <tag ...>...</tag> section (the opening tag may
// carry attributes, as in <file_content path="...">).
func removeSections(text, tag string) string {
	open, closeTag := "<"+tag, "</"+tag+">"
	var sb strings.Builder
	i := 0
	for {
		j := strings.Index(text[i:], open)
		if j < 0 {
			sb.WriteString(text[i:])
			break
		}
		start := i + j
		after := start + len(open)
		if after < len(text) && text[after] != '>' && text[after] != ' ' {
			sb.WriteString(text[i:after]) // a longer tag name, e.g. <taskx>
			i = after
			continue
		}
		sb.WriteString(text[i:start])
		end := strings.Index(text[after:], closeTag)
		if end < 0 {
			break // an unterminated section runs to the end
		}
		i = after + end + len(closeTag)
	}
	return sb.String()
}
