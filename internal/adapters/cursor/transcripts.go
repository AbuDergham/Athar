package cursor

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// transcript is one agent-transcripts file.
type transcript struct {
	id   string // file stem: the chat (composer / agent) id
	path string
	slug string // the ~\.cursor\projects folder name
}

// listTranscripts finds <projects>\<slug>\agent-transcripts\<id>.jsonl|.txt and
// <id>\<id>.jsonl|.txt. A .jsonl wins over a .txt of the same id; subagents
// folders are ignored.
func listTranscripts(projects string) []transcript {
	if projects == "" {
		return nil
	}
	slugs, err := os.ReadDir(projects)
	if err != nil {
		return nil
	}
	var out []transcript
	for _, s := range slugs {
		if !s.IsDir() {
			continue
		}
		dir := filepath.Join(projects, s.Name(), "agent-transcripts")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		index := map[string]int{} // id -> position in out, for this folder
		add := func(tid, p string) {
			if tid == "" {
				return
			}
			t := transcript{id: tid, path: p, slug: s.Name()}
			if i, ok := index[tid]; ok {
				if strings.EqualFold(filepath.Ext(out[i].path), ".txt") && strings.EqualFold(filepath.Ext(p), ".jsonl") {
					out[i] = t
				}
				return
			}
			index[tid] = len(out)
			out = append(out, t)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				for _, ext := range []string{".jsonl", ".txt"} {
					p := filepath.Join(dir, name, name+ext)
					if isFile(p) {
						add(name, p)
					}
				}
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			if ext := strings.ToLower(filepath.Ext(name)); ext == ".jsonl" || ext == ".txt" {
				add(strings.TrimSuffix(name, filepath.Ext(name)), filepath.Join(dir, name))
			}
		}
	}
	return out
}

// parseTranscript reads a .jsonl or .txt transcript.
func parseTranscript(path string) ([]model.Message, int, error) {
	if strings.EqualFold(filepath.Ext(path), ".txt") {
		data, err := fsread.ReadFile(path)
		if err != nil {
			return nil, 0, err
		}
		return parseTextTranscript(string(data)), 0, nil
	}
	var msgs []model.Message
	skipped := 0
	err := fsread.Lines(path, func(line []byte) error {
		ms, ok := transcriptRecord(line)
		if !ok {
			skipped++
			return nil
		}
		msgs = append(msgs, ms...)
		return nil
	})
	return msgs, skipped, err
}

// record is one JSONL transcript line.
type record struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Timestamp json.RawMessage `json:"timestamp"`
	Content   json.RawMessage `json:"content"`
	Message   *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// block is one element of a content array.
type block struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// transcriptRecord converts one JSONL line; ok is false for a line that is not JSON.
func transcriptRecord(line []byte) ([]model.Message, bool) {
	var r record
	if !decodeJSON(line, &r) {
		return nil, false
	}
	role := strings.ToLower(r.Role)
	content := r.Content
	if r.Message != nil {
		if role == "" {
			role = strings.ToLower(r.Message.Role)
		}
		if len(content) == 0 {
			content = r.Message.Content
		}
	}
	if role == "" {
		role = strings.ToLower(r.Type)
	}
	at := msTime(r.Timestamp)
	var out []model.Message
	addText := func(text string) {
		switch role {
		case "user", "human":
			q, t := cleanUserText(text)
			if !t.IsZero() {
				at = t
			}
			if q != "" {
				out = append(out, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: q})
			}
		case "assistant", "ai", "model":
			if s := strings.TrimSpace(text); s != "" {
				out = append(out, model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: at, Text: s})
			}
		case "tool":
			if s := strings.TrimSpace(text); s != "" {
				out = append(out, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, At: at, Text: clip(s, model.MaxToolOutput)})
			}
		}
	}
	content = bytes.TrimSpace(content)
	switch {
	case len(content) == 0 || bytes.Equal(content, []byte("null")):
	case content[0] == '"':
		addText(jsonStringOrRaw(content))
	case content[0] == '[':
		var blocks []block
		if json.Unmarshal(content, &blocks) != nil {
			return nil, false
		}
		for _, b := range blocks {
			switch b.Type {
			case "text", "":
				addText(b.Text)
			case "tool_use", "tool-call", "tool_call":
				out = append(out, model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at,
					Text: toolCallText(b.Name, jsonStringOrRaw(b.Input))})
			case "tool_result", "tool-result":
				if s := strings.TrimSpace(blockText(b.Content)); s != "" {
					out = append(out, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, At: at, Text: clip(s, model.MaxToolOutput)})
				}
			}
		}
	}
	return out, true
}

// blockText is the text of a tool_result content: a string or text blocks.
func blockText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var bs []block
		if json.Unmarshal(raw, &bs) == nil {
			var parts []string
			for _, b := range bs {
				if b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return jsonStringOrRaw(raw)
}

var (
	timestampTag = regexp.MustCompile(`(?s)<timestamp>(.*?)</timestamp>`)
	userQueryTag = regexp.MustCompile(`(?s)<user_query>(.*?)(?:</user_query>|$)`)
)

// cleanUserText keeps the <user_query> of a user turn (or the text without the
// <timestamp> prefix) and returns the timestamp when it can be read.
func cleanUserText(text string) (string, time.Time) {
	var at time.Time
	if m := timestampTag.FindStringSubmatch(text); m != nil {
		at, _ = parseTranscriptTime(m[1])
		text = strings.Replace(text, m[0], "", 1)
	}
	if m := userQueryTag.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	return strings.TrimSpace(text), at
}

var tzSuffix = regexp.MustCompile(`^(.*?)\s*\(\s*(?:UTC|GMT)\s*(?:([+-])(\d{1,2})(?::?(\d{2}))?)?\s*\)\s*$`)

// parseTranscriptTime reads "Monday, Dec 15, 2025, 9:30 AM (UTC+3)".
func parseTranscriptTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	offset := 0
	if m := tzSuffix.FindStringSubmatch(s); m != nil {
		s = m[1]
		if m[2] != "" {
			h, _ := strconv.Atoi(m[3])
			mins, _ := strconv.Atoi(m[4])
			offset = h*3600 + mins*60
			if m[2] == "-" {
				offset = -offset
			}
		}
	}
	loc := time.FixedZone("", offset)
	for _, layout := range []string{
		"Monday, Jan 2, 2006, 3:04 PM",
		"Monday, January 2, 2006, 3:04 PM",
		"Monday, Jan 2, 2006, 3:04:05 PM",
		"Jan 2, 2006, 3:04 PM",
	} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseTextTranscript reads the plain-text form: blocks that start with a
// "user:" or "assistant:" line.
func parseTextTranscript(data string) []model.Message {
	var out []model.Message
	role := ""
	var buf []string
	flush := func() {
		text := strings.Join(buf, "\n")
		buf = buf[:0]
		switch role {
		case "user":
			if q, at := cleanUserText(text); q != "" {
				out = append(out, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: q})
			}
		case "assistant":
			if s := strings.TrimSpace(text); s != "" {
				out = append(out, model.Message{Role: model.RoleAssistant, Kind: model.KindText, Text: s})
			}
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "user:":
			flush()
			role = "user"
			continue
		case "assistant:":
			flush()
			role = "assistant"
			continue
		}
		if role != "" {
			buf = append(buf, line)
		}
	}
	flush()
	return out
}
