package qwen

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

// conversation is a whole-file session of Qwen Code 0.0.14-0.3.x
// (tmp/<hash>/chats/session-<time>-<id8>.json, Gemini CLI's old format).
type conversation struct {
	SessionID   string            `json:"sessionId"`
	StartTime   string            `json:"startTime"`
	LastUpdated string            `json:"lastUpdated"`
	Messages    []json.RawMessage `json:"messages"`
}

type convMessage struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"` // user | qwen (gemini in forks) | info/error/warning
	Content   json.RawMessage `json:"content"`
	ToolCalls []struct {
		Name          string          `json:"name"`
		Args          json.RawMessage `json:"args"`
		Result        json.RawMessage `json:"result"`
		ResultDisplay json.RawMessage `json:"resultDisplay"`
		Timestamp     string          `json:"timestamp"`
	} `json:"toolCalls"`
}

func readSessionJSON(env adapters.Env, src adapters.Source, base string) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	data, err := fsread.ReadFile(src.Path)
	if err != nil {
		return res, err
	}
	var c conversation
	if err := json.Unmarshal(bytes.TrimPrefix(data, utf8BOM), &c); err != nil {
		return res, fmt.Errorf("session %s: %w", filepath.Base(src.Path), err)
	}
	if strings.TrimSpace(c.SessionID) == "" {
		res.Skipped++
		return res, nil
	}
	s := model.Session{
		Agent:    "qwen",
		NativeID: c.SessionID,
		Coverage: model.CoverageFull,
		Origin:   model.OriginLive,
		Root:     src.Root,
	}
	var times span
	if t, ok := parseTime(c.StartTime); ok {
		times.add(t)
	}
	if t, ok := parseTime(c.LastUpdated); ok {
		times.add(t)
	}
	firstPrompt := ""
	for _, raw := range c.Messages {
		var m convMessage
		if json.Unmarshal(raw, &m) != nil {
			res.Skipped++
			continue
		}
		at, _ := parseTime(m.Timestamp)
		times.add(at)
		switch m.Type {
		case "user":
			text, attachments := userText(parsePartList(m.Content))
			if firstPrompt == "" && strings.TrimSpace(text) != "" {
				firstPrompt = text
			}
			if full := withAttachments(text, attachments); strings.TrimSpace(full) != "" {
				s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: full})
			}
		case "qwen", "gemini", "model", "assistant":
			s.Messages = append(s.Messages, assistantMessages(parsePartList(m.Content), at)...)
			for _, tc := range m.ToolCalls {
				tat, ok := parseTime(tc.Timestamp)
				if !ok {
					tat = at
				}
				times.add(tat)
				s.Messages = append(s.Messages, toolCall(tc.Name, tc.Args, tat))
				out := resultText(tc.Result)
				if strings.TrimSpace(out) == "" {
					out = jsonString(tc.ResultDisplay)
				}
				if msg, ok := toolOutput(out, tat); ok {
					s.Messages = append(s.Messages, msg)
				}
			}
		}
	}
	if len(s.Messages) == 0 {
		return res, nil
	}
	s.StartedAt, s.UpdatedAt = times.first, times.last
	s.Title, s.TitleSource = titleFrom(firstPrompt), model.TitlePrompt
	if s.Title == "" {
		s.TitleSource = model.TitleNone
	}
	// tmp/<hash>/chats/<file>: the folder hash identifies the project.
	s.Cwd = resolveHash(env, base, filepath.Base(filepath.Dir(filepath.Dir(src.Path))))
	res.Sessions = append(res.Sessions, s)
	return res, nil
}
