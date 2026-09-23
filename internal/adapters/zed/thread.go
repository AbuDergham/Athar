package zed

import (
	"bytes"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// Thread JSON, three generations (Zed upgrades old rows only when it rewrites them):
//
//   - version "0.3.0" (current agent): title, updated_at, messages[] enum-tagged:
//     {"User":{id, content[]}}, {"Agent":{content[], tool_results{<id>: result}}}
//     or the bare string "Resume". User content items: {"Text": s},
//     {"Mention":{uri:{<Kind>:{abs_path|name|url…}}, content}} (content is the
//     attached body, never kept; the mention shows as "@name"), {"Image":{…}}
//     (shown as "[image]"). Agent content items: {"Text": s}, {"Thinking":{…}}
//     and {"RedactedThinking": …} (dropped), {"ToolUse":{id, name, raw_input,
//     input, is_input_complete}}. subagent_context set = a subagent thread.
//   - version "0.2.0" (the earlier agent panel): summary, updated_at, messages[]
//     {id, role, segments[{type:text|thinking|redacted_thinking, text}],
//     tool_uses[{id, name, input}], tool_results[{tool_use_id, is_error, content}],
//     context (attached files, not read), is_hidden}.
//   - no version (oldest): like 0.2.0 but a message's text is in "text".
//
// Both generations share initial_project_snapshot{worktree_snapshots[
// {worktree_path}], timestamp}. A tool result's content is a string, {"Text": s}
// or {"Image": …}. Unknown variants are ignored.

// thread is what Athar keeps of one thread's JSON.
type thread struct {
	title, summary string
	updated        time.Time
	snapshotAt     time.Time
	worktree       string
	subagent       bool
	messages       []model.Message
}

type rawThread struct {
	Title    json.RawMessage   `json:"title"`
	Summary  json.RawMessage   `json:"summary"`
	Updated  json.RawMessage   `json:"updated_at"`
	Messages []json.RawMessage `json:"messages"`
	Snapshot *struct {
		Worktrees []struct {
			Path json.RawMessage `json:"worktree_path"`
		} `json:"worktree_snapshots"`
		Timestamp json.RawMessage `json:"timestamp"`
	} `json:"initial_project_snapshot"`
	Subagent json.RawMessage `json:"subagent_context"`
}

// parseThread decodes a thread's JSON. Only invalid JSON is an error.
func parseThread(data []byte) (thread, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var raw rawThread
	if err := json.Unmarshal(data, &raw); err != nil {
		return thread{}, err
	}
	t := thread{
		title:    rawStr(raw.Title),
		summary:  rawStr(raw.Summary),
		updated:  parseTime(rawStr(raw.Updated)),
		subagent: !isNull(raw.Subagent),
	}
	if s := raw.Snapshot; s != nil {
		t.snapshotAt = parseTime(rawStr(s.Timestamp))
		for _, w := range s.Worktrees {
			if p := strings.TrimSpace(rawStr(w.Path)); p != "" {
				t.worktree = p
				break
			}
		}
	}
	var b builder
	for _, m := range raw.Messages {
		b.message(m)
	}
	t.messages = b.msgs
	return t, nil
}

// builder collects the messages of a thread in order.
type builder struct {
	msgs []model.Message
}

func (b *builder) add(role model.Role, kind model.Kind, text string) {
	if kind == model.KindToolOutput {
		text = clip(text, model.MaxToolOutput)
	} else {
		text = strings.TrimSpace(text)
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	b.msgs = append(b.msgs, model.Message{Role: role, Kind: kind, Text: text})
}

// message converts one element of messages[].
func (b *builder) message(raw json.RawMessage) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return // "Resume" and other unit variants
	}
	if _, legacy := obj["role"]; legacy {
		b.legacy(raw)
		return
	}
	if u, ok := obj["User"]; ok {
		b.user(u)
	}
	if a, ok := obj["Agent"]; ok {
		b.agent(a)
	}
}

// user converts {"User":{id, content[]}}.
func (b *builder) user(raw json.RawMessage) {
	var u struct {
		Content []json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	var text joiner
	for _, c := range u.Content {
		kind, val := variant(c)
		switch kind {
		case "Text":
			text.text(rawStr(val))
		case "Mention":
			var m struct {
				URI json.RawMessage `json:"uri"`
			}
			if json.Unmarshal(val, &m) == nil {
				text.marker("@" + mentionLabel(m.URI))
			}
		case "Image":
			text.marker("[image]")
		}
	}
	b.add(model.RoleUser, model.KindText, text.String())
}

// toolUse is a tool call in either generation.
type toolUse struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	RawInput json.RawMessage `json:"raw_input"`
	Input    json.RawMessage `json:"input"`
}

// toolResult is a tool result in either generation.
type toolResult struct {
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// agent converts {"Agent":{content[], tool_results{}}}.
func (b *builder) agent(raw json.RawMessage) {
	var a struct {
		Content []json.RawMessage     `json:"content"`
		Results map[string]toolResult `json:"tool_results"`
	}
	if json.Unmarshal(raw, &a) != nil {
		// A malformed tool_results map must not lose the text.
		var only struct {
			Content []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &only) != nil {
			return
		}
		a.Content = only.Content
	}
	used := map[string]bool{}
	var text strings.Builder
	flush := func() {
		b.add(model.RoleAssistant, model.KindText, text.String())
		text.Reset()
	}
	for _, c := range a.Content {
		kind, val := variant(c)
		switch kind {
		case "Text":
			text.WriteString(rawStr(val))
		case "ToolUse":
			var tu toolUse
			if json.Unmarshal(val, &tu) != nil {
				continue
			}
			flush()
			b.add(model.RoleAssistant, model.KindToolCall, toolCallText(tu))
			if r, ok := a.Results[tu.ID]; ok && tu.ID != "" {
				used[tu.ID] = true
				b.add(model.RoleTool, model.KindToolOutput, resultText(r.Content))
			}
		}
	}
	flush()
	ids := make([]string, 0, len(a.Results))
	for id := range a.Results {
		if !used[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		b.add(model.RoleTool, model.KindToolOutput, resultText(a.Results[id].Content))
	}
}

// legacy converts a 0.2.0 or unversioned message.
func (b *builder) legacy(raw json.RawMessage) {
	var m struct {
		Role     string `json:"role"`
		Hidden   bool   `json:"is_hidden"`
		Segments []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"segments"`
		Text    json.RawMessage `json:"text"`
		Uses    []toolUse       `json:"tool_uses"`
		Results []toolResult    `json:"tool_results"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Hidden {
		return
	}
	var role model.Role
	switch strings.ToLower(m.Role) {
	case "user":
		role = model.RoleUser
	case "assistant":
		role = model.RoleAssistant
	default:
		return // system prompts and unknown roles
	}
	var text strings.Builder
	if m.Segments != nil {
		for _, s := range m.Segments {
			if s.Type == "text" {
				text.WriteString(s.Text)
			}
		}
	} else {
		text.WriteString(rawStr(m.Text))
	}
	b.add(role, model.KindText, text.String())
	used := make([]bool, len(m.Results))
	for _, tu := range m.Uses {
		b.add(model.RoleAssistant, model.KindToolCall, toolCallText(tu))
		for i, r := range m.Results {
			if !used[i] && tu.ID != "" && r.ToolUseID == tu.ID {
				used[i] = true
				b.add(model.RoleTool, model.KindToolOutput, resultText(r.Content))
				break
			}
		}
	}
	for i, r := range m.Results {
		if !used[i] {
			b.add(model.RoleTool, model.KindToolOutput, resultText(r.Content))
		}
	}
}

// argKeys are the tool arguments that best describe a call (Zed's built-in
// tools: terminal, read_file, edit_file, grep, find_path, fetch, web_search …).
var argKeys = []string{"command", "path", "abs_path", "path_or_url", "url", "query", "regex", "glob", "pattern", "source_path", "file_path", "name"}

// toolCallText is "name: <command, path, pattern, … or the first 200 runes of
// the arguments>", or the bare name when there are no arguments. input wins
// over raw_input, which may be cut short while the call streams in.
func toolCallText(tu toolUse) string {
	name := strings.TrimSpace(tu.Name)
	if name == "" {
		name = "tool"
	}
	if s := argSummary(tu.Input); s != "" {
		return name + ": " + s
	}
	rawIn := strings.TrimSpace(rawStr(tu.RawInput))
	if s := argSummary(json.RawMessage(rawIn)); s != "" {
		return name + ": " + s
	}
	if rawIn == "" || rawIn == "{}" || rawIn == "null" {
		return name
	}
	return name + ": " + cutRunes(collapse(rawIn), 200)
}

// argSummary describes a JSON object of arguments; "" when it is empty or not an object.
func argSummary(raw json.RawMessage) string {
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil || len(args) == 0 {
		return ""
	}
	for _, k := range argKeys {
		var s string
		if v, ok := args[k]; ok && json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return ""
	}
	return cutRunes(buf.String(), 200)
}

// resultText is a tool result's text: a string, {"Text": s}, {"text": s} or
// "[image]" for {"Image": …}.
func resultText(raw json.RawMessage) string {
	if s, ok := strOK(raw); ok {
		return s
	}
	kind, val := variant(raw)
	switch kind {
	case "Text":
		return rawStr(val)
	case "Image":
		return "[image]"
	}
	var o struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Text != "" {
		return o.Text
	}
	return ""
}

// mentionLabel names a mention: its name, else the file or folder name of its
// path, else its URL, else its kind.
func mentionLabel(raw json.RawMessage) string {
	if s, ok := strOK(raw); ok { // a URI string
		return baseName(strings.TrimRight(s, "/\\"))
	}
	kind, val := variant(raw)
	var f map[string]json.RawMessage
	_ = json.Unmarshal(val, &f)
	if n := collapse(rawStr(f["name"])); n != "" {
		return n
	}
	for _, k := range []string{"abs_path", "path"} {
		if p := strings.TrimRight(strings.TrimSpace(rawStr(f[k])), "/\\"); p != "" {
			return baseName(p)
		}
	}
	if u := strings.TrimSpace(rawStr(f["url"])); u != "" {
		return u
	}
	if kind != "" {
		return strings.ToLower(kind)
	}
	return "mention"
}

// baseName is the last element of a Windows or POSIX path.
func baseName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return path.Base(p)
}

// joiner concatenates a user message's text chunks and markers, adding a space
// around a marker only where the text has none.
type joiner struct {
	b           strings.Builder
	afterMarker bool
}

func (j *joiner) text(s string) {
	if j.afterMarker && s != "" {
		r, _ := utf8.DecodeRuneInString(s)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			j.b.WriteByte(' ')
		}
	}
	j.b.WriteString(s)
	j.afterMarker = false
}

func (j *joiner) marker(m string) {
	if j.b.Len() > 0 {
		r, _ := utf8.DecodeLastRuneInString(j.b.String())
		if !unicode.IsSpace(r) {
			j.b.WriteByte(' ')
		}
	}
	j.b.WriteString(m)
	j.afterMarker = true
}

func (j *joiner) String() string { return j.b.String() }

// variant splits an externally tagged enum value {"Kind": value} into its
// kind and value; ("", nil) for anything else.
func variant(raw json.RawMessage) (string, json.RawMessage) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || len(obj) != 1 {
		return "", nil
	}
	for k, v := range obj {
		return k, v
	}
	return "", nil
}

// rawStr returns a JSON string's value, or "".
func rawStr(raw json.RawMessage) string {
	s, _ := strOK(raw)
	return s
}

func strOK(raw json.RawMessage) (string, bool) {
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

func isNull(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || string(raw) == "null"
}

// collapse trims s and folds every run of white space into one space.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

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
