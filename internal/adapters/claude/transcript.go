package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// ctxCheckEvery is how many lines are parsed between cancellation checks.
const ctxCheckEvery = 256

// toolArgKeys are the tool_use input fields shown after the tool name, in order.
var toolArgKeys = []string{"command", "file_path", "pattern"}

// record is the subset of a transcript line Athar uses. Unknown record types
// and fields are ignored; Claude Code adds new ones often.
type record struct {
	Type             string `json:"type"`
	Timestamp        string `json:"timestamp"`
	Cwd              string `json:"cwd"`
	Entrypoint       string `json:"entrypoint"`
	IsMeta           bool   `json:"isMeta"`
	IsSidechain      bool   `json:"isSidechain"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Message          struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Attachment  json.RawMessage `json:"attachment"`
	AITitle     string          `json:"aiTitle"`
	CustomTitle string          `json:"customTitle"`
	AgentName   string          `json:"agentName"`
	Summary     string          `json:"summary"`
}

// block is one element of message.content (or of a tool_result's content).
type block struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// attachment is the part of an attachment record used for queued prompts
// (prompts typed while Claude was busy; they are not stored as user records).
type attachment struct {
	Type        string          `json:"type"`
	CommandMode string          `json:"commandMode"`
	Prompt      json.RawMessage `json:"prompt"`
	Origin      struct {
		Kind string `json:"kind"`
	} `json:"origin"`
}

// transcript accumulates one session while its lines are parsed.
type transcript struct {
	cwd         string
	entrypoint  string
	userTitle   string // last custom-title / agent-name
	agentTitle  string // last ai-title / summary
	firstPrompt string
	times       span
	msgs        []model.Message
	skipped     int
}

// readTranscript parses <root>\projects\<dir>\<uuid>.jsonl into one session.
// Headless (sdk-*) sessions and sessions without any message yield nothing.
func readTranscript(ctx context.Context, src adapters.Source) (adapters.ReadResult, error) {
	var t transcript
	n := 0
	err := fsread.Lines(src.Path, func(line []byte) error {
		n++
		if n%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		t.line(line)
		return nil
	})
	if err != nil {
		return adapters.ReadResult{}, err
	}
	res := adapters.ReadResult{Skipped: t.skipped}
	if strings.HasPrefix(t.entrypoint, "sdk-") || len(t.msgs) == 0 {
		return res, nil
	}

	s := model.Session{
		Agent:     "claude",
		NativeID:  strings.TrimSuffix(filepath.Base(src.Path), filepath.Ext(src.Path)),
		Cwd:       t.cwd,
		StartedAt: t.times.first,
		UpdatedAt: t.times.last,
		Coverage:  model.CoverageFull,
		Origin:    model.OriginLive,
		Root:      src.Root,
		Messages:  t.msgs,
	}
	switch {
	case t.userTitle != "":
		s.Title, s.TitleSource = t.userTitle, model.TitleUser
	case t.agentTitle != "":
		s.Title, s.TitleSource = t.agentTitle, model.TitleAgent
	case t.firstPrompt != "":
		s.Title, s.TitleSource = promptTitle(t.firstPrompt), model.TitlePrompt
	default:
		s.TitleSource = model.TitleNone
	}
	if s.StartedAt.IsZero() {
		if fi, err := os.Stat(src.Path); err == nil {
			s.StartedAt = fi.ModTime().UTC()
			s.UpdatedAt = s.StartedAt
		}
	}
	res.Sessions = []model.Session{s}
	return res, nil
}

// line handles one JSONL record.
func (t *transcript) line(b []byte) {
	var r record
	if !decode(b, &r) {
		t.skipped++
		return
	}
	at := parseTime(r.Timestamp)
	t.times.add(at)
	if t.cwd == "" && r.Cwd != "" {
		t.cwd = r.Cwd
	}
	if t.entrypoint == "" && r.Entrypoint != "" {
		t.entrypoint = r.Entrypoint
	}
	switch r.Type {
	case "user":
		if !r.IsSidechain && !r.IsMeta && !r.IsCompactSummary {
			t.user(r.Message.Content, at)
		}
	case "assistant":
		if !r.IsSidechain {
			t.assistant(r.Message.Content, at)
		}
	case "attachment":
		if !r.IsSidechain {
			t.queued(r.Attachment, at)
		}
	case "custom-title":
		t.setUserTitle(r.CustomTitle)
	case "agent-name":
		t.setUserTitle(r.AgentName)
	case "ai-title":
		t.setAgentTitle(r.AITitle)
	case "summary":
		t.setAgentTitle(r.Summary)
	}
}

func (t *transcript) setUserTitle(s string) {
	if s = cleanTitle(s); s != "" {
		t.userTitle = s
	}
}

func (t *transcript) setAgentTitle(s string) {
	if s = cleanTitle(s); s != "" {
		t.agentTitle = s
	}
}

func (t *transcript) add(role model.Role, kind model.Kind, at time.Time, text string) {
	t.msgs = append(t.msgs, model.Message{Role: role, Kind: kind, At: at, Text: text})
}

// prompt collects the text parts of one user message, wrappers removed.
type prompt struct {
	parts []string
	plain bool // at least one part is a typed prompt (not a "!" shell escape)
}

func (p *prompt) add(raw string) {
	if text, isShell, ok := userText(raw); ok {
		p.parts = append(p.parts, text)
		p.plain = p.plain || !isShell
	}
}

// promptContent reads a prompt given as a JSON string or as content blocks
// (non-text blocks such as tool_result and image are ignored here).
func promptContent(content json.RawMessage) prompt {
	var p prompt
	if len(content) == 0 {
		return p
	}
	if content[0] == '"' {
		var s string
		if json.Unmarshal(content, &s) == nil {
			p.add(s)
		}
		return p
	}
	var blocks []block
	if decode(content, &blocks) {
		for _, b := range blocks {
			if b.Type == "text" {
				p.add(b.Text)
			}
		}
	}
	return p
}

// addPrompt adds a user message and remembers the first typed prompt for the title.
func (t *transcript) addPrompt(p prompt, at time.Time) {
	if len(p.parts) == 0 {
		return
	}
	text := strings.Join(p.parts, "\n\n")
	t.add(model.RoleUser, model.KindText, at, text)
	if t.firstPrompt == "" && p.plain {
		t.firstPrompt = text
	}
}

// user handles a user record: tool results and/or a prompt (string or text blocks).
func (t *transcript) user(content json.RawMessage, at time.Time) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return
	}
	if content[0] == '[' {
		var blocks []block
		if decode(content, &blocks) {
			for _, b := range blocks {
				if b.Type == "tool_result" {
					t.toolResult(b.Content, at)
				}
			}
		}
	}
	t.addPrompt(promptContent(content), at)
}

// assistant handles an assistant record: text and tool calls; thinking is dropped.
func (t *transcript) assistant(content json.RawMessage, at time.Time) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return
	}
	if content[0] == '"' {
		var s string
		if json.Unmarshal(content, &s) == nil && strings.TrimSpace(s) != "" {
			t.add(model.RoleAssistant, model.KindText, at, strings.TrimSpace(s))
		}
		return
	}
	var blocks []block
	if !decode(content, &blocks) {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if s := strings.TrimSpace(b.Text); s != "" {
				t.add(model.RoleAssistant, model.KindText, at, s)
			}
		case "tool_use", "server_tool_use":
			t.add(model.RoleAssistant, model.KindToolCall, at, toolCallText(b.Name, b.Input))
		}
	}
}

// toolResult keeps the first model.MaxToolOutput bytes of a tool_result's text.
func (t *transcript) toolResult(content json.RawMessage, at time.Time) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return
	}
	var text string
	switch content[0] {
	case '"':
		if json.Unmarshal(content, &text) != nil {
			return
		}
	case '[':
		var parts []block
		if !decode(content, &parts) {
			return
		}
		var sb strings.Builder
		for _, p := range parts {
			if p.Type != "text" || p.Text == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(p.Text)
			if sb.Len() >= model.MaxToolOutput {
				break
			}
		}
		text = sb.String()
	default:
		return
	}
	if text = strings.TrimSpace(text); text != "" {
		t.add(model.RoleTool, model.KindToolOutput, at, truncate(text, model.MaxToolOutput))
	}
}

// queued turns a queued_command attachment typed by the user into a prompt.
func (t *transcript) queued(raw json.RawMessage, at time.Time) {
	var a attachment
	if !decode(raw, &a) || a.Type != "queued_command" || a.CommandMode != "prompt" {
		return
	}
	if a.Origin.Kind != "" && a.Origin.Kind != "human" {
		return
	}
	t.addPrompt(promptContent(bytes.TrimSpace(a.Prompt)), at)
}

// toolCallText is "Name: <command | file_path | pattern | first 200 chars of
// the input JSON>", at most model.MaxToolOutput bytes.
func toolCallText(name string, input json.RawMessage) string {
	if name == "" {
		name = "tool"
	}
	arg := ""
	var fields map[string]json.RawMessage
	if decode(input, &fields) {
		for _, k := range toolArgKeys {
			var s string
			if json.Unmarshal(fields[k], &s) == nil && strings.TrimSpace(s) != "" {
				arg = strings.TrimSpace(s)
				break
			}
		}
	}
	if arg == "" && len(input) > 0 {
		var buf bytes.Buffer
		if json.Compact(&buf, input) == nil {
			if js := buf.String(); js != "{}" && js != "null" && js != `""` {
				arg = firstRunes(js, 200)
			}
		}
	}
	if arg == "" {
		return name
	}
	return truncate(name+": "+arg, model.MaxToolOutput)
}
