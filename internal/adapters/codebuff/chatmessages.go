package codebuff

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// chatMessage is one element of chat-messages.json.
type chatMessage struct {
	ID        string      `json:"id"`
	Variant   string      `json:"variant"`
	Content   string      `json:"content"`
	Blocks    []chatBlock `json:"blocks"`
	Timestamp string      `json:"timestamp"`
}

// chatBlock is one content block of a chat message. Unknown block types are skipped.
type chatBlock struct {
	Type          string          `json:"type"`
	TextType      string          `json:"textType"`
	Content       string          `json:"content"`
	ToolName      string          `json:"toolName"`
	Input         json.RawMessage `json:"input"`
	Output        json.RawMessage `json:"output"`
	AgentType     string          `json:"agentType"`
	AgentName     string          `json:"agentName"`
	InitialPrompt string          `json:"initialPrompt"`
	Questions     []struct {
		Question string `json:"question"`
	} `json:"questions"`
}

// readChatMessages reads a chat folder's chat-messages.json (see the package
// doc), its run-state.json for the working folder and chat-meta.json for a
// title fallback. created is the chat folder's creation time (UTC, may be zero).
// A chat-messages.json that is not a JSON array is an error (it may be torn);
// an element that cannot be decoded is counted as skipped.
func readChatMessages(ctx context.Context, dir string, created time.Time) (parsedChat, error) {
	var pc parsedChat
	path := filepath.Join(dir, chatMessagesFile)
	data, err := fsread.ReadFile(path)
	if err != nil {
		return pc, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return pc, fmt.Errorf("%s: %w", path, err)
	}

	clk := newChatClock(created)
	for i, r := range raw {
		if i%256 == 255 {
			if err := ctx.Err(); err != nil {
				return pc, err
			}
		}
		var m chatMessage
		if json.Unmarshal(r, &m) != nil {
			pc.skipped++
			continue
		}
		at := clk.place(m.ID, m.Timestamp)
		switch m.Variant {
		case "user":
			if t := userText(m); t != "" {
				pc.add(model.RoleUser, model.KindText, at, t)
			}
		case "error":
			if t := strings.TrimSpace(m.Content); t != "" {
				pc.add(model.RoleSystem, model.KindText, at, truncate(t, model.MaxToolOutput))
			}
		case "ai":
			if strings.HasPrefix(m.ID, "sys-") {
				// CLI notices (mode switches, credits, ...), not model output.
				if t := strings.TrimSpace(m.Content); t != "" {
					pc.add(model.RoleSystem, model.KindText, at, t)
				}
				continue
			}
			if t := strings.TrimSpace(m.Content); t != "" {
				pc.add(model.RoleAssistant, model.KindText, at, t)
			}
			pc.addBlocks(at, m.Blocks)
		default:
			pc.skipped++
		}
	}

	var rs runState
	if b, err := fsread.ReadFile(filepath.Join(dir, runStateFile)); err == nil && json.Unmarshal(b, &rs) == nil {
		pc.cwd = rs.cwd()
	}
	var meta struct {
		FirstPrompt string `json:"firstPrompt"`
	}
	if b, err := fsread.ReadFile(filepath.Join(dir, chatMetaFile)); err == nil && json.Unmarshal(b, &meta) == nil {
		if meta.FirstPrompt != "(empty chat)" {
			pc.firstPrompt = meta.FirstPrompt
		}
	}
	return pc, nil
}

// userText is a user message's content, else its text blocks joined by blank lines.
func userText(m chatMessage) string {
	if t := strings.TrimSpace(m.Content); t != "" {
		return t
	}
	var parts []string
	for _, b := range m.Blocks {
		if b.Type == "text" && b.TextType != "reasoning" {
			if t := strings.TrimSpace(b.Content); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// addBlocks adds an assistant message's blocks: consecutive text blocks form
// one message; reasoning, mode dividers, images and unknown blocks are dropped;
// a subagent contributes only its spawn (tool call) and returned content.
func (pc *parsedChat) addBlocks(at time.Time, blocks []chatBlock) {
	var text []string
	flush := func() {
		if len(text) > 0 {
			pc.add(model.RoleAssistant, model.KindText, at, strings.Join(text, "\n\n"))
			text = nil
		}
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.TextType == "reasoning" {
				continue
			}
			if t := strings.TrimSpace(b.Content); t != "" {
				text = append(text, t)
			}
		case "tool":
			flush()
			pc.add(model.RoleAssistant, model.KindToolCall, at, toolCallText(b.ToolName, b.Input))
			if t := outputText(b.Output); t != "" {
				pc.add(model.RoleTool, model.KindToolOutput, at, t)
			}
		case "agent":
			flush()
			name := firstNonEmpty(b.AgentType, b.AgentName, "agent")
			call := name
			if p := strings.TrimSpace(b.InitialPrompt); p != "" {
				call = name + ": " + p
			}
			pc.add(model.RoleAssistant, model.KindToolCall, at, truncate(call, model.MaxToolOutput))
			if t := strings.TrimSpace(b.Content); t != "" {
				pc.add(model.RoleTool, model.KindToolOutput, at, truncate(t, model.MaxToolOutput))
			}
		case "plan":
			flush()
			if t := strings.TrimSpace(b.Content); t != "" {
				pc.add(model.RoleAssistant, model.KindText, at, t)
			}
		case "ask-user":
			flush()
			var qs []string
			for _, q := range b.Questions {
				if t := strings.TrimSpace(q.Question); t != "" {
					qs = append(qs, t)
				}
			}
			if len(qs) > 0 {
				pc.add(model.RoleAssistant, model.KindText, at, strings.Join(qs, "\n"))
			}
		}
	}
	flush()
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// chatClock turns chat-messages.json stamps into times. Ids like
// "user-<unix ms>" are exact; an ISO timestamp is exact; a local time of day
// ("07:23 PM") is placed on the local date of the previous message (starting
// at the chat folder's creation time), rolling over to the next day when it
// goes backwards.
type chatClock struct {
	cur time.Time // latest placed time, in localZone; zero when unknown
}

func newChatClock(created time.Time) *chatClock {
	c := &chatClock{}
	if !created.IsZero() {
		c.cur = created.In(localZone)
	}
	return c
}

// place returns the UTC time of a message, or zero when it cannot be known.
func (c *chatClock) place(id, stamp string) time.Time {
	if t := idTime(id); !t.IsZero() {
		c.cur = t.In(localZone)
		return t
	}
	if t := parseRFC3339(stamp); !t.IsZero() {
		c.cur = t.In(localZone)
		return t
	}
	minute, ok := parseTimeOfDay(stamp)
	if !ok || c.cur.IsZero() {
		return time.Time{}
	}
	y, mo, d := c.cur.Date()
	t := time.Date(y, mo, d, minute/60, minute%60, 0, 0, localZone)
	if t.Before(c.cur.Truncate(time.Minute)) {
		t = time.Date(y, mo, d+1, minute/60, minute%60, 0, 0, localZone)
	}
	if t.Before(c.cur) { // same minute as an exact stamp: keep the order
		t = c.cur
	}
	c.cur = t
	return t.UTC()
}

// idTime reads the Unix-millisecond part of ids like "user-1782892830000" or
// "ai-1782892830000-9f3a"; zero when there is none.
func idTime(id string) time.Time {
	parts := strings.Split(id, "-")
	for _, p := range parts[min(1, len(parts)):] {
		if len(p) != 13 || strings.TrimLeft(p, "0123456789") != "" {
			continue
		}
		ms, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			continue
		}
		t := time.UnixMilli(ms).UTC()
		if t.Year() < 2020 || t.Year() > 2100 {
			continue
		}
		return t
	}
	return time.Time{}
}

// parseTimeOfDay parses a local time of day as written by
// toLocaleTimeString ("07:23 PM", "7:23 pm", "19:23", "11:05 a.m.",
// "07:23:45 PM", Arabic "٠٧:٢٣ م") into minutes after midnight.
func parseTimeOfDay(s string) (int, bool) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r == '.' || unicode.Is(unicode.Cf, r): // "a.m.", bidi marks
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	str := strings.TrimSpace(b.String())

	i := 0
	num := func() (int, bool) {
		start := i
		for i < len(str) && str[i] >= '0' && str[i] <= '9' {
			i++
		}
		if i == start || i-start > 2 {
			return 0, false
		}
		n, _ := strconv.Atoi(str[start:i])
		return n, true
	}
	h, ok := num()
	if !ok || i >= len(str) || str[i] != ':' {
		return 0, false
	}
	i++
	m, ok := num()
	if !ok || m > 59 {
		return 0, false
	}
	if i < len(str) && str[i] == ':' { // seconds
		i++
		if sec, ok := num(); !ok || sec > 59 {
			return 0, false
		}
	}
	switch strings.TrimSpace(str[i:]) {
	case "":
		if h > 23 {
			return 0, false
		}
	case "am", "ص":
		if h < 1 || h > 12 {
			return 0, false
		}
		h %= 12
	case "pm", "م":
		if h < 1 || h > 12 {
			return 0, false
		}
		h = h%12 + 12
	default:
		return 0, false
	}
	return h*60 + m, true
}
