package codebuff

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// Log message prefixes that carry the conversation (see the package doc).
const (
	msgSendMessage = "[send-message]"
	msgStartAgent  = "Start agent "
	msgEndAgent    = "End agent "
	msgMainDone    = "Main prompt finished"
)

// logLine is one log.jsonl record; hostname, userId and userEmail are never decoded.
type logLine struct {
	Msg       string          `json:"msg"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// sessionState is the part of an SDK run state that names the working folder.
type sessionState struct {
	Cwd            string `json:"cwd"`
	ProjectContext struct {
		Cwd string `json:"cwd"`
	} `json:"projectContext"`
	FileContext struct {
		Cwd         string `json:"cwd"`
		ProjectRoot string `json:"projectRoot"`
	} `json:"fileContext"`
}

// runState is run-state.json, or runConfig.previousRun in the log.
type runState struct {
	Cwd          string       `json:"cwd"`
	SessionState sessionState `json:"sessionState"`
}

// cwd is the recorded working folder: projectContext.cwd, fileContext.cwd,
// sessionState.cwd, the top-level cwd, then fileContext.projectRoot.
func (r runState) cwd() string {
	ss := r.SessionState
	for _, c := range []string{ss.ProjectContext.Cwd, ss.FileContext.Cwd, ss.Cwd, r.Cwd, ss.FileContext.ProjectRoot} {
		if c = strings.TrimSpace(c); c != "" {
			return c
		}
	}
	return ""
}

type sendMessageData struct {
	RunConfig struct {
		Agent       string   `json:"agent"`
		Prompt      string   `json:"prompt"`
		PreviousRun runState `json:"previousRun"`
	} `json:"runConfig"`
}

type logToolCall struct {
	ToolName   string          `json:"toolName"`
	ToolCallID string          `json:"toolCallId"`
	Input      json.RawMessage `json:"input"`
}

type logToolResult struct {
	ToolName   string          `json:"toolName"`
	ToolCallID string          `json:"toolCallId"`
	Content    json.RawMessage `json:"content"`
}

type endStepData struct {
	FullResponse string          `json:"fullResponse"`
	ToolCalls    []logToolCall   `json:"toolCalls"`
	ToolResults  []logToolResult `json:"toolResults"`
}

type mainDoneData struct {
	Output struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"output"`
}

// logState follows the runs of one log.jsonl.
type logState struct {
	parsedChat
	mainType string // runConfig.agent of the current run
	mainID   string // agent id of the current run's main agent ("" until its first step)
}

// readLog extracts the prompts, the main agent's step texts, tool calls and
// results, and run errors from a debug log (see the package doc).
func readLog(ctx context.Context, path string) (parsedChat, error) {
	var st logState
	n := 0
	err := fsread.Lines(path, func(line []byte) error {
		if n++; n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var l logLine
		if json.Unmarshal(line, &l) != nil {
			st.skipped++
			return nil
		}
		st.handle(l)
		return nil
	})
	return st.parsedChat, err
}

func (st *logState) handle(l logLine) {
	at := parseRFC3339(l.Timestamp)
	switch {
	case strings.HasPrefix(l.Msg, msgSendMessage):
		var d sendMessageData
		if !decodeData(l.Data, &d) {
			st.skipped++
			return
		}
		rc := d.RunConfig
		st.mainType, st.mainID = strings.TrimSpace(rc.Agent), ""
		if st.cwd == "" {
			st.cwd = rc.PreviousRun.cwd()
		}
		st.touch(at)
		if p := strings.TrimSpace(rc.Prompt); p != "" {
			st.add(model.RoleUser, model.KindText, at, p)
		}

	case strings.HasPrefix(l.Msg, msgStartAgent):
		typ, id := stepAgent(l.Msg, msgStartAgent)
		if st.mainID == "" && (st.mainType == "" || typ == st.mainType) {
			st.mainType, st.mainID = typ, id
		}

	case strings.HasPrefix(l.Msg, msgEndAgent):
		typ, id := stepAgent(l.Msg, msgEndAgent)
		if st.mainType == "" || typ != st.mainType || (st.mainID != "" && id != st.mainID) {
			return // a subagent's step: its transcript is not archived (spec §1 non-goals)
		}
		var d endStepData
		if !decodeData(l.Data, &d) {
			st.skipped++
			return
		}
		st.touch(at)
		st.addStep(at, d)

	case l.Msg == msgMainDone:
		var d mainDoneData
		if !decodeData(l.Data, &d) {
			return
		}
		st.touch(at)
		if d.Output.Type == "error" {
			if m := strings.TrimSpace(d.Output.Message); m != "" {
				st.add(model.RoleSystem, model.KindText, at, truncate(m, model.MaxToolOutput))
			}
		}
	}
}

// addStep adds one main-agent step: its visible text, then each tool call
// followed by its result, then results without a call.
func (st *logState) addStep(at time.Time, d endStepData) {
	if t := strings.TrimSpace(d.FullResponse); t != "" {
		st.add(model.RoleAssistant, model.KindText, at, t)
	}
	used := make([]bool, len(d.ToolResults))
	for _, c := range d.ToolCalls {
		st.add(model.RoleAssistant, model.KindToolCall, at, toolCallText(c.ToolName, c.Input))
		for i, r := range d.ToolResults {
			if !used[i] && c.ToolCallID != "" && r.ToolCallID == c.ToolCallID {
				used[i] = true
				st.addOutput(at, r.Content)
			}
		}
	}
	for i, r := range d.ToolResults {
		if !used[i] {
			st.addOutput(at, r.Content)
		}
	}
}

func (st *logState) addOutput(at time.Time, content json.RawMessage) {
	if t := outputText(content); t != "" {
		st.add(model.RoleTool, model.KindToolOutput, at, t)
	}
}

func (pc *parsedChat) add(role model.Role, kind model.Kind, at time.Time, text string) {
	pc.msgs = append(pc.msgs, model.Message{Role: role, Kind: kind, At: at, Text: text})
}

// touch records activity at t for UpdatedAt.
func (pc *parsedChat) touch(t time.Time) {
	if t.After(pc.last) {
		pc.last = t
	}
}

// stepAgent splits "Start agent <type> step <n> (<id> - <prompt>)" into type and id.
func stepAgent(msg, prefix string) (typ, id string) {
	rest := strings.TrimPrefix(msg, prefix)
	i := strings.Index(rest, " step ")
	if i < 0 {
		return strings.TrimSpace(rest), ""
	}
	typ = strings.TrimSpace(rest[:i])
	if j := strings.IndexByte(rest[i:], '('); j >= 0 {
		idPart := rest[i+j+1:]
		if k := strings.IndexAny(idPart, " )"); k >= 0 {
			idPart = idPart[:k]
		}
		id = idPart
	}
	return typ, id
}

// logPrompts returns the prompts of a debug log (for message-history dedup).
// Only lines mentioning "[send-message]" are decoded.
func logPrompts(ctx context.Context, path string, into map[string]bool) error {
	marker := []byte(msgSendMessage)
	n := 0
	return fsread.Lines(path, func(line []byte) error {
		if n++; n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if !bytes.Contains(line, marker) {
			return nil
		}
		var l logLine
		var d sendMessageData
		if json.Unmarshal(line, &l) != nil || !strings.HasPrefix(l.Msg, msgSendMessage) || !decodeData(l.Data, &d) {
			return nil
		}
		if p := strings.TrimSpace(d.RunConfig.Prompt); p != "" {
			into[p] = true
		}
		return nil
	})
}

func decodeData(raw json.RawMessage, v any) bool {
	return len(raw) > 0 && json.Unmarshal(raw, v) == nil
}

// parseRFC3339 parses an ISO timestamp into UTC; zero when it is not one.
func parseRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
