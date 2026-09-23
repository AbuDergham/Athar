// Package copilot reads the chat sessions of VS Code's chat view (GitHub
// Copilot Chat, including agent mode) from VS Code, VS Code Insiders and
// VSCodium.
//
// Layout (per host, %APPDATA%\<host>\User):
//
//	workspaceStorage\<h>\chatSessions\<sessionId>.json   whole-file JSON (VS Code < 1.109)
//	workspaceStorage\<h>\chatSessions\<sessionId>.jsonl  mutation log (VS Code >= 1.109)
//	workspaceStorage\<h>\workspace.json                  {folder: "file:///c%3A/..."} or {workspace: ...}
//	globalStorage\emptyWindowChatSessions\<sessionId>.json|.jsonl  chats of windows with no folder
//
// Rules learned from real files (version 3 sessions):
//   - Every session file is one Source; Extra["host"] names the host folder.
//     If <id>.json and <id>.jsonl both exist, only the newer one is listed.
//   - .json files are rewritten whole, so one modified within the last 5 s
//     fails with adapters.ErrRetryLater and is read on the next sync (§4.3).
//   - .jsonl is replayed: kind 0 = snapshot {v}, 1 = set k-path to v,
//     2 = push v items to the array at k (after truncating it to i when
//     given), 3 = delete k. A torn last line of a file written in the last
//     5 s means "retry later"; any other bad line is skipped.
//   - requests[].message.text → user; response[] parts without kind (or
//     markdownContent) → assistant text, with inlineReference parts shown as
//     the file's base name (as VS Code does); toolInvocationSerialized →
//     tool call "<toolId>: <terminal command | past-tense message>", except
//     invocations with presentation "hidden" (the edit tools, which name no
//     file); textEditGroup → tool call "edit: <path>", once per file and
//     request. Thinking, progress, confirmation, undo stops and code block
//     URIs are UI state and are not read.
//   - Tool outputs are not read: result.metadata.toolCallResults is keyed by
//     the model's tool-call ids, which do not match the toolCallId of the
//     response parts, so outputs cannot be placed next to their calls.
//   - customTitle → TitleUser; otherwise the first prompt.
//   - creationDate / lastMessageDate / timestamp are epoch ms. The log format
//     has no lastMessageDate: UpdatedAt is the last request's timestamp.
//   - Cwd is workspace.json "folder" of the same <h> (a file URI becomes a
//     Windows path; remote URIs are kept verbatim). Multi-root workspaces and
//     empty windows have no Cwd.
//   - Sessions with no requests (empty drafts, 20 of 28 files locally) are skipped.
package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

const (
	id = "copilot"
	// extraHost is the Source.Extra key naming the VS Code host folder.
	extraHost = "host"
	// recentWindow: a file modified this recently may be mid-write.
	recentWindow = 5 * time.Second
)

// hosts are the %APPDATA% folders of the VS Code builds that share this format.
var hosts = []string{"Code", "Code - Insiders", "VSCodium"}

type adapter struct {
	now func() time.Time // nil = time.Now; tests move the clock
}

// New returns the VS Code Copilot Chat adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return id }
func (adapter) Name() string { return "VS Code Copilot Chat" }
func (adapter) Version() int { return 1 }

func (a adapter) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// Sources lists every chat session file of every host.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	if env.AppData == "" {
		return nil, nil
	}
	var out []adapters.Source
	for _, host := range hosts {
		user := filepath.Join(env.AppData, host, "User")
		ws, err := os.ReadDir(filepath.Join(user, "workspaceStorage"))
		if err == nil {
			for _, e := range ws {
				if e.IsDir() {
					out = append(out, sessionFiles(filepath.Join(user, "workspaceStorage", e.Name(), "chatSessions"), host)...)
				}
			}
		}
		out = append(out, sessionFiles(filepath.Join(user, "globalStorage", "emptyWindowChatSessions"), host)...)
	}
	return out, nil
}

// sessionFiles lists the .json/.jsonl files of one folder, keeping only the
// newer file when a session exists in both formats.
func sessionFiles(dir, host string) []adapters.Source {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // missing locations are skipped silently
	}
	type cand struct {
		path  string
		mtime time.Time
	}
	best := map[string]cand{}
	var order []string
	for _, e := range entries {
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !e.Type().IsRegular() || (ext != ".json" && ext != ".jsonl") {
			continue
		}
		p := filepath.Join(dir, name)
		var mtime time.Time
		if fi, err := e.Info(); err == nil {
			mtime = fi.ModTime()
		}
		key := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		old, seen := best[key]
		if !seen {
			order = append(order, key)
		}
		if !seen || mtime.After(old.mtime) || (mtime.Equal(old.mtime) && ext == ".jsonl") {
			best[key] = cand{p, mtime}
		}
	}
	out := make([]adapters.Source, 0, len(order))
	for _, k := range order {
		p := best[k].path
		out = append(out, adapters.Source{
			Adapter: id,
			Path:    p,
			Files:   []string{p},
			Extra:   map[string]string{extraHost: host},
		})
	}
	return out
}

// Read parses one session file.
func (a adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	_, mtime, err := fsread.Stat(src.Path)
	if err != nil {
		return res, err
	}
	recent := false
	if age := a.clock().Sub(mtime); age >= 0 && age < recentWindow {
		recent = true
	}
	base := filepath.Base(src.Path)
	isLog := strings.EqualFold(filepath.Ext(src.Path), ".jsonl")
	if recent && !isLog {
		return res, fmt.Errorf("%s: modified less than 5 s ago: %w", base, adapters.ErrRetryLater)
	}
	data, err := fsread.ReadFile(src.Path)
	if err != nil {
		return res, err
	}

	if isLog {
		state, skipped, torn := replayLog(data)
		if torn && recent {
			return res, fmt.Errorf("%s: last line is being written: %w", base, adapters.ErrRetryLater)
		}
		res.Skipped += skipped
		if torn {
			res.Skipped++
		}
		if state == nil {
			res.Warnings = append(res.Warnings, base+": no session snapshot (kind 0) in the log; format not recognized")
			return res, nil
		}
		if data, err = json.Marshal(state); err != nil {
			return res, fmt.Errorf("%s: %w", base, err)
		}
	}

	var f sessionData
	if err := json.Unmarshal(data, &f); err != nil {
		// A syntax error means a torn or corrupt file: the source fails and is
		// retried. A field of an unexpected type only loses that field.
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) {
			return res, fmt.Errorf("%s: %w", base, err)
		}
		res.Skipped++
	}

	msgs, lastReq, skipped := convertRequests(f.Requests)
	res.Skipped += skipped
	if len(msgs) == 0 {
		return res, nil // an empty draft is not a conversation
	}

	nativeID := strings.TrimSpace(f.SessionID)
	if nativeID == "" {
		nativeID = strings.TrimSuffix(base, filepath.Ext(base))
	}
	started := msTime(f.CreationDate)
	updated := msTime(f.LastMessageDate)
	if lastReq.After(updated) {
		updated = lastReq
	}
	if updated.IsZero() {
		updated = mtime.UTC()
	}
	if started.IsZero() || started.After(updated) {
		started = firstTime(msgs, updated)
	}
	title, titleSource := sessionTitle(f.CustomTitle, msgs)

	res.Sessions = append(res.Sessions, model.Session{
		Agent:       id,
		NativeID:    nativeID,
		Cwd:         sessionCwd(src.Path),
		Title:       title,
		TitleSource: titleSource,
		StartedAt:   started,
		UpdatedAt:   updated,
		Coverage:    model.CoverageFull,
		Origin:      model.OriginLive,
		Messages:    msgs,
	})
	return res, nil
}

// firstTime is the earliest message time, or fallback.
func firstTime(msgs []model.Message, fallback time.Time) time.Time {
	t := fallback
	for _, m := range msgs {
		if !m.At.IsZero() && m.At.Before(t) {
			t = m.At
		}
	}
	return t
}

// sessionTitle prefers the user's customTitle, then the first prompt.
func sessionTitle(custom string, msgs []model.Message) (string, model.TitleSource) {
	if t := strings.TrimSpace(custom); t != "" {
		return promptTitle(t), model.TitleUser
	}
	for _, m := range msgs {
		if m.Role == model.RoleUser && m.Kind == model.KindText {
			if t := promptTitle(m.Text); t != "" {
				return t, model.TitlePrompt
			}
		}
	}
	return "", model.TitleNone
}

// sessionCwd reads the workspace folder of a workspaceStorage\<h>\chatSessions
// file from <h>\workspace.json. "" for empty windows and multi-root workspaces.
func sessionCwd(sessionPath string) string {
	dir := filepath.Dir(sessionPath)
	if !strings.EqualFold(filepath.Base(dir), "chatSessions") {
		return ""
	}
	data, err := fsread.ReadFile(filepath.Join(filepath.Dir(dir), "workspace.json"))
	if err != nil {
		return ""
	}
	var w struct {
		Folder string `json:"folder"`
	}
	if json.Unmarshal(data, &w) != nil {
		return ""
	}
	return workspacePath(w.Folder)
}

// Caps: VS Code has no command line or URL that opens one chat session, so
// Athar opens the project in VS Code and the user picks the chat from the
// chat history. Insiders and VSCodium sessions use the same command; their
// launchers are the last candidates.
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		App: &adapters.AppCommand{
			Name:    "VS Code",
			Program: "code",
			Args:    []string{"{cwd}"},
			Candidates: []string{
				`%LOCALAPPDATA%\Programs\Microsoft VS Code\bin\code.cmd`,
				`%ProgramFiles%\Microsoft VS Code\bin\code.cmd`,
				`%LOCALAPPDATA%\Programs\Microsoft VS Code Insiders\bin\code-insiders.cmd`,
				`%LOCALAPPDATA%\Programs\VSCodium\bin\codium.cmd`,
			},
		},
	}
}
