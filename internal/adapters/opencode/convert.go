package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// session is the store-independent shape of one OpenCode session, filled from
// the SQLite tables or from the legacy JSON tree.
type session struct {
	id, parentID, directory, title string
	created, updated               int64 // Unix ms
	messages                       []*message
}

// message is one OpenCode message with its parts already converted.
type message struct {
	role model.Role
	at   time.Time
	skip bool            // a compaction summary: its parts are dropped
	out  []model.Message // converted parts, in part order
}

// messageData is the part of a message's JSON "data" that Athar uses.
type messageData struct {
	Role    string          `json:"role"`
	Summary json.RawMessage `json:"summary"` // assistant: true for a compaction summary; user: an object
	Time    struct {
		Created float64 `json:"created"`
	} `json:"time"`
}

type span struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// partData is the part of a part's JSON "data" that Athar uses. Types:
// text, reasoning, tool, file, subtask, step-start, step-finish, patch,
// snapshot, agent, retry, compaction.
type partData struct {
	Type      string     `json:"type"`
	Text      string     `json:"text"`
	Synthetic bool       `json:"synthetic"`
	Ignored   bool       `json:"ignored"`
	Time      *span      `json:"time"`
	Tool      string     `json:"tool"`
	State     *toolState `json:"state"`
	Mime      string     `json:"mime"`
	Filename  string     `json:"filename"`
	Prompt    string     `json:"prompt"` // subtask
}

type toolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
	Error  json.RawMessage `json:"error"`
	Time   *span           `json:"time"`
}

// errUnknownRole marks a message whose role Athar does not know.
var errUnknownRole = errors.New("unknown message role")

// parseMessage decodes a message's JSON data. colCreated is the row's
// time_created column (0 when there is none, as in the legacy tree).
func parseMessage(data []byte, colCreated int64) (*message, error) {
	var d messageData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	m := &message{}
	switch d.Role {
	case "user":
		m.role = model.RoleUser
	case "assistant":
		m.role = model.RoleAssistant
		// The summary an agent writes when it compacts a long session repeats
		// earlier content (Claude's isCompactSummary is skipped the same way).
		m.skip = bytes.Equal(bytes.TrimSpace(d.Summary), []byte("true"))
	default:
		return nil, fmt.Errorf("%w %q", errUnknownRole, d.Role)
	}
	m.at = msTime(d.Time.Created)
	if m.at.IsZero() {
		m.at = msTime(float64(colCreated))
	}
	return m, nil
}

// addPart converts one part's JSON data and appends the result to m.
// colCreated is the row's time_created column (0 when unknown). Reasoning,
// step markers, patches and snapshots are dropped; synthetic or ignored text
// parts (file contents OpenCode inlines for the model) are dropped too.
func (m *message) addPart(data []byte, colCreated int64) error {
	var p partData
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if m.skip {
		return nil
	}
	at := msTime(float64(colCreated))
	if at.IsZero() {
		at = m.at
	}
	switch p.Type {
	case "text":
		if p.Synthetic || p.Ignored {
			return nil
		}
		if p.Time != nil && p.Time.Start > 0 {
			at = msTime(p.Time.Start)
		}
		m.addText(p.Text, at)
	case "subtask":
		m.addText(p.Prompt, at)
	case "file":
		switch {
		case strings.HasPrefix(p.Mime, "image/"):
			m.addText("[image]", at)
		case p.Filename != "":
			m.addText("[file: "+p.Filename+"]", at)
		default:
			m.addText("[file]", at)
		}
	case "tool":
		var st toolState
		if p.State != nil {
			st = *p.State
		}
		callAt, outAt := at, at
		if st.Time != nil {
			if st.Time.Start > 0 {
				callAt, outAt = msTime(st.Time.Start), msTime(st.Time.Start)
			}
			if st.Time.End > 0 {
				outAt = msTime(st.Time.End)
			}
		}
		m.out = append(m.out, model.Message{
			Role: model.RoleAssistant, Kind: model.KindToolCall, At: callAt,
			Text: toolSummary(p.Tool, st.Input),
		})
		out := strings.TrimSpace(rawText(st.Output))
		if out == "" {
			out = strings.TrimSpace(rawText(st.Error))
		}
		if out != "" {
			m.out = append(m.out, model.Message{
				Role: model.RoleTool, Kind: model.KindToolOutput, At: outAt,
				Text: truncate(out, model.MaxToolOutput),
			})
		}
	}
	return nil
}

// addText appends text in m's role, joining it to the previous text of the
// same message when no tool call came in between.
func (m *message) addText(text string, at time.Time) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if n := len(m.out); n > 0 && m.out[n-1].Kind == model.KindText && m.out[n-1].Role == m.role {
		m.out[n-1].Text += "\n\n" + text
		return
	}
	m.out = append(m.out, model.Message{Role: m.role, Kind: model.KindText, At: at, Text: text})
}

// build turns a parsed session into a model.Session. It reports false for
// child (subagent) sessions and for sessions with nothing to archive.
func build(agent string, s *session) (model.Session, bool) {
	if s.parentID != "" {
		return model.Session{}, false
	}
	var msgs []model.Message
	for _, m := range s.messages {
		msgs = append(msgs, m.out...)
	}
	if len(msgs) == 0 {
		return model.Session{}, false
	}
	out := model.Session{
		Agent:       agent,
		NativeID:    s.id,
		Cwd:         filepath.FromSlash(s.directory),
		Title:       strings.TrimSpace(s.title),
		TitleSource: model.TitleAgent,
		StartedAt:   msTime(float64(s.created)),
		UpdatedAt:   msTime(float64(s.updated)),
		Coverage:    model.CoverageFull,
		Origin:      model.OriginLive,
		Messages:    msgs,
	}
	if isDefaultTitle(out.Title) {
		out.Title, out.TitleSource = "", model.TitleNone
		for _, m := range msgs {
			if m.Role == model.RoleUser && m.Kind == model.KindText {
				out.Title, out.TitleSource = promptTitle(m.Text), model.TitlePrompt
				break
			}
		}
	}
	// StartedAt is the session's creation time (else its first dated message);
	// UpdatedAt is the later of the session's update time and its last message.
	for _, m := range msgs {
		if m.At.IsZero() {
			continue
		}
		if out.StartedAt.IsZero() {
			out.StartedAt = m.At
		}
		if m.At.After(out.UpdatedAt) {
			out.UpdatedAt = m.At
		}
	}
	if out.UpdatedAt.IsZero() {
		out.UpdatedAt = out.StartedAt
	}
	return out, true
}

// defaultTitle matches the placeholder OpenCode gives a session until it
// generates a real title: "New session - <ISO time>" ("Child session - …" for subagents).
var defaultTitle = regexp.MustCompile(`^(New|Child) session - \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)

func isDefaultTitle(title string) bool {
	title = strings.TrimSpace(title)
	return title == "" || defaultTitle.MatchString(title)
}

// promptTitle makes a title from a prompt: whitespace collapsed, at most 100 runes.
func promptTitle(text string) string {
	return firstRunes(strings.Join(strings.Fields(text), " "), 100)
}

// toolInputKeys are the input fields that best describe a tool call, in order.
var toolInputKeys = []string{"command", "filePath", "pattern", "url", "query", "path", "description", "prompt"}

// toolSummary describes a tool call as "name: <command, file path, pattern, …>",
// or "name: <first 200 characters of the input JSON>" when no known field is set.
func toolSummary(tool string, input []byte) string {
	name := strings.TrimSpace(tool)
	if name == "" {
		name = "tool"
	}
	input = bytes.TrimSpace(input)
	if len(input) == 0 || bytes.Equal(input, []byte("null")) {
		return name
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err == nil {
		if len(fields) == 0 {
			return name
		}
		for _, k := range toolInputKeys {
			var v string
			if json.Unmarshal(fields[k], &v) == nil && strings.TrimSpace(v) != "" {
				return name + ": " + strings.TrimSpace(v)
			}
		}
	}
	var compact bytes.Buffer
	if json.Compact(&compact, input) == nil {
		input = compact.Bytes()
	}
	return name + ": " + firstRunes(string(input), 200)
}

// rawText returns a JSON string's value, or the compact JSON of any other value.
func rawText(r json.RawMessage) string {
	r = bytes.TrimSpace(r)
	if len(r) == 0 || bytes.Equal(r, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	var compact bytes.Buffer
	if json.Compact(&compact, r) == nil {
		return compact.String()
	}
	return string(r)
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
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

// msTime converts OpenCode's Unix-millisecond timestamps to UTC. Values that
// can only be seconds (before 1973 as milliseconds) are read as seconds.
func msTime(v float64) time.Time {
	switch {
	case v <= 0:
		return time.Time{}
	case v < 1e11:
		return time.Unix(int64(v), 0).UTC()
	default:
		return time.UnixMilli(int64(v)).UTC()
	}
}
