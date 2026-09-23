package qwen

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// chatRecord is one line of a 0.4+ transcript (Qwen's ChatRecord). Only the
// fields Athar uses are decoded.
type chatRecord struct {
	UUID        string `json:"uuid"`
	Timestamp   string `json:"timestamp"`
	Type        string `json:"type"`    // user | assistant | tool_result | system
	Subtype     string `json:"subtype"` // 30+ values; unknown ones are ignored
	Cwd         string `json:"cwd"`
	IsSidechain bool   `json:"isSidechain"` // subagent records: not archived
	Message     *struct {
		Parts []part `json:"parts"`
	} `json:"message"`
	SystemPayload  json.RawMessage `json:"systemPayload"`
	ToolCallResult *struct {
		ResultDisplay json.RawMessage `json:"resultDisplay"`
	} `json:"toolCallResult"`
}

func (r chatRecord) parts() []part {
	if r.Message == nil {
		return nil
	}
	return r.Message.Parts
}

// readChat reads projects/<enc>/chats/<id>.jsonl. Records are kept in file
// order (rewound branches included: the archive never forgets); a record
// repeated verbatim (same uuid and bytes) is read once.
func readChat(ctx context.Context, src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	s := model.Session{
		Agent:    "qwen",
		NativeID: strings.TrimSuffix(filepath.Base(src.Path), ".jsonl"),
		Coverage: model.CoverageFull,
		Origin:   model.OriginLive,
		Root:     src.Root,
	}
	var (
		times       span
		title       string
		titleSource model.TitleSource
		firstPrompt string
		seen        = map[string]bool{}
	)
	err := fsread.Lines(src.Path, func(line []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var r chatRecord
		if json.Unmarshal(line, &r) != nil || r.Type == "" {
			res.Skipped++
			return nil
		}
		if r.UUID != "" {
			sum := sha256.Sum256(line)
			key := r.UUID + "\x00" + string(sum[:])
			if seen[key] {
				return nil
			}
			seen[key] = true
		}
		at, _ := parseTime(r.Timestamp)
		times.add(at)
		if s.Cwd == "" && r.Cwd != "" {
			s.Cwd = r.Cwd // the FIRST cwd; later records never change it
		}
		if r.IsSidechain {
			return nil
		}
		switch r.Type {
		case "user":
			switch r.Subtype {
			case "", "mid_turn_user_message", "realtime_message":
			default: // notification, cron, goal_runtime, …: not typed by the user
				return nil
			}
			text, attachments := userText(r.parts())
			if dt, ok := displayText(r.SystemPayload); ok {
				text = dt // what the user typed, before @file / hook expansion
			}
			if r.Subtype == "" && firstPrompt == "" && strings.TrimSpace(text) != "" {
				firstPrompt = text
			}
			if full := withAttachments(text, attachments); strings.TrimSpace(full) != "" {
				s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: full})
			}
			s.Messages = append(s.Messages, toolOutputs(r.parts(), at)...)
		case "assistant":
			if r.Subtype != "" && r.Subtype != "realtime_message" {
				return nil
			}
			s.Messages = append(s.Messages, assistantMessages(r.parts(), at)...)
		case "tool_result":
			outs := toolOutputs(r.parts(), at)
			if len(outs) == 0 && r.ToolCallResult != nil {
				if m, ok := toolOutput(jsonString(r.ToolCallResult.ResultDisplay), at); ok {
					outs = append(outs, m)
				}
			}
			s.Messages = append(s.Messages, outs...)
		case "system":
			if r.Subtype == "custom_title" {
				var p struct {
					CustomTitle string `json:"customTitle"`
					TitleSource string `json:"titleSource"`
				}
				if json.Unmarshal(r.SystemPayload, &p) == nil && strings.TrimSpace(p.CustomTitle) != "" {
					title = p.CustomTitle // last one wins, as in Qwen
					titleSource = model.TitleUser
					if p.TitleSource == "auto" {
						titleSource = model.TitleAgent
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	if len(s.Messages) == 0 {
		return res, nil // startup-only file (system records): nothing to archive
	}
	switch {
	case title != "":
		s.Title, s.TitleSource = strings.TrimSpace(title), titleSource
	case firstPrompt != "":
		s.Title, s.TitleSource = titleFrom(firstPrompt), model.TitlePrompt
	default:
		s.TitleSource = model.TitleNone
	}
	s.StartedAt, s.UpdatedAt = times.first, times.last
	res.Sessions = append(res.Sessions, s)
	return res, nil
}

// displayText returns systemPayload.displayText of a user record: the prompt
// as the user typed it (the parts may hold the expanded, model-facing text).
func displayText(payload json.RawMessage) (string, bool) {
	var p struct {
		DisplayText *string `json:"displayText"`
	}
	if len(payload) == 0 || json.Unmarshal(payload, &p) != nil || p.DisplayText == nil {
		return "", false
	}
	return *p.DisplayText, true
}
