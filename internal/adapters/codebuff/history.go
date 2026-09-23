package codebuff

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// historyID is the NativeID of the prompt-log session of the default root.
const historyID = "message-history"

// readHistory reads <root>\message-history.json (a JSON array of prompt
// strings, oldest first) into one prompt-log session holding only the prompts
// that no chat folder under the same root contains. Non-string elements are
// counted as skipped; blank ones are ignored. No session when nothing is left.
func readHistory(ctx context.Context, src adapters.Source) (adapters.ReadResult, error) {
	path := src.Path
	if err := checkRecent(path); err != nil {
		return adapters.ReadResult{}, err
	}
	data, err := fsread.ReadFile(path)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return adapters.ReadResult{}, fmt.Errorf("%s: %w", path, err)
	}
	_, mtime, err := fsread.Stat(path)
	if err != nil {
		return adapters.ReadResult{}, err
	}

	var res adapters.ReadResult
	var prompts []string
	for _, r := range raw {
		var p string
		if json.Unmarshal(r, &p) != nil {
			res.Skipped++
			continue
		}
		if p = strings.TrimSpace(p); p != "" {
			prompts = append(prompts, p)
		}
	}
	if len(prompts) == 0 {
		return res, nil
	}

	inChats, err := chatPrompts(ctx, filepath.Dir(path))
	if err != nil {
		return adapters.ReadResult{}, err
	}
	var msgs []model.Message
	seen := map[string]bool{}
	for _, p := range prompts {
		if inChats[p] || seen[p] {
			continue
		}
		seen[p] = true
		msgs = append(msgs, model.Message{Role: model.RoleUser, Kind: model.KindText, Text: p})
	}
	if len(msgs) == 0 {
		return res, nil
	}

	id := historyID
	if src.Root != "" {
		id += ":" + filepath.Base(filepath.Dir(path))
	}
	s := model.Session{
		Agent:    ID,
		NativeID: id,
		Coverage: model.CoveragePrompts,
		Origin:   model.OriginPromptLog,
		Root:     src.Root,
		Messages: msgs,
	}
	s.Title, s.TitleSource = promptTitle(msgs, "")
	s.StartedAt, s.UpdatedAt = mtime.UTC(), mtime.UTC()
	res.Sessions = []model.Session{s}
	return res, nil
}

// chatPrompts returns the (trimmed) user prompts of every chat folder under a
// data root. Unreadable chats are ignored: at worst a prompt is kept twice.
func chatPrompts(ctx context.Context, root string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, c := range chatDirs(root) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		files := chatFiles(c.path)
		if len(files) == 0 {
			continue
		}
		if filepath.Base(files[0]) == logFile {
			if err := logPrompts(ctx, files[0], out); err != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		data, err := fsread.ReadFile(files[0])
		if err != nil {
			continue
		}
		var msgs []json.RawMessage
		if json.Unmarshal(data, &msgs) != nil {
			continue
		}
		for _, r := range msgs {
			var m chatMessage
			if json.Unmarshal(r, &m) == nil && m.Variant == "user" {
				if t := userText(m); t != "" {
					out[t] = true
				}
			}
		}
	}
	return out, nil
}
