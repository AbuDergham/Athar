package cursor

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// Bubble types in composerData headers and bubbles.
const (
	bubbleUser      = 1
	bubbleAssistant = 2
)

// composer is the part of a composerData:<id> value that Athar uses.
type composer struct {
	ComposerID          string                     `json:"composerId"`
	Name                string                     `json:"name"`
	CreatedAt           json.RawMessage            `json:"createdAt"`
	LastUpdatedAt       json.RawMessage            `json:"lastUpdatedAt"`
	Headers             []header                   `json:"fullConversationHeadersOnly"`
	Conversation        []json.RawMessage          `json:"conversation"`
	ConversationMap     map[string]json.RawMessage `json:"conversationMap"`
	WorkspaceIdentifier *struct {
		URI *struct {
			FsPath   string `json:"fsPath"`
			External string `json:"external"`
			Path     string `json:"path"`
			Scheme   string `json:"scheme"`
		} `json:"uri"`
	} `json:"workspaceIdentifier"`
}

type header struct {
	BubbleID string `json:"bubbleId"`
	Type     int    `json:"type"`
}

// workspacePath is the project folder recorded on the chat itself (newest builds).
func (c composer) workspacePath() string {
	if c.WorkspaceIdentifier == nil || c.WorkspaceIdentifier.URI == nil {
		return ""
	}
	u := c.WorkspaceIdentifier.URI
	if p := strings.TrimSpace(u.FsPath); p != "" {
		return upperDrive(p)
	}
	if p := fileURIPath(u.External); p != "" {
		return p
	}
	return ""
}

// bubble is one message: a bubbleId row or an element of conversation[].
type bubble struct {
	Type       int             `json:"type"`
	Text       string          `json:"text"`
	CreatedAt  json.RawMessage `json:"createdAt"`
	TimingInfo *struct {
		ClientStartTime float64 `json:"clientStartTime"`
		ClientEndTime   float64 `json:"clientEndTime"`
	} `json:"timingInfo"`
	ToolFormerData *toolFormer `json:"toolFormerData"`
	WorkspaceURIs  []string    `json:"workspaceUris"`
}

// toolFormer is a tool call made by the agent and its result.
type toolFormer struct {
	Name    string          `json:"name"`
	RawArgs json.RawMessage `json:"rawArgs"` // a JSON string holding a JSON object
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"` // a JSON string, usually holding a JSON object
	Status  string          `json:"status"`
	Error   json.RawMessage `json:"error"`
}

func decodeComposer(data []byte) (composer, bool) {
	var c composer
	if !decodeJSON(data, &c) {
		return composer{}, false
	}
	return c, true
}

// decodeBubble fails for a missing (nil), NULL or torn value.
func decodeBubble(data []byte) (bubble, bool) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return bubble{}, false
	}
	var b bubble
	if !decodeJSON(data, &b) {
		return bubble{}, false
	}
	return b, true
}

// at is the bubble's time: createdAt, else the end of its generation.
func (b bubble) at() time.Time {
	if t := msTime(b.CreatedAt); !t.IsZero() {
		return t
	}
	if b.TimingInfo != nil {
		for _, v := range []float64{b.TimingInfo.ClientEndTime, b.TimingInfo.ClientStartTime} {
			if v > 0 {
				return epochTime(int64(v))
			}
		}
	}
	return time.Time{}
}

// bubbleMessages converts bubbles in order. Empty bubbles (thinking,
// placeholders) are dropped. A user bubble without a time (inline
// conversation[] bubbles) takes the time its reply was requested.
func bubbleMessages(bs []bubble) []model.Message {
	var out []model.Message
	for i, b := range bs {
		at := b.at()
		if at.IsZero() && b.Type == bubbleUser && i+1 < len(bs) {
			if next := bs[i+1].TimingInfo; next != nil && bs[i+1].Type == bubbleAssistant && next.ClientStartTime > 0 {
				at = epochTime(int64(next.ClientStartTime))
			}
		}
		text := strings.TrimSpace(b.Text)
		switch b.Type {
		case bubbleUser:
			if text != "" {
				out = append(out, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: text})
			}
		case bubbleAssistant:
			if text != "" {
				out = append(out, model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: at, Text: text})
			}
			if tf := b.ToolFormerData; tf != nil && (tf.Name != "" || len(tf.RawArgs) > 0) {
				out = append(out, model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at,
					Text: toolCallText(tf.Name, jsonStringOrRaw(tf.RawArgs), jsonStringOrRaw(tf.Params))})
				if o := tf.output(); o != "" {
					out = append(out, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, At: at, Text: o})
				}
			}
		}
	}
	return out
}

// output is the tool's result text (at most MaxToolOutput bytes).
func (tf *toolFormer) output() string {
	s := strings.TrimSpace(resultText(jsonStringOrRaw(tf.Result)))
	if s == "" {
		s = strings.TrimSpace(resultText(jsonStringOrRaw(tf.Error)))
	}
	return clip(s, model.MaxToolOutput)
}

// resultText extracts the readable part of a tool result: the output /
// contents string of a JSON object, a JSON string, or the text as is.
func resultText(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "{") {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(t), &m) == nil {
			for _, k := range []string{"output", "contents", "content", "stdout", "result", "text", "message",
				"clientVisibleErrorMessage", "errorMessage", "error"} {
				if v := jsonStringOrRaw(m[k]); v != "" && !strings.HasPrefix(strings.TrimSpace(v), "{") {
					return v
				}
			}
			return ""
		}
	}
	return s
}

// toolArgKeys are the arguments that best describe a tool call, in order.
var toolArgKeys = []string{
	"command", "target_file", "file_path", "targetFile", "path", "relative_workspace_path",
	"target_directory", "query", "pattern", "search_term", "url", "glob_pattern",
}

// toolCallText is "name: <command, file or query>" (or the first 200
// characters of the arguments).
func toolCallText(name string, args ...string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	first := ""
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if first == "" {
			first = a
		}
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(a), &m) != nil {
			continue
		}
		for _, k := range toolArgKeys {
			if v := strings.TrimSpace(jsonStringOrRaw(m[k])); v != "" && !strings.HasPrefix(v, "{") && !strings.HasPrefix(v, "[") {
				return name + ": " + v
			}
		}
	}
	if first == "" {
		return name
	}
	return name + ": " + cutRunes(strings.Join(strings.Fields(first), " "), 200)
}

// jsonStringOrRaw returns the value of a JSON string, or the raw JSON of any
// other value ("" for null or missing).
func jsonStringOrRaw(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

// msTime reads an epoch-ms (or s) number, a numeric string or an RFC 3339 string.
func msTime(raw json.RawMessage) time.Time {
	s := strings.TrimSpace(jsonStringOrRaw(raw))
	if s == "" {
		return time.Time{}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return epochTime(int64(f))
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// epochTime reads milliseconds (or seconds for small values). Zero for n <= 0.
func epochTime(n int64) time.Time {
	switch {
	case n <= 0:
		return time.Time{}
	case n >= 100_000_000_000: // ms; as seconds it would be year 5138+
		return time.UnixMilli(n).UTC()
	default:
		return time.Unix(n, 0).UTC()
	}
}

// promptTitleOf is the first user prompt as a title.
func promptTitleOf(msgs []model.Message) (string, model.TitleSource) {
	for _, m := range msgs {
		if m.Role == model.RoleUser && m.Kind == model.KindText {
			if t := cutRunes(strings.Join(strings.Fields(m.Text), " "), 100); t != "" {
				return t, model.TitlePrompt
			}
		}
	}
	return "", model.TitleNone
}

// cutRunes keeps the first n runes of s.
func cutRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
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
