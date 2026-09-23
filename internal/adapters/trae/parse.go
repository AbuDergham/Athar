package trae

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// parsedSession is one copy of a session from one store key.
type parsedSession struct {
	id               string
	title            string
	created, updated time.Time
	hasMore          bool
	msgs             []model.Message
}

// rawSession is a session element of a store list. Scalars are raw so a
// field of an unexpected type loses only that field.
type rawSession struct {
	SessionID json.RawMessage   `json:"sessionId"`
	Title     json.RawMessage   `json:"title"`
	Name      json.RawMessage   `json:"name"`
	CreatedAt json.RawMessage   `json:"createdAt"`
	UpdatedAt json.RawMessage   `json:"updatedAt"`
	HasMore   json.RawMessage   `json:"hasMore"`
	Messages  []json.RawMessage `json:"messages"`
}

// parseSession decodes one list element. ok is false (and skipped counts it)
// when the element is not a session; skipped also counts bad messages.
func parseSession(raw json.RawMessage) (s parsedSession, skipped int, ok bool) {
	var rs rawSession
	if err := json.Unmarshal(raw, &rs); err != nil {
		return s, 1, false
	}
	s.id = strings.TrimSpace(str(rs.SessionID))
	if s.id == "" {
		return s, 1, false
	}
	s.title = str(rs.Title)
	if strings.TrimSpace(s.title) == "" {
		s.title = str(rs.Name)
	}
	s.title = strings.TrimSpace(s.title)
	s.created = msTime(rs.CreatedAt)
	s.updated = msTime(rs.UpdatedAt)
	s.hasMore = strings.TrimSpace(string(rs.HasMore)) == "true"
	for _, rm := range rs.Messages {
		msgs, bad := convertMessage(rm)
		skipped += bad
		s.msgs = append(s.msgs, msgs...)
	}
	return s, skipped, true
}

// rawMessage is one element of session.messages.
type rawMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	Timestamp        json.RawMessage `json:"timestamp"`
	ParsedQuery      json.RawMessage `json:"parsedQuery"`
	AgentTaskContent json.RawMessage `json:"agentTaskContent"`
}

// taskContent is an agent-mode assistant message's work: proposal and plan.
// proposalReasoningContent (the model's thinking) is deliberately not decoded.
type taskContent struct {
	Proposal  string `json:"proposal"`
	Guideline struct {
		Thought   string            `json:"thought"`
		PlanItems []json.RawMessage `json:"planItems"`
	} `json:"guideline"`
}

// planItem is one step of an agent turn. reasoningContent is not decoded.
type planItem struct {
	ToolName string          `json:"toolName"`
	Thought  string          `json:"thought"`
	Params   json.RawMessage `json:"params"`
	Result   *planResult     `json:"result"`
}

// planResult is a tool's result.
type planResult struct {
	Data         json.RawMessage `json:"data"`
	ErrorMessage string          `json:"error_message"`
}

// finishTool ends an agent turn; its summary is the final answer.
const finishTool = "finish"

// convertMessage turns one stored message into archive messages. bad counts
// the parts that could not be decoded.
func convertMessage(raw json.RawMessage) (out []model.Message, bad int) {
	var rm rawMessage
	if err := json.Unmarshal(raw, &rm); err != nil {
		return nil, 1
	}
	at := msTime(rm.Timestamp)
	switch strings.ToLower(strings.TrimSpace(rm.Role)) {
	case "user":
		text := textOf(rm.Content)
		if strings.TrimSpace(text) == "" {
			text = queryText(rm.ParsedQuery)
		}
		if strings.TrimSpace(text) != "" {
			out = append(out, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: text})
		}
	case "assistant":
		a := assistant{at: at, seen: map[string]bool{}}
		a.text(textOf(rm.Content))
		if len(rm.AgentTaskContent) > 0 && string(rm.AgentTaskContent) != "null" {
			var tc taskContent
			if err := json.Unmarshal(rm.AgentTaskContent, &tc); err != nil {
				bad++
			} else {
				a.text(tc.Proposal)
				a.text(tc.Guideline.Thought)
				for _, rp := range tc.Guideline.PlanItems {
					var p planItem
					if err := json.Unmarshal(rp, &p); err != nil {
						bad++
						continue
					}
					a.plan(p)
				}
			}
		}
		out = a.out
	}
	return out, bad
}

// assistant collects the messages of one assistant turn; the same text
// (a finish summary repeating the step's thought) is kept once.
type assistant struct {
	at   time.Time
	seen map[string]bool
	out  []model.Message
}

func (a *assistant) text(s string) {
	key := strings.TrimSpace(s)
	if key == "" || a.seen[key] {
		return
	}
	a.seen[key] = true
	a.out = append(a.out, model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: a.at, Text: s})
}

func (a *assistant) plan(p planItem) {
	a.text(p.Thought)
	name := strings.TrimSpace(p.ToolName)
	switch name {
	case "":
		return
	case finishTool:
		summary := paramString(p.Params, "summary")
		if summary == "" && p.Result != nil {
			summary = paramString(p.Result.Data, "summary")
		}
		a.text(summary)
		return
	}
	a.out = append(a.out, model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: a.at, Text: toolCallText(name, p.Params)})
	if p.Result != nil {
		if o := toolOutput(*p.Result); o != "" {
			a.out = append(a.out, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, At: a.at, Text: o})
		}
	}
}

// toolCallText renders a tool call as "name: <command, paths or query>",
// falling back to the first 200 runes of the parameters' JSON.
func toolCallText(name string, params json.RawMessage) string {
	var p map[string]json.RawMessage
	if json.Unmarshal(params, &p) != nil || len(p) == 0 {
		return name
	}
	arg := ""
	if cmd := str(p["command"]); strings.TrimSpace(cmd) != "" {
		parts := []string{cmd}
		parts = append(parts, strList(p["args"])...)
		arg = strings.Join(parts, " ")
	}
	for _, k := range []string{"file_path", "filePath"} {
		if arg == "" {
			arg = str(p[k])
		}
	}
	if arg == "" {
		arg = strings.Join(strList(p["file_paths"]), ", ")
	}
	if arg == "" {
		var files []map[string]json.RawMessage
		if json.Unmarshal(p["files"], &files) == nil {
			var paths []string
			for _, f := range files {
				fp := str(f["file_path"])
				if fp == "" {
					fp = str(f["filePath"])
				}
				if fp != "" {
					paths = append(paths, fp)
				}
			}
			arg = strings.Join(paths, ", ")
		}
	}
	for _, k := range []string{"dir_path", "query", "preview_url", "url"} {
		if arg == "" {
			arg = str(p[k])
		}
	}
	if strings.TrimSpace(arg) == "" {
		var buf bytes.Buffer
		if json.Compact(&buf, params) == nil {
			arg = cutRunes(buf.String(), 200)
		}
	}
	return name + ": " + arg
}

// toolOutput extracts readable output from a tool result (error message,
// stdout, file contents, search results), at most model.MaxToolOutput bytes.
func toolOutput(r planResult) string {
	if e := strings.TrimSpace(r.ErrorMessage); e != "" {
		return clip("error: "+e, model.MaxToolOutput)
	}
	var d map[string]json.RawMessage
	if json.Unmarshal(r.Data, &d) != nil {
		return ""
	}
	for _, k := range []string{"stdout", "result", "file_tree"} {
		if s := str(d[k]); strings.TrimSpace(s) != "" {
			return clip(s, model.MaxToolOutput)
		}
	}
	var contents []map[string]json.RawMessage
	if json.Unmarshal(d["contents"], &contents) == nil {
		var parts []string
		for _, c := range contents {
			if s := str(c["content"]); s != "" {
				parts = append(parts, s)
			}
		}
		return clip(strings.Join(parts, "\n"), model.MaxToolOutput)
	}
	return ""
}

// textOf reads a message content: a string, or an array of strings and
// {text} parts.
func textOf(raw json.RawMessage) string {
	if s := str(raw); s != "" {
		return s
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if s := str(p); s != "" {
			out = append(out, s)
			continue
		}
		var obj struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(p, &obj) == nil && obj.Text != "" {
			out = append(out, obj.Text)
		}
	}
	return strings.Join(out, "\n")
}

// queryText joins the string items of parsedQuery (file mentions are objects
// and are left out).
func queryText(raw json.RawMessage) string {
	return strings.Join(strList(raw), " ")
}

// paramString reads one string field of a JSON object.
func paramString(raw json.RawMessage, key string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return str(m[key])
}

// str decodes a JSON string; anything else yields "".
func str(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '"' {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// strList returns the string items of a JSON array.
func strList(raw json.RawMessage) []string {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		if s := str(it); strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
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
