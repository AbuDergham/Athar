package zcode

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// messageData is the part of a message's JSON "data" that Athar uses.
type messageData struct {
	Role       string          `json:"role"`
	Synthetic  json.RawMessage `json:"synthetic"`
	Visibility string          `json:"visibility"`
	Source     string          `json:"source"`
	Summary    json.RawMessage `json:"summary"`
	Mode       string          `json:"mode"`
	Time       json.RawMessage `json:"time"`
}

// partData is the part of a part's JSON "data" that Athar uses.
type partData struct {
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	Synthetic        json.RawMessage `json:"synthetic"`
	Ignored          json.RawMessage `json:"ignored"`
	Time             json.RawMessage `json:"time"`
	Tool             string          `json:"tool"`
	State            *toolState      `json:"state"`
	Mime             string          `json:"mime"`
	Filename         string          `json:"filename"`
	Prompt           string          `json:"prompt"`      // subagent
	Description      string          `json:"description"` // subagent
	SummaryMessageID string          `json:"summaryMessageId"`
}

type toolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
	Error  json.RawMessage `json:"error"`
	Time   json.RawMessage `json:"time"`
}

// span is a {start, end} (or {created, completed}) time pair in Unix ms.
type span struct {
	Start, End float64
}

// parseSpan reads a lenient time object; unknown shapes give a zero span.
func parseSpan(r json.RawMessage) span {
	var v struct {
		Start     float64 `json:"start"`
		End       float64 `json:"end"`
		Created   float64 `json:"created"`
		Completed float64 `json:"completed"`
	}
	if len(r) == 0 || json.Unmarshal(r, &v) != nil {
		return span{}
	}
	s := span{Start: v.Start, End: v.End}
	if s.Start == 0 {
		s.Start = v.Created
	}
	if s.End == 0 {
		s.End = v.Completed
	}
	return s
}

// hiddenUserSources are user-message sources ZCode injects for the model
// (not typed by the user).
var hiddenUserSources = map[string]bool{
	"todo_reminder":     true,
	"goal-continuation": true,
	"background_task":   true,
	"subagent":          true,
}

// convertSession turns a session row and its messages into a model.Session
// (Messages may be empty). skipped counts messages and parts whose JSON did
// not parse.
func convertSession(s sessionRow, msgs []*messageRow) (model.Session, int) {
	out := model.Session{
		Agent:     ID,
		NativeID:  s.id,
		Cwd:       cleanDir(s.directory),
		StartedAt: msTime(float64(s.created)),
		UpdatedAt: msTime(float64(s.updated)),
		Coverage:  model.CoverageFull,
		Origin:    model.OriginLive,
	}
	out.Title, out.TitleSource = sessionTitle(s)

	skipped := 0
	// Compaction parts name the summary message that repeats earlier content.
	summaries := map[string]bool{}
	parsed := make([][]*partData, len(msgs))
	for i, m := range msgs {
		for _, pr := range m.parts {
			var p partData
			if json.Unmarshal(pr.data, &p) != nil {
				skipped++
				parsed[i] = append(parsed[i], nil)
				continue
			}
			if p.Type == "compaction" && p.SummaryMessageID != "" {
				summaries[p.SummaryMessageID] = true
			}
			parsed[i] = append(parsed[i], &p)
		}
	}

	for i, m := range msgs {
		var d messageData
		if json.Unmarshal(m.data, &d) != nil {
			skipped++
			continue
		}
		if summaries[m.id] || isTrue(d.Summary) || d.Mode == "compaction" {
			continue
		}
		var role model.Role
		switch d.Role {
		case "user":
			if isTrue(d.Synthetic) || d.Visibility == "model-only" || hiddenUserSources[d.Source] {
				continue
			}
			role = model.RoleUser
		case "assistant":
			role = model.RoleAssistant
		default:
			skipped++
			continue
		}
		at := msTime(parseSpan(d.Time).Start)
		if at.IsZero() {
			at = msTime(float64(m.created))
		}
		c := converter{role: role, at: at}
		for j, p := range parsed[i] {
			if p != nil {
				c.add(p, msTime(float64(m.parts[j].created)))
			}
		}
		out.Messages = append(out.Messages, c.out...)
	}
	return out, skipped
}

// sessionTitle maps ZCode's title and title_source. The "default" source is a
// placeholder title ("New task"); finishSession then uses the first prompt.
func sessionTitle(s sessionRow) (string, model.TitleSource) {
	title := strings.Join(strings.Fields(s.title), " ")
	if title == "" {
		return "", model.TitleNone
	}
	switch s.titleSource {
	case "custom":
		return title, model.TitleUser
	case "generated":
		return title, model.TitleAgent
	case "first_input":
		return firstRunes(title, maxTitle), model.TitlePrompt
	case "default":
		return "", model.TitleNone
	default:
		// Older schema without title_source: ZCode titled the session itself.
		return title, model.TitleAgent
	}
}

// converter accumulates the archive messages of one ZCode message.
type converter struct {
	role model.Role
	at   time.Time
	out  []model.Message
}

// add converts one part. colAt is the part row's time_created. Reasoning,
// step markers, snapshots, patches, retries and compaction markers are dropped;
// synthetic or ignored text parts (content ZCode inlines for the model) too.
func (c *converter) add(p *partData, colAt time.Time) {
	at := colAt
	if at.IsZero() {
		at = c.at
	}
	switch p.Type {
	case "text":
		if isTrue(p.Synthetic) || isTrue(p.Ignored) {
			return
		}
		if t := msTime(parseSpan(p.Time).Start); !t.IsZero() {
			at = t
		}
		c.addText(p.Text, at)
	case "subagent":
		text := strings.TrimSpace(p.Prompt)
		if text == "" {
			text = strings.TrimSpace(p.Description)
		}
		c.addText(text, at)
	case "file":
		switch {
		case strings.HasPrefix(p.Mime, "image/"):
			c.addText("[image]", at)
		case p.Filename != "":
			c.addText("[file: "+p.Filename+"]", at)
		default:
			c.addText("[file]", at)
		}
	case "tool":
		var st toolState
		if p.State != nil {
			st = *p.State
		}
		callAt, outAt := at, at
		if sp := parseSpan(st.Time); sp.Start > 0 {
			callAt, outAt = msTime(sp.Start), msTime(sp.Start)
			if sp.End > 0 {
				outAt = msTime(sp.End)
			}
		}
		c.out = append(c.out, model.Message{
			Role: model.RoleAssistant, Kind: model.KindToolCall, At: callAt,
			Text: toolSummary(p.Tool, st.Input),
		})
		out := strings.TrimSpace(rawText(st.Output))
		if out == "" {
			out = strings.TrimSpace(rawText(st.Error))
		}
		if out != "" {
			c.out = append(c.out, model.Message{
				Role: model.RoleTool, Kind: model.KindToolOutput, At: outAt,
				Text: truncate(out, model.MaxToolOutput),
			})
		}
	}
}

// addText appends text in c's role, joining it to the previous text of the
// same message when no tool call came in between.
func (c *converter) addText(text string, at time.Time) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if n := len(c.out); n > 0 && c.out[n-1].Kind == model.KindText && c.out[n-1].Role == c.role {
		c.out[n-1].Text += "\n\n" + text
		return
	}
	c.out = append(c.out, model.Message{Role: c.role, Kind: model.KindText, At: at, Text: text})
}

// maxTitle is the number of runes of a prompt used as a title.
const maxTitle = 100

// promptTitle makes a title from a prompt: whitespace collapsed, at most maxTitle runes.
func promptTitle(text string) string {
	return firstRunes(strings.Join(strings.Fields(text), " "), maxTitle)
}

// toolInputKeys are the input fields that best describe a tool call, in order.
// ZCode's tools use Claude Code's names (Bash, Read, Edit, Write, Glob, Grep,
// WebFetch, WebSearch, TodoWrite, Agent, Skill, …) and snake_case inputs.
var toolInputKeys = []string{
	"command", "file_path", "filePath", "notebook_path", "pattern", "url", "query",
	"path", "description", "prompt", "skill", "name",
}

// maxToolArgs is the number of runes of raw tool input kept in a tool call.
const maxToolArgs = 200

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
	if json.Unmarshal(input, &fields) == nil {
		if len(fields) == 0 {
			return name
		}
		for _, k := range toolInputKeys {
			var v string
			if json.Unmarshal(fields[k], &v) == nil && strings.TrimSpace(v) != "" {
				return name + ": " + firstRunes(strings.TrimSpace(v), 1000)
			}
		}
	}
	var compact bytes.Buffer
	if json.Compact(&compact, input) == nil {
		input = compact.Bytes()
	}
	return name + ": " + firstRunes(string(input), maxToolArgs)
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

// isTrue reports whether a raw JSON value is the literal true.
func isTrue(r json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(r), []byte("true")) }

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

// msTime converts ZCode's Unix-millisecond timestamps to UTC. Values that can
// only be seconds (before 1973 as milliseconds) are read as seconds.
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
