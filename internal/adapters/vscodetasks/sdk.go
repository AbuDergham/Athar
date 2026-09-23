package vscodetasks

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/model"
)

// sdkManifest is <id>.json of a Cline SDK / CLI session (format taken from the
// Cline sources; no local session existed to verify it).
type sdkManifest struct {
	SessionID     string          `json:"session_id"`
	StartedAt     json.RawMessage `json:"started_at"`
	EndedAt       json.RawMessage `json:"ended_at"`
	UpdatedAt     json.RawMessage `json:"updated_at"`
	Cwd           string          `json:"cwd"`
	WorkspaceRoot string          `json:"workspace_root"`
	Prompt        string          `json:"prompt"`
	Title         string          `json:"title"`
	ParentID      string          `json:"parent_session_id"`
	IsSubagent    bool            `json:"is_subagent"`
	Metadata      struct {
		Title string `json:"title"`
	} `json:"metadata"`
}

// sdkMessage is one message of <id>.messages.json; the time field name varies.
type sdkMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Ts        json.RawMessage `json:"ts"`
	Timestamp json.RawMessage `json:"timestamp"`
	CreatedAt json.RawMessage `json:"created_at"`
}

// readSDK reads sessions\<id>\<id>.json and <id>.messages.json. Subagent
// sessions are skipped (they belong to their parent's conversation).
func (a *adapter) readSDK(src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	id := filepath.Base(src.Path)
	manifestPath := filepath.Join(src.Path, id+".json")
	msgsPath := filepath.Join(src.Path, id+".messages.json")
	newest, err := a.checkSettled(manifestPath, msgsPath)
	if err != nil {
		return res, err
	}
	var man sdkManifest
	found, err := readJSON(manifestPath, &man)
	if err != nil {
		return res, err
	}
	if !found || man.IsSubagent || strings.TrimSpace(man.ParentID) != "" {
		return res, nil
	}

	var raw json.RawMessage
	if _, err := readJSON(msgsPath, &raw); err != nil {
		return res, err
	}
	list, ok := sdkMessageList(raw)
	if !ok {
		res.Warnings = append(res.Warnings, filepath.Base(msgsPath)+": not a message list; format not recognized")
	}
	c := converter{}
	var times span
	for _, r := range list {
		var m sdkMessage
		if !decode(r, &m) || m.Role == "" {
			c.skipped++
			continue
		}
		ts := m.Ts
		if len(ts) == 0 {
			ts = m.Timestamp
		}
		if len(ts) == 0 {
			ts = m.CreatedAt
		}
		entry, _ := json.Marshal(apiMessage{Role: m.Role, Content: nonNil(m.Content), Ts: nonNil(ts)})
		c.message(entry)
	}
	res.Skipped += c.skipped
	msgs, coverage := c.msgs, model.CoverageFull
	if len(msgs) == 0 && strings.TrimSpace(man.Prompt) != "" {
		msgs = []model.Message{{Role: model.RoleUser, Kind: model.KindText, At: msTime(man.StartedAt), Text: strings.TrimSpace(man.Prompt)}}
		coverage = model.CoveragePrompts
	}
	if len(msgs) == 0 {
		return res, nil
	}
	for _, m := range msgs {
		times.add(m.At)
	}
	times.add(msTime(man.StartedAt))
	times.add(msTime(man.EndedAt))
	times.add(msTime(man.UpdatedAt))
	started, updated := times.first, times.last
	if s := msTime(man.StartedAt); !s.IsZero() {
		started = s
	}
	if updated.IsZero() {
		updated = newest.UTC()
	}
	if started.IsZero() || started.After(updated) {
		started = updated
	}

	title, titleSource := "", model.TitleNone
	if t := promptTitle(firstNonEmpty(man.Title, man.Metadata.Title)); t != "" {
		title, titleSource = t, model.TitleAgent
	} else if t := promptTitle(firstNonEmpty(man.Prompt, firstUserText(msgs))); t != "" {
		title, titleSource = t, model.TitlePrompt
	}
	nativeID := strings.TrimSpace(man.SessionID)
	if nativeID == "" {
		nativeID = id
	}
	cwd := firstNonEmpty(man.Cwd, man.WorkspaceRoot, c.cwd)

	res.Sessions = append(res.Sessions, model.Session{
		Agent:       a.f.id,
		NativeID:    nativeID,
		Cwd:         cwd,
		Title:       title,
		TitleSource: titleSource,
		StartedAt:   started,
		UpdatedAt:   updated,
		Coverage:    coverage,
		Origin:      model.OriginLive,
		Root:        src.Root,
		Messages:    msgs,
	})
	return res, nil
}

// sdkMessageList accepts [messages] or {"messages": [messages]}.
func sdkMessageList(raw json.RawMessage) ([]json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var list []json.RawMessage
	if raw[0] == '[' {
		err := json.Unmarshal(raw, &list)
		return list, err == nil
	}
	var obj struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if raw[0] == '{' && json.Unmarshal(raw, &obj) == nil && obj.Messages != nil {
		return obj.Messages, true
	}
	return nil, false
}

func nonNil(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}
