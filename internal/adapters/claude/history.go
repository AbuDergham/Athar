package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// historyLine is one prompt of <root>\history.jsonl:
// {display, pastedContents, project, sessionId, timestamp (epoch ms)}.
type historyLine struct {
	Display   string          `json:"display"`
	Project   string          `json:"project"`
	SessionID string          `json:"sessionId"`
	Timestamp json.RawMessage `json:"timestamp"`
}

// promptLog accumulates one session of the prompt log.
type promptLog struct {
	s          model.Session
	times      span
	firstTyped string // first prompt that is not a slash command
}

// readHistory turns history.jsonl into one prompt-log session per sessionId
// (coverage prompts). The archive merges them into the full session when a
// transcript exists, and keeps them as the only trace once Claude Code has
// deleted the transcript. Lines without a sessionId (older Claude Code
// versions) cannot be attributed and are counted as skipped.
func readHistory(ctx context.Context, src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	byID := map[string]*promptLog{}
	var order []*promptLog
	noID, n := 0, 0
	err := fsread.Lines(src.Path, func(line []byte) error {
		n++
		if n%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var h historyLine
		if !decode(line, &h) {
			res.Skipped++
			return nil
		}
		if h.SessionID == "" {
			res.Skipped++
			noID++
			return nil
		}
		p := byID[h.SessionID]
		if p == nil {
			p = &promptLog{s: model.Session{
				Agent:    "claude",
				NativeID: h.SessionID,
				Coverage: model.CoveragePrompts,
				Origin:   model.OriginPromptLog,
				Root:     src.Root,
			}}
			byID[h.SessionID] = p
			order = append(order, p)
		}
		at := epochTime(h.Timestamp)
		p.times.add(at)
		if p.s.Cwd == "" {
			p.s.Cwd = h.Project
		}
		text := strings.TrimSpace(h.Display)
		if text == "" {
			return nil
		}
		p.s.Messages = append(p.s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: text})
		if p.firstTyped == "" && !strings.HasPrefix(text, "/") {
			p.firstTyped = text
		}
		return nil
	})
	if err != nil {
		return adapters.ReadResult{}, err
	}
	if noID > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d prompts without a session id skipped", noID))
	}
	for _, p := range order {
		if len(p.s.Messages) == 0 {
			continue
		}
		s := p.s
		s.StartedAt, s.UpdatedAt = p.times.first, p.times.last
		// The first real prompt; a session of slash commands only is titled by its first one.
		title := p.firstTyped
		if title == "" {
			title = s.Messages[0].Text
		}
		s.Title, s.TitleSource = promptTitle(title), model.TitlePrompt
		res.Sessions = append(res.Sessions, s)
	}
	return res, nil
}

// epochTime reads a timestamp given as epoch milliseconds (or seconds, for
// small values) or as an ISO-8601 string; zero when absent or invalid.
func epochTime(raw json.RawMessage) time.Time {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return time.Time{}
		}
		return parseTime(s)
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || f <= 0 {
		return time.Time{}
	}
	if f < 1e11 { // seconds: 1e11 s is the year 5138, 1e11 ms is 1973
		return time.Unix(int64(f), 0).UTC()
	}
	return time.UnixMilli(int64(f)).UTC()
}
