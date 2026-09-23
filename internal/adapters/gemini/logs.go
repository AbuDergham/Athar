package gemini

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// logEntry is one element of logs.json (written by older CLI versions):
// {sessionId, messageId, type: "user", message, timestamp}.
type logEntry struct {
	SessionID json.RawMessage `json:"sessionId"`
	Type      json.RawMessage `json:"type"`
	Message   json.RawMessage `json:"message"`
	Timestamp json.RawMessage `json:"timestamp"`
}

// readLogs turns a prompt log into one prompt-log session per sessionId.
// The whole file is one JSON array, so a file that does not parse (torn
// write) fails the source and is read again on the next sync.
func readLogs(path, cwd, root string) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	b, err := fsread.ReadFile(path)
	if err != nil {
		return res, err
	}
	b = bytes.TrimSpace(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
	if len(b) == 0 {
		return res, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(b, &entries); err != nil {
		return res, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}

	var order []string
	byID := map[string]*model.Session{}
	for _, raw := range entries {
		var e logEntry
		if json.Unmarshal(raw, &e) != nil {
			res.Skipped++
			continue
		}
		sid, ok := jsonString(e.SessionID)
		if !ok || strings.TrimSpace(sid) == "" {
			res.Skipped++
			continue
		}
		if typ, _ := jsonString(e.Type); typ != "" && typ != "user" {
			continue // only user prompts are logged; anything else is not a prompt
		}
		s := byID[sid]
		if s == nil {
			s = &model.Session{
				Agent:       agentID,
				NativeID:    sid,
				Cwd:         cwd,
				TitleSource: model.TitleNone,
				Coverage:    model.CoveragePrompts,
				Origin:      model.OriginPromptLog,
				Root:        root,
			}
			byID[sid] = s
			order = append(order, sid)
		}
		tsStr, _ := jsonString(e.Timestamp)
		at := parseTime(tsStr)
		if !at.IsZero() {
			if s.StartedAt.IsZero() || at.Before(s.StartedAt) {
				s.StartedAt = at
			}
			if at.After(s.UpdatedAt) {
				s.UpdatedAt = at
			}
		}
		msg, _ := jsonString(e.Message)
		text := strings.TrimSpace(msg)
		if ignoredUser(text) {
			continue
		}
		s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: text})
		if s.TitleSource == model.TitleNone {
			s.Title, s.TitleSource = promptTitle(text), model.TitlePrompt
		}
	}
	for _, sid := range order {
		s := byID[sid]
		if len(s.Messages) == 0 {
			continue
		}
		if s.StartedAt.IsZero() {
			if _, mtime, err := fsread.Stat(path); err == nil {
				s.StartedAt, s.UpdatedAt = mtime.UTC(), mtime.UTC()
			}
		}
		res.Sessions = append(res.Sessions, *s)
	}
	return res, nil
}
