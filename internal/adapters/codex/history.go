package codex

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// historyLine is one line of history.jsonl: {session_id, ts (unix seconds), text}.
type historyLine struct {
	SessionID string      `json:"session_id"`
	Ts        json.Number `json:"ts"`
	Text      string      `json:"text"`
}

// unixTime converts a history timestamp; values that look like milliseconds are accepted too.
func unixTime(n json.Number) time.Time {
	f, err := n.Float64()
	if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return time.Time{}
	}
	if f > 1e12 {
		f /= 1000
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

// readHistory turns the prompt log into one prompts-only session per session id
// (spec §4.5): it fills sessions whose rollout is gone and merges into the rest.
func readHistory(ctx context.Context, src adapters.Source, idx map[string]thread) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	byID := map[string]*model.Session{}
	var order []string
	n := 0
	err := fsread.Lines(src.Path, func(line []byte) error {
		n++
		if n%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var h historyLine
		if json.Unmarshal(line, &h) != nil || strings.TrimSpace(h.SessionID) == "" {
			res.Skipped++
			return nil
		}
		if strings.TrimSpace(h.Text) == "" {
			return nil
		}
		at := unixTime(h.Ts)
		s := byID[h.SessionID]
		if s == nil {
			s = &model.Session{
				Agent:    agentID,
				NativeID: h.SessionID,
				Coverage: model.CoveragePrompts,
				Origin:   model.OriginPromptLog,
				Root:     src.Root,
			}
			byID[h.SessionID] = s
			order = append(order, h.SessionID)
		}
		s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: h.Text})
		if !at.IsZero() {
			if s.StartedAt.IsZero() || at.Before(s.StartedAt) {
				s.StartedAt = at
			}
			if at.After(s.UpdatedAt) {
				s.UpdatedAt = at
			}
		}
		return nil
	})
	if err != nil {
		return adapters.ReadResult{}, err
	}
	for _, id := range order {
		s := byID[id]
		info := idx[id]
		s.Cwd = info.cwd
		s.Title, s.TitleSource = pickTitle("", info.title, s.Messages)
		res.Sessions = append(res.Sessions, *s)
	}
	return res, nil
}
