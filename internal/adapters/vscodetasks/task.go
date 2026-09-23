package vscodetasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// errRecent marks a source skipped because a file was modified within settle.
var errRecent = fmt.Errorf("modified less than 5 s ago: %w", adapters.ErrRetryLater)

// Read parses one task folder or one SDK session folder.
func (a *adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	if src.Extra[extraKind] == kindSDK {
		return a.readSDK(src)
	}
	return a.readTask(env, src)
}

// checkSettled fails with errRecent when an existing file of paths was
// modified within the settle window, and returns the newest mtime.
func (a *adapter) checkSettled(paths ...string) (time.Time, error) {
	var newest time.Time
	now := a.clock()
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		m := fi.ModTime()
		if age := now.Sub(m); age >= 0 && age < settle {
			return newest, fmt.Errorf("%s: %w", filepath.Base(p), errRecent)
		}
		if m.After(newest) {
			newest = m
		}
	}
	return newest, nil
}

// readJSON reads a whole JSON file into v. A missing file reports found=false.
// A syntax error is returned; a value of an unexpected type is tolerated.
func readJSON(path string, v any) (found bool, err error) {
	data, err := fsread.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) {
			return true, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
	}
	return true, nil
}

// uiMessage is one entry of ui_messages.json (ClineMessage).
type uiMessage struct {
	Ts   json.RawMessage `json:"ts"`
	Type string          `json:"type"` // ask | say
	Ask  string          `json:"ask"`
	Say  string          `json:"say"`
	Text string          `json:"text"`
}

// readTask reads tasks\<id>\.
func (a *adapter) readTask(env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	dir := src.Path
	id := filepath.Base(dir)
	apiPath, uiPath := filepath.Join(dir, apiFile), filepath.Join(dir, uiFile)
	newest, err := a.checkSettled(apiPath, uiPath, filepath.Join(dir, itemFile))
	if err != nil {
		return res, err
	}

	var api []json.RawMessage
	apiFound, apiErr := readJSON(apiPath, &api)
	var ui []json.RawMessage
	uiFound, uiErr := readJSON(uiPath, &ui)
	if apiErr != nil && (uiErr != nil || !uiFound) {
		return res, apiErr // nothing readable: the source fails and is retried
	}
	if apiErr != nil {
		res.Warnings = append(res.Warnings, apiFile+": "+apiErr.Error()+"; read from the chat log instead")
		apiFound = false
	}
	if uiErr != nil {
		res.Warnings = append(res.Warnings, uiFile+": "+uiErr.Error())
		ui = nil
	}
	if !apiFound && !uiFound {
		return res, nil // the task folder was emptied meanwhile
	}

	var times span
	c := converter{}
	for _, raw := range api {
		c.message(raw)
	}
	res.Skipped += c.skipped
	for _, m := range c.msgs {
		times.add(m.At)
	}
	uiMsgs, firstPrompt, uiSkipped := convertUI(ui, &times)
	msgs, coverage := c.msgs, model.CoverageFull
	if len(msgs) == 0 {
		msgs, coverage = uiMsgs, model.CoveragePartial
		res.Skipped += uiSkipped
	}
	if len(msgs) == 0 {
		return res, nil // a task that never got a prompt is not a conversation
	}

	item, _ := a.lookup(env.TempDir, dir, id)
	cwd := item.cwd()
	if cwd == "" {
		cwd = c.cwd
	}
	title, titleSource := "", model.TitleNone
	for _, cand := range []string{item.Task, firstPrompt, firstUserText(msgs)} {
		if t := promptTitle(cand); t != "" {
			title, titleSource = t, model.TitlePrompt
			break
		}
	}

	times.add(item.itemTime())
	started, updated := times.first, times.last
	if t := idTime(id); !t.IsZero() && (started.IsZero() || t.Before(started)) && (updated.IsZero() || !t.After(updated)) {
		started = t
	}
	if updated.IsZero() {
		updated = newest.UTC()
	}
	if started.IsZero() || started.After(updated) {
		started = updated
	}

	res.Sessions = append(res.Sessions, model.Session{
		Agent:       a.f.id,
		NativeID:    id,
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

// firstUserText is the first user prompt among msgs.
func firstUserText(msgs []model.Message) string {
	for _, m := range msgs {
		if m.Role == model.RoleUser && m.Kind == model.KindText {
			return m.Text
		}
	}
	return ""
}

// convertUI turns the chat log into messages (used when the API history is
// missing), records every ts in times, and returns the first prompt (say=task).
func convertUI(ui []json.RawMessage, times *span) (msgs []model.Message, firstPrompt string, skipped int) {
	for _, raw := range ui {
		var m uiMessage
		if !isObject(raw) || !decode(raw, &m) {
			skipped++
			continue
		}
		at := msTime(m.Ts)
		times.add(at)
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		switch {
		case m.Type == "say" && m.Say == "task":
			if firstPrompt == "" {
				firstPrompt = text
			}
			msgs = append(msgs, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: text})
		case m.Type == "say" && m.Say == "user_feedback":
			msgs = append(msgs, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: text})
		case m.Type == "say" && (m.Say == "text" || m.Say == "completion_result"):
			msgs = append(msgs, model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: at, Text: text})
		case m.Type == "ask" && (m.Ask == "followup" || m.Ask == "plan_mode_respond"):
			// text is JSON {question|response, options}; plain text in old builds.
			var q struct {
				Question string `json:"question"`
				Response string `json:"response"`
			}
			if strings.HasPrefix(text, "{") && decode([]byte(text), &q) {
				text = strings.TrimSpace(q.Question + q.Response)
			}
			if text != "" {
				msgs = append(msgs, model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: at, Text: text})
			}
		case m.Say == "command" || m.Ask == "command":
			msgs = append(msgs, model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at,
				Text: callText("execute_command", []string{text}, "")})
		}
	}
	return msgs, firstPrompt, skipped
}
