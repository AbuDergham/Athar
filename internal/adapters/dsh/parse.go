package dsh

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// knownFormat is the newest dsh session format this parser was written against.
const knownFormat = 3

// rawLine is either the header line or an event line. Numbers are float64 so
// an unexpected float never makes a whole line unreadable.
type rawLine struct {
	Type string          `json:"type"`
	Time float64         `json:"time"`
	Data json.RawMessage `json:"data"`

	// Header-only fields.
	Version         float64 `json:"version"`
	ID              string  `json:"id"`
	CreatedAt       float64 `json:"createdAt"`
	Cwd             string  `json:"cwd"`
	DelegationDepth float64 `json:"delegationDepth"`
	Origin          string  `json:"origin"`
}

type header struct {
	version   int
	id, cwd   string
	createdAt time.Time
	subagent  bool
}

// chatMessage is the message shape of user/message data and inbox entries.
type chatMessage struct {
	Role    string          `json:"role"`
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
	Source  struct {
		Kind string `json:"kind"`
	} `json:"source"`
}

// parser folds the lines of one session log into a model.Session.
type parser struct {
	hdr      *header
	records  int // lines that parsed as JSON
	skipped  int
	warnings []string

	msgs     []model.Message
	inboxIDs []string // parallel to msgs: the id of an inbox-derived message, else ""
	userIDs  map[string]bool
	inboxed  map[string]bool

	title    string
	titleSrc model.TitleSource

	first, last time.Time // activity range (session/end-seed excluded)
}

func newParser() *parser {
	return &parser{userIDs: map[string]bool{}, inboxed: map[string]bool{}}
}

// maxMillis (year 5138) rejects garbage that would overflow into odd dates.
const maxMillis = 1e14

func msTime(v float64) time.Time {
	if v <= 0 || v > maxMillis {
		return time.Time{}
	}
	return time.UnixMilli(int64(v)).UTC()
}

func (p *parser) line(b []byte) {
	var l rawLine
	if err := json.Unmarshal(b, &l); err != nil {
		p.skipped++
		return
	}
	p.records++
	if l.Type == "session" {
		if p.hdr == nil {
			p.header(l)
		}
		return
	}
	at := msTime(l.Time)
	if !at.IsZero() && l.Type != "session/end-seed" {
		if p.first.IsZero() || at.Before(p.first) {
			p.first = at
		}
		if at.After(p.last) {
			p.last = at
		}
	}
	var err error
	switch l.Type {
	case "user/message":
		err = p.userMessage(l.Data, at)
	case "agent/inbox/spliced":
		err = p.inbox(l.Data, at)
	case "assistant/message":
		err = p.assistantMessage(l.Data, at)
	case "tool/call":
		err = p.toolCall(l.Data, at)
	case "tool/result":
		err = p.toolResult(l.Data, at)
	case "session/title":
		err = p.sessionTitle(l.Data)
	}
	if err != nil {
		p.skipped++
	}
}

func (p *parser) header(l rawLine) {
	h := &header{
		version:   int(l.Version),
		id:        strings.TrimSpace(l.ID),
		cwd:       l.Cwd,
		createdAt: msTime(l.CreatedAt),
	}
	// A parentSession alone marks a fork, which is a session of its own
	// (dsh and AgentsView agree); only a delegated run is a subagent.
	h.subagent = l.Origin == "subagent" || l.DelegationDepth > 0
	if h.version > knownFormat {
		p.warnings = append(p.warnings, fmt.Sprintf("session format v%d is newer than v%d; some records may be missing", h.version, knownFormat))
	}
	p.hdr = h
}

func (p *parser) add(role model.Role, kind model.Kind, at time.Time, text, inboxID string) {
	p.msgs = append(p.msgs, model.Message{Role: role, Kind: kind, At: at, Text: text})
	p.inboxIDs = append(p.inboxIDs, inboxID)
}

func (p *parser) userMessage(data json.RawMessage, at time.Time) error {
	var m chatMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m.Source.Kind != "user" {
		return nil // injected context, not the human
	}
	if m.ID != "" {
		p.userIDs[m.ID] = true
	}
	if text := blocksText(m.Content); strings.TrimSpace(text) != "" {
		p.add(model.RoleUser, model.KindText, at, text, "")
	}
	return nil
}

func (p *parser) inbox(data json.RawMessage, at time.Time) error {
	var d struct {
		Inserted []chatMessage `json:"inserted"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	for _, m := range d.Inserted {
		if m.Source.Kind != "user" || (m.Role != "" && m.Role != "user") || m.ID == "" || p.inboxed[m.ID] {
			continue
		}
		p.inboxed[m.ID] = true
		if text := blocksText(m.Content); strings.TrimSpace(text) != "" {
			p.add(model.RoleUser, model.KindText, at, text, m.ID)
		}
	}
	return nil
}

func (p *parser) assistantMessage(data json.RawMessage, at time.Time) error {
	var d struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	if text := blocksText(d.Message.Content); strings.TrimSpace(text) != "" {
		p.add(model.RoleAssistant, model.KindText, at, text, "")
	}
	return nil
}

func (p *parser) toolCall(data json.RawMessage, at time.Time) error {
	var d struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	p.add(model.RoleAssistant, model.KindToolCall, at, toolCallText(d.Name, d.Arguments), "")
	return nil
}

func (p *parser) toolResult(data json.RawMessage, at time.Time) error {
	var d struct {
		Message struct {
			Content []struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Content json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	var parts []string
	for _, b := range d.Message.Content {
		switch {
		case b.Type == "text":
			parts = append(parts, b.Text)
		case len(b.Content) > 0:
			parts = append(parts, blocksText(b.Content))
		}
	}
	text := truncateBytes(strings.Join(parts, "\n"), model.MaxToolOutput)
	if strings.TrimSpace(text) != "" {
		p.add(model.RoleTool, model.KindToolOutput, at, text, "")
	}
	return nil
}

func (p *parser) sessionTitle(data json.RawMessage) error {
	var d struct {
		Title  string `json:"title"`
		Source struct {
			Kind string `json:"kind"`
		} `json:"source"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	t := strings.TrimSpace(d.Title)
	if t == "" {
		return nil
	}
	p.title = t
	switch d.Source.Kind {
	case "user":
		p.titleSrc = model.TitleUser
	case "fallback":
		p.titleSrc = model.TitlePrompt
	default: // "provider" (an LLM-generated title) and future kinds
		p.titleSrc = model.TitleAgent
	}
	return nil
}

// session returns the folded session; ok is false for subagent sessions and
// sessions without any message. dirName is the fallback id.
func (p *parser) session(dirName, root string) (model.Session, bool) {
	if p.hdr != nil && p.hdr.subagent {
		return model.Session{}, false
	}
	var msgs []model.Message
	for i, m := range p.msgs {
		if id := p.inboxIDs[i]; id != "" && p.userIDs[id] {
			continue // the same prompt was also logged as user/message
		}
		msgs = append(msgs, m)
	}
	if len(msgs) == 0 {
		return model.Session{}, false
	}
	s := model.Session{
		Agent:       "dsh",
		NativeID:    dirName,
		Title:       p.title,
		TitleSource: p.titleSrc,
		StartedAt:   p.first,
		UpdatedAt:   p.last,
		Coverage:    model.CoverageFull,
		Origin:      model.OriginLive,
		Root:        root,
		Messages:    msgs,
	}
	if h := p.hdr; h != nil {
		if h.id != "" {
			s.NativeID = h.id
		}
		s.Cwd = h.cwd
		if !h.createdAt.IsZero() {
			s.StartedAt = h.createdAt
		}
	}
	if s.UpdatedAt.Before(s.StartedAt) {
		s.UpdatedAt = s.StartedAt
	}
	if s.Title == "" {
		s.TitleSource = model.TitleNone
		for _, m := range msgs {
			if m.Role == model.RoleUser && m.Kind == model.KindText {
				s.Title, s.TitleSource = promptTitle(m.Text), model.TitlePrompt
				break
			}
		}
	}
	return s, true
}

// blocksText returns the text of a content value: a plain string, or an array
// of blocks where "text" blocks give their text and "image" blocks "[image]".
// Other blocks (reasoning, tool-call, ...) are dropped. Parts are joined by "\n".
func blocksText(raw json.RawMessage) string {
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
	case '[':
		var blocks []struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) != nil {
			return ""
		}
		var parts []string
		for _, b := range blocks {
			switch {
			case (b.Type == "text" || b.Type == "") && b.Text != nil && *b.Text != "":
				parts = append(parts, *b.Text)
			case b.Type == "image":
				parts = append(parts, "[image]")
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// toolCallText renders a tool call as "name: <command / file path / pattern / url>",
// falling back to the first 200 runes of the raw arguments.
func toolCallText(name string, raw json.RawMessage) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	args := bytes.TrimSpace(raw)
	if len(args) > 0 && args[0] == '"' { // arguments is a JSON string holding JSON
		var s string
		if json.Unmarshal(args, &s) == nil {
			args = bytes.TrimSpace([]byte(s))
		}
	}
	var summary string
	var m map[string]any
	if json.Unmarshal(args, &m) == nil {
		var parts []string
		for _, k := range []string{"command", "file_path", "pattern", "path", "url", "query"} {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, v)
			}
		}
		summary = strings.Join(parts, " ")
	}
	if summary == "" {
		if s := string(args); s != "" && s != "{}" && s != "null" {
			summary = truncateRunes(s, 200)
		}
	}
	if summary == "" {
		return name
	}
	return name + ": " + summary
}

// promptTitle collapses whitespace and keeps the first 100 runes.
func promptTitle(s string) string {
	return truncateRunes(strings.Join(strings.Fields(s), " "), 100)
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

// truncateBytes keeps at most n bytes of s without splitting a UTF-8 sequence.
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
