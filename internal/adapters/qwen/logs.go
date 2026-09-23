package qwen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// logEntry is one element of tmp/<hash>/logs.json (Qwen's Logger).
type logEntry struct {
	SessionID *string `json:"sessionId"`
	Type      *string `json:"type"` // "user"; newer versions also log "model_switch"
	Message   *string `json:"message"`
	Timestamp *string `json:"timestamp"`
}

// readLogs reads a prompt log: every prompt the user typed in one project
// folder, grouped by session id into prompt-log sessions.
func readLogs(env adapters.Env, src adapters.Source, base string) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	data, err := fsread.ReadFile(src.Path)
	if err != nil {
		return res, err
	}
	data = bytes.TrimSpace(bytes.TrimPrefix(data, utf8BOM))
	if len(data) == 0 {
		return res, nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		// Torn or corrupt: fail the source so the next sync reads it again.
		return res, fmt.Errorf("prompt log %s: %w", filepath.Base(src.Path), err)
	}
	bySession := map[string]*model.Session{}
	for _, raw := range raws {
		var e logEntry
		if json.Unmarshal(raw, &e) != nil || e.SessionID == nil || *e.SessionID == "" ||
			e.Type == nil || e.Message == nil || e.Timestamp == nil {
			res.Skipped++
			continue
		}
		if *e.Type != "user" {
			continue
		}
		at, ok := parseTime(*e.Timestamp)
		if !ok {
			res.Skipped++
			continue
		}
		s := bySession[*e.SessionID]
		if s == nil {
			s = &model.Session{
				Agent:    "qwen",
				NativeID: *e.SessionID,
				Coverage: model.CoveragePrompts,
				Origin:   model.OriginPromptLog,
				Root:     src.Root,
			}
			bySession[*e.SessionID] = s
		}
		s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: *e.Message})
	}
	if len(bySession) == 0 {
		return res, nil
	}
	cwd := resolveHash(env, base, filepath.Base(filepath.Dir(src.Path)))
	for _, s := range bySession {
		sort.SliceStable(s.Messages, func(i, j int) bool { return s.Messages[i].At.Before(s.Messages[j].At) })
		s.Cwd = cwd
		s.StartedAt = s.Messages[0].At
		s.UpdatedAt = s.Messages[len(s.Messages)-1].At
		s.Title, s.TitleSource = promptLogTitle(s.Messages)
		res.Sessions = append(res.Sessions, *s)
	}
	sort.Slice(res.Sessions, func(i, j int) bool { return res.Sessions[i].NativeID < res.Sessions[j].NativeID })
	return res, nil
}

// promptLogTitle is the first prompt that is not a slash command (/auth,
// /quit, …), else the first prompt.
func promptLogTitle(msgs []model.Message) (string, model.TitleSource) {
	for _, m := range msgs {
		if t := titleFrom(m.Text); t != "" && !strings.HasPrefix(t, "/") {
			return t, model.TitlePrompt
		}
	}
	for _, m := range msgs {
		if t := titleFrom(m.Text); t != "" {
			return t, model.TitlePrompt
		}
	}
	return "", model.TitleNone
}
