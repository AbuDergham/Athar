package lmstudio

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

const (
	// titleRunes is the length of a title taken from the first prompt.
	titleRunes = 100
	// toolSummaryRunes is the length of a tool call's argument summary when no
	// command or path argument is found.
	toolSummaryRunes = 200
)

// placeholderNames are the names LM Studio gives chats it has not named yet.
var placeholderNames = []string{"Untitled", "Untitled Chat", "New Chat", "New Conversation"}

// conversation is the part of a chat file Athar reads. Fields stay raw so that
// one field of an unexpected type does not make the whole file unreadable.
type conversation struct {
	Name          json.RawMessage   `json:"name"`
	CreatedAt     json.RawMessage   `json:"createdAt"`
	UserLast      json.RawMessage   `json:"userLastMessagedAt"`
	AssistantLast json.RawMessage   `json:"assistantLastMessagedAt"`
	Messages      []json.RawMessage `json:"messages"`
}

// entry is one element of messages[]: the alternative versions of a message.
// Role and Content are set instead of Versions in simple {role, content} messages.
type entry struct {
	Versions          []json.RawMessage `json:"versions"`
	CurrentlySelected json.RawMessage   `json:"currentlySelected"`
	Role              string            `json:"role"`
}

// version is one version of a message.
type version struct {
	Type    string            `json:"type"` // singleStep | multiStep
	Role    string            `json:"role"`
	Content json.RawMessage   `json:"content"` // array of parts, or a string
	Steps   []json.RawMessage `json:"steps"`
}

// step is one step of a multiStep (assistant) version.
type step struct {
	Type           string          `json:"type"` // contentBlock | debugInfoBlock | ...
	StepIdentifier string          `json:"stepIdentifier"`
	Content        json.RawMessage `json:"content"`
	Style          *struct {
		Type string `json:"type"` // "thinking" for reasoning blocks
	} `json:"style"`
}

// part is one content part.
type part struct {
	Type            string          `json:"type"` // text | file | toolCallRequest | toolCallResult
	Text            string          `json:"text"`
	IsStructural    bool            `json:"isStructural"`
	FileIdentifier  string          `json:"fileIdentifier"`
	Name            string          `json:"name"`
	FileType        string          `json:"fileType"`
	ToolCallRequest *toolCall       `json:"toolCallRequest"`
	Content         json.RawMessage `json:"content"` // toolCallResult output
}

type toolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// parse turns a chat file into a session. It returns nil when the chat has no
// messages. mtime is used only when the file carries no dates at all.
func parse(data []byte, id string, mtime time.Time) (*model.Session, int, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	var c conversation
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, 0, err
	}
	p := &parser{}
	for _, raw := range c.Messages {
		p.entry(raw)
	}
	if len(p.msgs) == 0 {
		return nil, p.skipped, nil
	}

	s := &model.Session{
		Agent:       "lmstudio",
		NativeID:    id,
		Coverage:    model.CoverageFull,
		Origin:      model.OriginLive,
		TitleSource: model.TitleNone,
		Messages:    p.msgs,
	}
	if name := chatName(c.Name); name != "" {
		s.Title, s.TitleSource = name, model.TitleAgent
	} else if p.firstPrompt != "" {
		s.Title, s.TitleSource = truncateRunes(collapse(p.firstPrompt), titleRunes), model.TitlePrompt
	}

	created := msTime(c.CreatedAt)
	switch {
	case !created.IsZero():
		s.StartedAt = created
	case !idTime(id).IsZero():
		s.StartedAt = idTime(id)
	default:
		for _, m := range p.msgs {
			if !m.At.IsZero() && (s.StartedAt.IsZero() || m.At.Before(s.StartedAt)) {
				s.StartedAt = m.At
			}
		}
	}
	for _, t := range []time.Time{created, msTime(c.UserLast), msTime(c.AssistantLast)} {
		if t.After(s.UpdatedAt) {
			s.UpdatedAt = t
		}
	}
	for _, m := range p.msgs {
		if m.At.After(s.UpdatedAt) {
			s.UpdatedAt = m.At
		}
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = mtime.UTC()
	}
	if s.UpdatedAt.Before(s.StartedAt) {
		s.UpdatedAt = s.StartedAt
	}
	return s, p.skipped, nil
}

// parser accumulates the messages of one chat.
type parser struct {
	msgs        []model.Message
	skipped     int
	firstPrompt string // text of the first user message (without file markers)
}

// entry reads one element of messages[] and its selected version.
func (p *parser) entry(raw json.RawMessage) {
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		p.skipped++
		return
	}
	vraw := raw
	if len(e.Versions) > 0 {
		vraw = e.Versions[selected(e.CurrentlySelected, len(e.Versions))]
	} else if e.Role == "" {
		p.skipped++
		return
	}
	var v version
	if err := json.Unmarshal(vraw, &v); err != nil {
		p.skipped++
		return
	}
	role, ok := roleOf(v.Role)
	if !ok {
		p.skipped++
		return
	}
	if len(v.Steps) > 0 {
		for _, sraw := range v.Steps {
			p.step(role, sraw)
		}
		return
	}
	b := p.newBlock(role, time.Time{}, "\n")
	p.parts(b, v.Content)
	p.flush(b)
}

// step reads one step of a multiStep version. Only content blocks carry
// conversation text; reasoning ("thinking") blocks are dropped.
func (p *parser) step(role model.Role, raw json.RawMessage) {
	var st step
	if err := json.Unmarshal(raw, &st); err != nil {
		p.skipped++
		return
	}
	if st.Type != "contentBlock" || (st.Style != nil && st.Style.Type == "thinking") {
		return
	}
	b := p.newBlock(role, stepTime(st.StepIdentifier), "")
	p.parts(b, st.Content)
	p.flush(b)
}

// block collects the text of one message being built.
type block struct {
	role  model.Role
	at    time.Time
	sep   string   // "\n" between separate parts (singleStep), "" between streamed fragments
	segs  []string // text and file markers, in order
	texts []string // text only, for the title
}

func (p *parser) newBlock(role model.Role, at time.Time, sep string) *block {
	return &block{role: role, at: at, sep: sep}
}

// parts reads a content value: an array of parts or a plain string.
func (p *parser) parts(b *block, content json.RawMessage) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 || string(content) == "null" {
		return
	}
	if content[0] == '"' {
		var s string
		if err := json.Unmarshal(content, &s); err != nil {
			p.skipped++
			return
		}
		b.segs = append(b.segs, s)
		b.texts = append(b.texts, s)
		return
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(content, &raws); err != nil {
		p.skipped++
		return
	}
	for _, raw := range raws {
		var pt part
		if err := json.Unmarshal(raw, &pt); err != nil {
			p.skipped++
			continue
		}
		switch pt.Type {
		case "text":
			if pt.IsStructural {
				continue
			}
			b.segs = append(b.segs, pt.Text)
			b.texts = append(b.texts, pt.Text)
		case "file":
			b.marker(fileMarker(pt))
		case "toolCallRequest":
			if pt.ToolCallRequest == nil {
				continue
			}
			p.flush(b)
			p.msgs = append(p.msgs, model.Message{
				Role: model.RoleAssistant,
				Kind: model.KindToolCall,
				At:   b.at,
				Text: toolCallText(pt.ToolCallRequest.Name, pt.ToolCallRequest.Arguments),
			})
		case "toolCallResult":
			p.flush(b)
			if out := strings.TrimSpace(resultText(pt.Content)); out != "" {
				p.msgs = append(p.msgs, model.Message{
					Role: model.RoleTool,
					Kind: model.KindToolOutput,
					At:   b.at,
					Text: truncateBytes(out, model.MaxToolOutput),
				})
			}
		}
		// Other part types (reasoning, images generated by tools, ...) are not text.
	}
}

// marker adds an attachment marker as its own line.
func (b *block) marker(m string) {
	if b.sep == "" {
		m = "\n" + m + "\n"
	}
	b.segs = append(b.segs, m)
}

// flush turns the collected text into a message and resets the block.
func (p *parser) flush(b *block) {
	var text string
	if b.sep == "" {
		text = strings.Join(b.segs, "")
	} else {
		keep := b.segs[:0:0]
		for _, s := range b.segs {
			if s = strings.TrimSpace(s); s != "" {
				keep = append(keep, s)
			}
		}
		text = strings.Join(keep, b.sep)
	}
	text = strings.TrimSpace(text)
	if b.role == model.RoleUser && p.firstPrompt == "" {
		p.firstPrompt = strings.TrimSpace(strings.Join(b.texts, " "))
	}
	b.segs, b.texts = nil, nil
	if text == "" {
		return
	}
	m := model.Message{Role: b.role, Kind: model.KindText, At: b.at, Text: text}
	if b.role == model.RoleTool {
		m.Kind, m.Text = model.KindToolOutput, truncateBytes(text, model.MaxToolOutput)
	}
	p.msgs = append(p.msgs, m)
}

// roleOf maps an LM Studio role to a model role.
func roleOf(r string) (model.Role, bool) {
	switch r {
	case "user":
		return model.RoleUser, true
	case "assistant":
		return model.RoleAssistant, true
	case "system":
		return model.RoleSystem, true
	case "tool":
		return model.RoleTool, true
	}
	return "", false
}

// selected returns the index of the selected version, clamped to [0, n-1].
func selected(raw json.RawMessage, n int) int {
	var f float64
	if len(raw) == 0 || json.Unmarshal(raw, &f) != nil || f < 0 {
		return 0
	}
	if int(f) >= n {
		return n - 1
	}
	return int(f)
}

// chatName returns the chat's own name, or "" when it is unset or a placeholder.
func chatName(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	s = collapse(s)
	for _, ph := range placeholderNames {
		if strings.EqualFold(s, ph) {
			return ""
		}
	}
	return s
}

// fileMarker describes an attached file: "[image]" or "[file: name]".
func fileMarker(pt part) string {
	if strings.HasPrefix(strings.ToLower(pt.FileType), "image") {
		return "[image]"
	}
	name := pt.Name
	if name == "" {
		name = pt.FileIdentifier
		// Identifiers look like "<epoch-ms> - <original name>".
		if i := strings.Index(name, " - "); i > 0 && isDigits(name[:i]) {
			name = name[i+3:]
		}
	}
	if name = strings.TrimSpace(name); name == "" {
		return "[file]"
	}
	return "[file: " + name + "]"
}

// toolCallText is "name: <command, path, pattern, query or url argument>", or
// "name: <first 200 characters of the arguments as compact JSON>".
func toolCallText(name string, args json.RawMessage) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	args = bytes.TrimSpace(args)
	if len(args) > 0 && args[0] == '"' { // arguments encoded as a JSON string
		var s string
		if json.Unmarshal(args, &s) == nil {
			args = bytes.TrimSpace([]byte(s))
		}
	}
	summary := ""
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) == nil {
		for _, k := range []string{"command", "cmd", "file_path", "filePath", "path", "pattern", "query", "url"} {
			var s string
			if v, ok := obj[k]; ok && json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
				summary = strings.TrimSpace(s)
				break
			}
		}
	}
	if summary == "" {
		switch string(args) {
		case "", "null", "{}":
		default:
			var buf bytes.Buffer
			if json.Compact(&buf, args) == nil {
				summary = buf.String()
			} else {
				summary = string(args)
			}
			summary = truncateRunes(summary, toolSummaryRunes)
		}
	}
	if summary == "" {
		return name
	}
	return truncateBytes(name+": "+summary, model.MaxToolOutput)
}

// resultText returns a tool result as text: a string as is, text parts joined,
// anything else as compact JSON.
func resultText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var texts []string
		for _, pt := range parts {
			if pt.Text != "" {
				texts = append(texts, pt.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) == nil {
		return buf.String()
	}
	return string(raw)
}

// msTime reads an epoch-ms number (or numeric string); zero when absent or invalid.
func msTime(raw json.RawMessage) time.Time {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return time.Time{}
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return time.Time{}
		}
		f = v
	}
	if f <= 0 || f > 1e15 {
		return time.Time{}
	}
	return time.UnixMilli(int64(f)).UTC()
}

// idTime reads the creation time from a chat id (the file name: epoch ms).
func idTime(id string) time.Time {
	if len(id) != 13 || !isDigits(id) {
		return time.Time{}
	}
	v, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// stepTime reads the time from a step identifier "<epoch-ms>-<random>".
func stepTime(id string) time.Time {
	head, _, _ := strings.Cut(id, "-")
	return idTime(head)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// collapse joins the words of s with single spaces.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncateRunes keeps the first n runes of s (trailing spaces removed).
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n]))
}

// truncateBytes keeps at most n bytes of s without splitting a UTF-8 sequence.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
