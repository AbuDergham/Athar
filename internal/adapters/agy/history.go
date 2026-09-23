package agy

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// historyLine is one record of antigravity-cli\history.jsonl.
type historyLine struct {
	Display        string  `json:"display"`
	Timestamp      float64 `json:"timestamp"` // epoch ms
	Workspace      string  `json:"workspace"` // Windows path, verbatim
	ConversationID string  `json:"conversationId"`
	Type           string  `json:"type"` // "" for prompts, "slash_command" for /commands
}

// readHistory turns the prompt log into one prompts-coverage session per
// conversation id. Slash commands are UI commands, not prompts, and are ignored.
// Lines that are not JSON or have no conversation id (older agy versions) are
// counted as skipped.
func readHistory(ctx context.Context, path string) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	byID := map[string]*model.Session{}
	var order []string
	err := fsread.Lines(path, func(line []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var h historyLine
		if err := json.Unmarshal(line, &h); err != nil {
			res.Skipped++
			return nil
		}
		if h.ConversationID == "" {
			res.Skipped++
			return nil
		}
		if h.Type == "slash_command" || strings.TrimSpace(h.Display) == "" {
			return nil
		}
		s := byID[h.ConversationID]
		if s == nil {
			s = &model.Session{
				Agent:       agentID,
				NativeID:    h.ConversationID,
				TitleSource: model.TitleNone,
				Coverage:    model.CoveragePrompts,
				Origin:      model.OriginPromptLog,
			}
			byID[h.ConversationID] = s
			order = append(order, h.ConversationID)
		}
		if s.Cwd == "" {
			s.Cwd = h.Workspace
		}
		if s.TitleSource == model.TitleNone {
			s.Title, s.TitleSource = shortTitle(h.Display), model.TitlePrompt
		}
		at := msTime(h.Timestamp)
		widen(s, at)
		s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: h.Display})
		return nil
	})
	if err != nil {
		return adapters.ReadResult{}, err
	}
	for _, id := range order {
		res.Sessions = append(res.Sessions, *byID[id])
	}
	return res, nil
}
