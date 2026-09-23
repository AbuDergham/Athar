package neovate

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

const (
	// interruptedMarker is the user message Neovate logs when a turn is cancelled.
	interruptedMarker = "[Request interrupted by user]"
	// compactedUI is the uiContent of the summary message written by compaction.
	compactedUI = "Chat history compacted successfully."
	// maxPromptTitle is the number of runes of the first prompt used as a title.
	maxPromptTitle = 100
	// maxToolArgs is the number of runes of raw tool input kept in a tool call.
	maxToolArgs = 200
)

// record is one line of a session file. Fields that vary in type are raw so
// one odd field never makes the whole line unreadable.
type record struct {
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	Config     json.RawMessage `json:"config"`
	Content    json.RawMessage `json:"content"`
	Text       json.RawMessage `json:"text"`
	Timestamp  json.RawMessage `json:"timestamp"`
	ParentUUID json.RawMessage `json:"parentUuid"`
	UIContent  json.RawMessage `json:"uiContent"`
	Hidden     json.RawMessage `json:"hidden"`
	Cwd        json.RawMessage `json:"cwd"`
}

// parseSession reads one session file. It returns nil when the file holds no
// message worth archiving. skipped counts lines that are not valid records.
func parseSession(path, id string) (s *model.Session, skipped int, err error) {
	var (
		msgs          []model.Message
		summary       string
		cwd           string
		first, last   time.Time
		seenMessage   bool
		haveTimestamp bool
	)
	err = fsread.Lines(path, func(line []byte) error {
		var r record
		if json.Unmarshal(line, &r) != nil {
			skipped++
			return nil
		}
		if cwd == "" {
			if c := strings.TrimSpace(jsonString(r.Cwd)); c != "" {
				cwd = c
			}
		}
		switch {
		case r.Type == "config":
			var cfg struct {
				Summary json.RawMessage `json:"summary"`
			}
			if json.Unmarshal(r.Config, &cfg) == nil {
				if t := collapseSpace(jsonString(cfg.Summary)); t != "" {
					summary = t
				}
			}
			return nil
		case r.Type == "message", r.Type == "" && r.Role != "":
		default:
			return nil // other record kinds carry no conversation text
		}
		at, ok := parseTime(r.Timestamp)
		if ok {
			if !haveTimestamp || at.Before(first) {
				first = at
			}
			if !haveTimestamp || at.After(last) {
				last = at
			}
			haveTimestamp = true
		}
		isFirst := !seenMessage
		seenMessage = true
		out, ok := messagesOf(r, at, isFirst)
		if !ok {
			skipped++
			return nil
		}
		msgs = append(msgs, out...)
		return nil
	})
	if err != nil {
		return nil, skipped, err
	}
	if len(msgs) == 0 {
		return nil, skipped, nil
	}
	if !haveTimestamp {
		// No record has a usable time: fall back to the file's mtime.
		if _, mt, serr := fsread.Stat(path); serr == nil {
			first, last = mt.UTC(), mt.UTC()
		}
	}
	s = &model.Session{
		Agent:       ID,
		NativeID:    id,
		Cwd:         cwd,
		TitleSource: model.TitleNone,
		StartedAt:   first,
		UpdatedAt:   last,
		Coverage:    model.CoverageFull,
		Origin:      model.OriginLive,
		Messages:    msgs,
	}
	switch {
	case summary != "":
		s.Title, s.TitleSource = summary, model.TitleAgent
	default:
		for _, m := range msgs {
			if m.Role == model.RoleUser && m.Kind == model.KindText {
				if t := truncateRunes(collapseSpace(m.Text), maxPromptTitle); t != "" {
					s.Title, s.TitleSource = t, model.TitlePrompt
					break
				}
			}
		}
	}
	return s, skipped, nil
}

// messagesOf turns one message record into archive messages. ok is false
// when the content cannot be decoded at all.
func messagesOf(r record, at time.Time, isFirst bool) (out []model.Message, ok bool) {
	var role model.Role
	switch r.Role {
	case "user":
		role = model.RoleUser
		if bytes.Equal(bytes.TrimSpace(r.Hidden), []byte("true")) {
			return nil, true
		}
		if jsonString(r.UIContent) == compactedUI {
			return nil, true
		}
	case "assistant":
		role = model.RoleAssistant
	case "tool":
		role = model.RoleTool
	default:
		return nil, true // system prompts and unknown roles are not conversation text
	}

	str, parts, ok := decodeContent(r.Content)
	if !ok {
		return nil, false
	}
	if role == model.RoleUser && !isFirst && isNull(r.ParentUUID) && len(parts) == 1 && partType(parts[0]) == "text" {
		return nil, true // compaction summary written without its uiContent marker
	}

	emitText := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		if role == model.RoleUser && strings.TrimSpace(text) == interruptedMarker {
			return
		}
		kind := model.KindText
		if role == model.RoleTool {
			kind = model.KindToolOutput
			text = truncateBytes(text, model.MaxToolOutput)
		}
		out = append(out, model.Message{Role: role, Kind: kind, At: at, Text: text})
	}

	if parts == nil {
		if strings.TrimSpace(str) == "" && role == model.RoleAssistant {
			str = jsonString(r.Text) // assistant text kept outside content
		}
		emitText(str)
		return out, true
	}

	var texts []string
	hasTool := false
	flush := func() {
		if len(texts) > 0 {
			emitText(strings.Join(texts, "\n"))
			texts = nil
		}
	}
	for _, p := range parts {
		switch partType(p) {
		case "text":
			if t := jsonString(p["text"]); strings.TrimSpace(t) != "" {
				texts = append(texts, t)
			}
		case "tool_use":
			hasTool = true
			flush()
			out = append(out, model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at, Text: toolCall(p)})
		case "tool_result":
			hasTool = true
			flush()
			if t := toolOutput(p["result"]); strings.TrimSpace(t) != "" {
				out = append(out, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, At: at, Text: truncateBytes(t, model.MaxToolOutput)})
			}
		default:
			// reasoning, image and unknown parts are not archived
		}
	}
	flush()
	if len(out) == 0 && !hasTool && role == model.RoleAssistant {
		emitText(jsonString(r.Text))
	}
	return out, true
}

// decodeContent splits content into a plain string or a list of parts.
// Missing or null content is an empty string.
func decodeContent(raw json.RawMessage) (str string, parts []map[string]json.RawMessage, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isNull(raw) {
		return "", nil, true
	}
	switch raw[0] {
	case '"':
		if json.Unmarshal(raw, &str) != nil {
			return "", nil, false
		}
		return str, nil, true
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return "", nil, false
		}
		parts = make([]map[string]json.RawMessage, 0, len(items))
		for _, it := range items {
			var p map[string]json.RawMessage
			if json.Unmarshal(it, &p) == nil && p != nil {
				parts = append(parts, p)
			}
		}
		return "", parts, true
	default:
		return "", nil, false
	}
}

func partType(p map[string]json.RawMessage) string { return jsonString(p["type"]) }

// toolCall renders a tool_use part as "name: <command | file path | pattern | ...>".
func toolCall(p map[string]json.RawMessage) string {
	name := strings.TrimSpace(jsonString(p["name"]))
	if name == "" {
		name = "tool"
	}
	var input map[string]json.RawMessage
	_ = json.Unmarshal(p["input"], &input)
	for _, k := range []string{"command", "file_path", "pattern", "path", "dir_path", "url", "query"} {
		if v := strings.TrimSpace(jsonString(input[k])); v != "" {
			return name + ": " + v
		}
	}
	if d := strings.TrimSpace(jsonString(p["description"])); d != "" {
		return name + ": " + d
	}
	raw := bytes.TrimSpace(p["input"])
	if len(raw) == 0 || isNull(raw) || string(raw) == "{}" {
		return name
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) == nil {
		raw = buf.Bytes()
	}
	return name + ": " + truncateRunes(string(raw), maxToolArgs)
}

// toolOutput extracts the text of a tool result: result.llmContent as a string
// or text parts; falls back to a string result or result.returnDisplay.
func toolOutput(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isNull(raw) {
		return ""
	}
	if raw[0] == '"' {
		return jsonString(raw)
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(raw, &res) != nil {
		return ""
	}
	if str, parts, ok := decodeContent(res["llmContent"]); ok {
		if parts == nil && str != "" {
			return str
		}
		var texts []string
		for _, p := range parts {
			if partType(p) == "text" {
				if t := jsonString(p["text"]); t != "" {
					texts = append(texts, t)
				}
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return jsonString(res["returnDisplay"])
}

// parseTime reads an ISO-8601 string or an epoch number (ms, or s when small).
func parseTime(raw json.RawMessage) (time.Time, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isNull(raw) {
		return time.Time{}, false
	}
	if raw[0] == '"' {
		s := strings.TrimSpace(jsonString(raw))
		if s == "" {
			return time.Time{}, false
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC(), true
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return epoch(n)
		}
		return time.Time{}, false
	}
	n, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return time.Time{}, false
	}
	return epoch(n)
}

func epoch(n float64) (time.Time, bool) {
	if n <= 0 {
		return time.Time{}, false
	}
	if n < 1e11 { // seconds
		return time.UnixMilli(int64(n * 1000)).UTC(), true
	}
	return time.UnixMilli(int64(n)).UTC(), true
}

// jsonString decodes raw as a JSON string; anything else yields "".
func jsonString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// collapseSpace joins the whitespace-separated fields of s with single spaces.
func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncateRunes keeps at most n runes of s.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return strings.TrimSpace(s[:pos])
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
