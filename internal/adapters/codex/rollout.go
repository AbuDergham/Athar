package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// uuidInName finds the thread id in rollout-<ts>-<uuid>[_<rollout id>].jsonl.
var uuidInName = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// ctxCheckEvery is how many lines are read between context checks.
const ctxCheckEvery = 256

// freshWindow: a .jsonl.zst rollout that fails to decode and was modified
// more recently than this is reported as adapters.ErrRetryLater (spec §4.3).
const freshWindow = 5 * time.Second

// recentlyModified reports whether path was modified less than freshWindow ago
// (or has an mtime in the near future, as clocks may disagree).
func recentlyModified(path string) bool {
	_, mtime, err := fsread.Stat(path)
	if err != nil {
		return false
	}
	age := time.Since(mtime)
	return age < freshWindow && age > -freshWindow
}

// envelope is one rollout line: {timestamp, type, payload} since mid-2025.
// Legacy rollouts have a bare meta line {id, timestamp, instructions},
// {"record_type":"state"} lines, and bare response items {type:"message", …}.
type envelope struct {
	Timestamp  string          `json:"timestamp"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	RecordType string          `json:"record_type"`
	ID         json.RawMessage `json:"id"`
}

type sessionMeta struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Cwd       string          `json:"cwd"`
	Source    json.RawMessage `json:"source"`
}

type typed struct {
	Type string `json:"type"`
}

type eventUserMessage struct {
	Message     string            `json:"message"`
	Kind        string            `json:"kind"`
	Images      []json.RawMessage `json:"images"`
	LocalImages []json.RawMessage `json:"local_images"`
}

type responseMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type functionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Input     string          `json:"input"`
	Output    json.RawMessage `json:"output"`
	Action    struct {
		Command json.RawMessage `json:"command"`
		Query   string          `json:"query"`
	} `json:"action"`
}

// entry classes: response-item user/assistant messages are only used when the
// file has no event_msg user/agent messages (older CLIs, legacy format).
const (
	fromEvent = iota
	fromResponseUser
	fromResponseAssistant
)

type entry struct {
	msg   model.Message
	class int
}

// rollout accumulates the state of one rollout file.
type rollout struct {
	metaSeen              bool
	id, metaCwd           string
	turnCwd, envCwd       string
	skip                  bool
	started, first, last  time.Time
	threadName            string
	entries               []entry
	hasUserEv, hasAgentEv bool
	skipped               int
}

func (r *rollout) touch(t time.Time) {
	if t.IsZero() {
		return
	}
	if r.first.IsZero() || t.Before(r.first) {
		r.first = t
	}
	if t.After(r.last) {
		r.last = t
	}
}

func (r *rollout) add(m model.Message, class int) {
	r.entries = append(r.entries, entry{m, class})
}

// readRollout parses one rollout (.jsonl or .jsonl.zst) into at most one session.
func readRollout(ctx context.Context, src adapters.Source, idx map[string]thread) (adapters.ReadResult, error) {
	r := &rollout{}
	n := 0
	fn := func(line []byte) error {
		n++
		if n%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		r.line(line)
		return nil
	}
	var err error
	if strings.HasSuffix(strings.ToLower(src.Path), ".zst") {
		err = fsread.ZstdLines(src.Path, fn)
		if err != nil && ctx.Err() == nil && recentlyModified(src.Path) {
			// a compressed rollout is written whole: a broken one that just
			// changed is most likely still being written
			err = fmt.Errorf("%s changed less than %v ago: %w", filepath.Base(src.Path), freshWindow, adapters.ErrRetryLater)
		}
	} else {
		err = fsread.Lines(src.Path, fn)
	}
	if err != nil {
		return adapters.ReadResult{}, err
	}
	res := adapters.ReadResult{Skipped: r.skipped}
	if r.skip {
		return res, nil
	}
	s, err := r.session(src, idx)
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", filepath.Base(src.Path), err))
		return res, nil
	}
	if len(s.Messages) > 0 {
		res.Sessions = []model.Session{s}
	}
	return res, nil
}

// line handles one JSONL line; lines that are not JSON are counted as skipped.
func (r *rollout) line(line []byte) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		r.skipped++
		return
	}
	at := parseTime(env.Timestamp)
	r.touch(at)
	switch env.Type {
	case "session_meta":
		r.meta(env.Payload)
	case "turn_context":
		var tc struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(env.Payload, &tc) == nil && r.turnCwd == "" {
			r.turnCwd = tc.Cwd
		}
	case "event_msg":
		r.event(env.Payload, at)
	case "response_item":
		r.item(env.Payload, at)
	case "compacted":
		// a summary written by compaction, not a message
	case "":
		if env.RecordType == "" && len(env.ID) > 0 && string(env.ID) != "null" {
			r.meta(line) // legacy meta line
		}
	default:
		if len(env.Payload) == 0 && env.RecordType == "" {
			r.item(line, at) // legacy bare response item
		}
	}
}

func (r *rollout) meta(raw json.RawMessage) {
	if r.metaSeen {
		return // later session_meta lines belong to a fork's parent
	}
	var m sessionMeta
	if json.Unmarshal(raw, &m) != nil {
		r.skipped++
		return
	}
	r.metaSeen = true
	r.id = m.ID
	r.metaCwd = m.Cwd
	r.started = parseTime(m.Timestamp)
	r.touch(r.started)
	if skipSource(m.Source) {
		r.skip = true
	}
}

func (r *rollout) event(raw json.RawMessage, at time.Time) {
	var t typed
	if json.Unmarshal(raw, &t) != nil {
		r.skipped++
		return
	}
	switch t.Type {
	case "user_message":
		r.hasUserEv = true
		var u eventUserMessage
		if json.Unmarshal(raw, &u) != nil {
			r.skipped++
			return
		}
		if u.Kind != "" && u.Kind != "plain" {
			if r.envCwd == "" {
				r.envCwd = envContextCwd(u.Message)
			}
			return // injected environment_context / user_instructions
		}
		if isInjected(u.Message) {
			if r.envCwd == "" {
				r.envCwd = envContextCwd(u.Message)
			}
			return
		}
		text := strings.TrimRight(userPromptText(u.Message), " \t\r\n")
		if strings.TrimSpace(text) == "" {
			text = ""
		}
		for range len(u.Images) + len(u.LocalImages) {
			if text != "" {
				text += "\n"
			}
			text += imageMarker
		}
		if text != "" {
			r.add(model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: text}, fromEvent)
		}
	case "agent_message":
		r.hasAgentEv = true
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &m) != nil {
			r.skipped++
			return
		}
		if strings.TrimSpace(m.Message) != "" {
			r.add(model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: at, Text: m.Message}, fromEvent)
		}
	case "thread_name_updated":
		var m struct {
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(raw, &m) == nil && strings.TrimSpace(m.ThreadName) != "" {
			r.threadName = m.ThreadName
		}
	}
}

func (r *rollout) item(raw json.RawMessage, at time.Time) {
	var t typed
	if json.Unmarshal(raw, &t) != nil {
		r.skipped++
		return
	}
	switch t.Type {
	case "message":
		var m responseMessage
		if json.Unmarshal(raw, &m) != nil {
			r.skipped++
			return
		}
		r.responseMessage(m, at)
	case "function_call", "custom_tool_call", "local_shell_call", "web_search_call",
		"function_call_output", "custom_tool_call_output":
		var c functionCall
		if json.Unmarshal(raw, &c) != nil {
			r.skipped++
			return
		}
		r.tool(t.Type, c, at)
	}
}

func (r *rollout) responseMessage(m responseMessage, at time.Time) {
	var items []contentItem
	if len(m.Content) > 0 && m.Content[0] == '"' {
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			items = []contentItem{{Type: "input_text", Text: s}}
		}
	} else if json.Unmarshal(m.Content, &items) != nil {
		r.skipped++
		return
	}
	switch m.Role {
	case "user":
		var kept []contentItem
		for _, it := range items {
			if isInjected(it.Text) {
				if r.envCwd == "" {
					r.envCwd = envContextCwd(it.Text)
				}
				continue
			}
			kept = append(kept, it)
		}
		if text := strings.TrimSpace(itemsText(kept)); text != "" {
			r.add(model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: userPromptText(itemsText(kept))}, fromResponseUser)
		}
	case "assistant":
		if text := itemsText(items); strings.TrimSpace(text) != "" {
			r.add(model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: at, Text: text}, fromResponseAssistant)
		}
	}
}

func (r *rollout) tool(typ string, c functionCall, at time.Time) {
	call := func(text string) {
		r.add(model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at, Text: text}, fromEvent)
	}
	switch typ {
	case "function_call":
		var args string
		if json.Unmarshal(c.Arguments, &args) != nil {
			args = string(c.Arguments) // arguments given as an object
		}
		call(callText(c.Name, args))
	case "custom_tool_call":
		call(customCallText(c.Name, c.Input))
	case "local_shell_call":
		if cmd := commandText(c.Action.Command); cmd != "" {
			call("shell: " + cmd)
		} else {
			call("shell")
		}
	case "web_search_call":
		if q := strings.TrimSpace(c.Action.Query); q != "" {
			call("web_search: " + q)
		} else {
			call("web_search")
		}
	case "function_call_output", "custom_tool_call_output":
		if m, ok := toolOutput(outputText(c.Output), at); ok {
			r.add(m, fromEvent)
		}
	}
}

// session builds the session once the whole file is read.
func (r *rollout) session(src adapters.Source, idx map[string]thread) (model.Session, error) {
	id := r.id
	if id == "" {
		id = uuidInName.FindString(filepath.Base(src.Path))
	}
	if id == "" {
		return model.Session{}, errNoID
	}
	var msgs []model.Message
	for _, e := range r.entries {
		switch {
		case e.class == fromResponseUser && r.hasUserEv:
		case e.class == fromResponseAssistant && r.hasAgentEv:
		default:
			msgs = append(msgs, e.msg)
		}
	}

	cwd := r.metaCwd
	if cwd == "" {
		cwd = r.turnCwd
	}
	if cwd == "" {
		cwd = r.envCwd
	}

	started, updated := r.started, r.last
	if !r.first.IsZero() && (started.IsZero() || r.first.Before(started)) {
		started = r.first
	}
	if updated.IsZero() { // legacy rollouts carry no per-line times
		if _, mtime, err := fsread.Stat(src.Path); err == nil {
			updated = mtime.UTC()
		}
	}
	if started.IsZero() {
		started = updated
	}
	if updated.Before(started) {
		updated = started
	}

	s := model.Session{
		Agent:     agentID,
		NativeID:  id,
		Cwd:       cwd,
		StartedAt: started,
		UpdatedAt: updated,
		Coverage:  model.CoverageFull,
		Origin:    model.OriginLive,
		Root:      src.Root,
		Messages:  msgs,
	}
	s.Title, s.TitleSource = pickTitle(collapse(r.threadName), idx[id].title, msgs)
	return s, nil
}

// pickTitle applies the title order: rollout thread name, index title, first prompt.
func pickTitle(threadName, indexTitle string, msgs []model.Message) (string, model.TitleSource) {
	if threadName != "" {
		return threadName, model.TitleAgent
	}
	if indexTitle != "" {
		return indexTitle, model.TitleAgent
	}
	for _, m := range msgs {
		if m.Role == model.RoleUser && m.Kind == model.KindText {
			if t := titleFrom(m.Text); t != "" {
				return t, model.TitlePrompt
			}
		}
	}
	return "", model.TitleNone
}
