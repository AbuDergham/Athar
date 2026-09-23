package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// record holds every field a transcript line (or a $set payload, or a legacy
// document) may carry. All fields stay raw so an unexpected type never makes
// a line undecodable; the CLI's own type checks are applied afterwards.
type record struct {
	ID             json.RawMessage `json:"id"`
	RewindTo       json.RawMessage `json:"$rewindTo"`
	Set            json.RawMessage `json:"$set"`
	SessionID      json.RawMessage `json:"sessionId"`
	ProjectHash    json.RawMessage `json:"projectHash"`
	StartTime      json.RawMessage `json:"startTime"`
	LastUpdated    json.RawMessage `json:"lastUpdated"`
	Summary        json.RawMessage `json:"summary"`
	Kind           json.RawMessage `json:"kind"`
	Messages       json.RawMessage `json:"messages"`
	Timestamp      json.RawMessage `json:"timestamp"`
	Type           json.RawMessage `json:"type"`
	Content        json.RawMessage `json:"content"`
	DisplayContent json.RawMessage `json:"displayContent"`
	ToolCalls      json.RawMessage `json:"toolCalls"`
}

// message is one replayed message record.
type message struct {
	timestamp      string
	typ            string
	content        json.RawMessage
	displayContent json.RawMessage
	toolCalls      json.RawMessage
}

// conversation is the state of a transcript replay: metadata plus the
// message records in insertion order (like the CLI's Map).
type conversation struct {
	sessionID, projectHash, startTime, lastUpdated, summary, kind string

	order []string
	byID  map[string]*message
}

func newConversation() *conversation {
	return &conversation{byID: map[string]*message{}}
}

// apply replays one JSONL line; it reports false for a line that is not valid JSON.
func (c *conversation) apply(line []byte) bool {
	var r record
	if json.Unmarshal(line, &r) != nil {
		return false
	}
	if id, ok := jsonString(r.RewindTo); ok {
		c.rewind(id)
		return true
	}
	if c.put(&r) {
		return true
	}
	if isObject(r.Set) {
		var s record
		if json.Unmarshal(r.Set, &s) != nil {
			return false
		}
		if isArray(s.Messages) {
			c.clear() // checkpoint: the list is rebuilt from $set.messages
			c.addMessages(s.Messages)
		}
		c.merge(&s)
		return true
	}
	_, hasID := jsonString(r.SessionID)
	_, hasHash := jsonString(r.ProjectHash)
	if hasID && hasHash {
		c.merge(&r)
		if isArray(r.Messages) {
			c.addMessages(r.Messages)
		}
	}
	return true
}

// put stores a message record (a record whose id is a string). A known id is
// replaced in place; a new id is appended. It reports whether r was a message.
func (c *conversation) put(r *record) bool {
	id, ok := jsonString(r.ID)
	if !ok {
		return false
	}
	ts, _ := jsonString(r.Timestamp)
	typ, _ := jsonString(r.Type)
	m := &message{timestamp: ts, typ: typ, content: r.Content, displayContent: r.DisplayContent, toolCalls: r.ToolCalls}
	if _, seen := c.byID[id]; !seen {
		c.order = append(c.order, id)
	}
	c.byID[id] = m
	return true
}

func (c *conversation) addMessages(raw json.RawMessage) {
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil {
		return
	}
	for _, e := range elems {
		var r record
		if json.Unmarshal(e, &r) == nil {
			c.put(&r)
		}
	}
}

// rewind drops the message id and everything after it; an unknown id drops everything.
func (c *conversation) rewind(id string) {
	for i, x := range c.order {
		if x == id {
			for _, gone := range c.order[i:] {
				delete(c.byID, gone)
			}
			c.order = c.order[:i]
			return
		}
	}
	c.clear()
}

func (c *conversation) clear() {
	c.order = nil
	c.byID = map[string]*message{}
}

// merge copies the string metadata fields that r carries.
func (c *conversation) merge(r *record) {
	for _, f := range []struct {
		raw json.RawMessage
		dst *string
	}{
		{r.SessionID, &c.sessionID},
		{r.ProjectHash, &c.projectHash},
		{r.StartTime, &c.startTime},
		{r.LastUpdated, &c.lastUpdated},
		{r.Summary, &c.summary},
		{r.Kind, &c.kind},
	} {
		if s, ok := jsonString(f.raw); ok {
			*f.dst = s
		}
	}
}

// parseDocument reads a legacy single-document transcript (ConversationRecord).
// A syntax error is returned as an error; a valid document without a
// sessionId returns (nil, nil).
func parseDocument(b []byte) (*conversation, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	c := newConversation()
	c.merge(&r)
	if c.sessionID == "" {
		return nil, nil
	}
	if isArray(r.Messages) {
		c.addMessages(r.Messages)
	}
	return c, nil
}

// readTranscript reads a JSONL transcript (legacy=false) or a legacy .json one.
func readTranscript(ctx context.Context, path string, legacy bool, cwd, root string) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	var c *conversation
	if legacy {
		b, err := fsread.ReadFile(path)
		if err != nil {
			return res, err
		}
		c, err = parseDocument(b)
		if err != nil {
			return res, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
	} else {
		c = newConversation()
		n := 0
		err := fsread.Lines(path, func(line []byte) error {
			if n++; n%1000 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if !c.apply(line) {
				res.Skipped++
			}
			return nil
		})
		if err != nil {
			return res, err
		}
		if c.sessionID == "" || c.projectHash == "" {
			// Like the CLI: a JSONL file without a metadata line may be a whole
			// legacy document.
			if b, err := fsread.ReadFile(path); err == nil {
				if d, err := parseDocument(b); err == nil && d != nil {
					c, res.Skipped = d, 0
				}
			}
		}
	}
	if c == nil || c.sessionID == "" {
		res.Warnings = append(res.Warnings, filepath.Base(path)+": no session metadata")
		return res, nil
	}
	s, ok := c.session(cwd, root)
	if !ok {
		return res, nil
	}
	if s.StartedAt.IsZero() {
		if _, mtime, err := fsread.Stat(path); err == nil {
			s.StartedAt, s.UpdatedAt = mtime.UTC(), mtime.UTC()
		}
	}
	res.Sessions = append(res.Sessions, s)
	return res, nil
}

// session builds the model session; ok is false for subagent sessions and
// sessions with no conversation (startup-only files).
func (c *conversation) session(cwd, root string) (model.Session, bool) {
	if c.kind == "subagent" {
		return model.Session{}, false
	}
	msgs := c.messages()
	if len(msgs) == 0 {
		return model.Session{}, false
	}
	s := model.Session{
		Agent:       agentID,
		NativeID:    c.sessionID,
		Cwd:         cwd,
		TitleSource: model.TitleNone,
		Coverage:    model.CoverageFull,
		Origin:      model.OriginLive,
		Root:        root,
		Messages:    msgs,
	}
	if t := strings.Join(strings.Fields(c.summary), " "); t != "" {
		s.Title, s.TitleSource = t, model.TitleAgent
	} else {
		for _, m := range msgs {
			if m.Role == model.RoleUser && m.Kind == model.KindText {
				s.Title, s.TitleSource = promptTitle(m.Text), model.TitlePrompt
				break
			}
		}
	}
	s.StartedAt, s.UpdatedAt = span(parseTime(c.startTime), parseTime(c.lastUpdated), msgs)
	return s, true
}

// span returns the first and last activity: the earliest of startTime and the
// message times, and the latest of lastUpdated and the message times.
func span(start, last time.Time, msgs []model.Message) (time.Time, time.Time) {
	for _, m := range msgs {
		if m.At.IsZero() {
			continue
		}
		if start.IsZero() || m.At.Before(start) {
			start = m.At
		}
		if m.At.After(last) {
			last = m.At
		}
	}
	if start.IsZero() {
		start = last
	}
	if last.Before(start) {
		last = start
	}
	return start, last
}

// messages converts the replayed records to model messages. Each tool call id
// yields one call and one output even when the record appears twice (in
// toolCalls[] and again as functionCall/functionResponse parts).
func (c *conversation) messages() []model.Message {
	var out []model.Message
	calls := map[string]bool{}
	outputs := map[string]bool{}
	add := func(role model.Role, kind model.Kind, at time.Time, text string) {
		out = append(out, model.Message{Role: role, Kind: kind, At: at, Text: text})
	}
	addOutput := func(id string, at time.Time, text string) {
		if id != "" && outputs[id] {
			return
		}
		if strings.TrimSpace(text) == "" {
			return
		}
		add(model.RoleTool, model.KindToolOutput, at, capOutput(text))
		if id != "" {
			outputs[id] = true
		}
	}
	addCall := func(id, name string, args json.RawMessage, at time.Time) {
		if id != "" && calls[id] {
			return
		}
		add(model.RoleAssistant, model.KindToolCall, at, callText(name, args))
		if id != "" {
			calls[id] = true
		}
	}

	for _, id := range c.order {
		m := c.byID[id]
		at := parseTime(m.timestamp)
		switch m.typ {
		case "user":
			parts := parseParts(m.content)
			text := strings.TrimSpace(partsText(parseParts(m.displayContent)))
			if text == "" {
				text = strings.TrimSpace(partsText(parts))
			}
			if !ignoredUser(text) {
				add(model.RoleUser, model.KindText, at, text)
			}
			for _, p := range parts {
				if fr := p.FunctionResponse; fr != nil {
					addOutput(fr.ID, at, responseText(fr.Response))
				}
			}
		case "gemini":
			parts := parseParts(m.content)
			if text := strings.TrimSpace(partsText(parts)); text != "" {
				add(model.RoleAssistant, model.KindText, at, text)
			}
			var tcs []toolCall
			_ = json.Unmarshal(m.toolCalls, &tcs) // tolerant: keeps what decodes
			for _, tc := range tcs {
				tat := parseTime(tc.Timestamp)
				if tat.IsZero() {
					tat = at
				}
				addCall(tc.ID, tc.Name, tc.Args, tat)
				addOutput(tc.ID, tat, resultText(tc))
			}
			for _, p := range parts {
				if fc := p.FunctionCall; fc != nil {
					addCall(fc.ID, fc.Name, fc.Args, at)
				}
			}
		}
		// info, error and warning records are UI notices, not conversation.
	}
	return out
}

func isObject(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{'
}

func isArray(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '['
}
