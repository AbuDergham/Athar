// Package continuedev reads Continue (continue.dev) chat history: the VS Code /
// JetBrains extension and the cn CLI share one global folder.
// The package is not named "continue" because that is a Go keyword.
//
// Layout (default root %USERPROFILE%\.continue, or CONTINUE_GLOBAL_DIR):
//
//	sessions\sessions.json   index: [{sessionId, title, dateCreated, workspaceDirectory, messageCount}]
//	sessions\<sessionId>.json one session: {sessionId, title, workspaceDirectory, mode, history[]}
//
// Rules learned from real files (Continue 1.2.x):
//   - Every session file is one Source. The index is not part of the
//     fingerprint (it changes whenever any session is saved); its dateCreated
//     (epoch ms as a string) is handed to Read through Source.Extra.
//   - Both files are rewritten whole on every save, so a session file modified
//     within the last 5 s fails the read and is retried on the next sync (§4.3).
//   - history[].message: role user|assistant|tool|thinking|system; content is a
//     string or [{type:"text",text}|{type:"imageUrl",...}]; assistant toolCalls
//     [{id, function{name, arguments}}]. arguments is a JSON string that can be
//     cut short, so the sibling toolCallStates[].parsedArgs is preferred.
//     role "tool" carries the tool output (toolCallId); when it is missing, the
//     output stored in toolCallStates[].output[].content is used instead.
//   - Not indexed: thinking messages, the reasoning field, system messages,
//     contextItems (attached files), promptLogs, editorState.
//   - There are no per-message timestamps: StartedAt = index dateCreated (else
//     the file's creation time), UpdatedAt = file mtime.
//   - workspaceDirectory is a VS Code file:// URI (drive letter lowercased,
//     ":" as %3A); it becomes a Windows path. Other schemes are kept verbatim.
//   - The default title "New Session" means "untitled": the first prompt is used.
//   - Sessions with no messages (empty drafts) are skipped.
package continuedev

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
	id      = "continue"
	rootEnv = "CONTINUE_GLOBAL_DIR"
	// indexName is the session index inside <root>\sessions.
	indexName = "sessions.json"
	// extraDateCreated is the Source.Extra key carrying the index dateCreated.
	extraDateCreated = "dateCreated"
	// defaultTitle is the title Continue gives a session before naming it.
	defaultTitle = "New Session"
	// recentWindow: whole-file JSON modified this recently may be mid-write.
	recentWindow = 5 * time.Second
)

// errRecent marks a source skipped because it was modified within recentWindow.
var errRecent = fmt.Errorf("modified less than 5 s ago: %w", adapters.ErrRetryLater)

type adapter struct {
	now func() time.Time // nil = time.Now; tests move the clock
}

// New returns the Continue adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return id }
func (adapter) Name() string { return "Continue" }
func (adapter) Version() int { return 1 }

func (a adapter) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// root is one Continue global folder; root is "" for the default one.
type root struct{ dir, root string }

// roots returns %USERPROFILE%\.continue and CONTINUE_GLOBAL_DIR when it is set
// to another absolute folder (Continue resolves a relative value against its
// own working directory, which Athar cannot know, so that is ignored).
func roots(env adapters.Env) []root {
	var out []root
	def := ""
	if env.Home != "" {
		def = filepath.Join(env.Home, ".continue")
		out = append(out, root{dir: def})
	}
	if env.Getenv != nil {
		if v := strings.TrimSpace(env.Getenv(rootEnv)); v != "" && filepath.IsAbs(v) {
			v = filepath.Clean(v)
			if def == "" || !strings.EqualFold(v, def) {
				out = append(out, root{dir: v, root: v})
			}
		}
	}
	return out
}

// Sources lists every <root>\sessions\*.json except the index.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	for _, r := range roots(env) {
		dir := filepath.Join(r.dir, "sessions")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // missing locations are skipped silently
		}
		created := readIndex(filepath.Join(dir, indexName))
		for _, e := range entries {
			name := e.Name()
			if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(name), ".json") || strings.EqualFold(name, indexName) {
				continue
			}
			p := filepath.Join(dir, name)
			src := adapters.Source{Adapter: id, Path: p, Files: []string{p}, Root: r.root}
			if dc := created[strings.TrimSuffix(name, filepath.Ext(name))]; dc != "" {
				src.Extra = map[string]string{extraDateCreated: dc}
			}
			out = append(out, src)
		}
	}
	return out, nil
}

// readIndex maps sessionId → dateCreated from sessions.json. A missing or torn
// index yields nil: Read then falls back to the file's own times.
func readIndex(path string) map[string]string {
	data, err := fsread.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	out := make(map[string]string, len(entries))
	for _, raw := range entries {
		var e struct {
			SessionID   json.RawMessage `json:"sessionId"`
			DateCreated json.RawMessage `json:"dateCreated"`
		}
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		if sid := rawText(e.SessionID); sid != "" {
			out[sid] = rawText(e.DateCreated)
		}
	}
	return out
}

// sessionFile is the part of <sessionId>.json that Athar uses.
type sessionFile struct {
	SessionID          string          `json:"sessionId"`
	Title              string          `json:"title"`
	WorkspaceDirectory string          `json:"workspaceDirectory"`
	History            json.RawMessage `json:"history"`
}

// Read parses one session file.
func (a adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	fi, err := os.Stat(src.Path)
	if err != nil {
		return res, err
	}
	mtime := fi.ModTime()
	if age := a.clock().Sub(mtime); age >= 0 && age < recentWindow {
		return res, fmt.Errorf("%s: %w", filepath.Base(src.Path), errRecent)
	}
	data, err := fsread.ReadFile(src.Path)
	if err != nil {
		return res, err
	}
	var f sessionFile
	mistyped := false
	if err := json.Unmarshal(data, &f); err != nil {
		// A syntax error means a torn or corrupt file: the source fails and is
		// retried. A field of an unexpected type only loses that field.
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) {
			return res, fmt.Errorf("%s: %w", filepath.Base(src.Path), err)
		}
		mistyped = true
	}
	if strings.TrimSpace(f.SessionID) == "" && f.History == nil {
		res.Warnings = append(res.Warnings, filepath.Base(src.Path)+": not a Continue session file (no sessionId or history)")
		return res, nil
	}
	if mistyped {
		res.Skipped++
	}

	msgs, skipped, ok := convertHistory(f.History)
	res.Skipped += skipped
	if !ok {
		res.Warnings = append(res.Warnings, filepath.Base(src.Path)+": history is not a list; format not recognized")
	}
	if len(msgs) == 0 {
		return res, nil // an empty draft is not a conversation
	}

	nativeID := strings.TrimSpace(f.SessionID)
	if nativeID == "" {
		nativeID = strings.TrimSuffix(filepath.Base(src.Path), filepath.Ext(src.Path))
	}
	updated := mtime.UTC()
	started := parseDateCreated(src.Extra[extraDateCreated])
	if started.IsZero() {
		started = fileBirth(fi)
	}
	if started.IsZero() || started.After(updated) {
		// The index entry is written a few ms after the session file.
		started = updated
	}
	title, titleSource := sessionTitle(f.Title, msgs)

	res.Sessions = append(res.Sessions, model.Session{
		Agent:       id,
		NativeID:    nativeID,
		Cwd:         workspacePath(f.WorkspaceDirectory),
		Title:       title,
		TitleSource: titleSource,
		StartedAt:   started,
		UpdatedAt:   updated,
		Coverage:    model.CoverageFull,
		Origin:      model.OriginLive,
		Root:        src.Root,
		Messages:    msgs,
	})
	return res, nil
}

// sessionTitle keeps Continue's title (generated by the model or set by the
// user; the file does not say which) unless it is empty or the default.
func sessionTitle(title string, msgs []model.Message) (string, model.TitleSource) {
	if t := strings.TrimSpace(title); t != "" && t != defaultTitle {
		return t, model.TitleAgent
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

// Caps: Continue has no way to resume one session from a terminal (cn
// --resume only reopens the terminal's last session), so Athar opens the
// project in VS Code and the user picks the chat from Continue's history.
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		App: &adapters.AppCommand{
			Name:    "VS Code",
			Program: "code",
			Args:    []string{"{cwd}"},
			Candidates: []string{
				`%LOCALAPPDATA%\Programs\Microsoft VS Code\bin\code.cmd`,
				`%ProgramFiles%\Microsoft VS Code\bin\code.cmd`,
			},
		},
		RootEnv: rootEnv,
	}
}
