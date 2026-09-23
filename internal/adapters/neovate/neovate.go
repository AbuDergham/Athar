// Package neovate reads Neovate Code (@neovate/code) sessions.
//
// Layout (verified against @neovate/code 0.12.7 and the files on the owner's PC):
//
//	%USERPROFILE%\.neovate\projects\<enc>\<id>.jsonl   one file per session
//	%USERPROFILE%\.neovate\data.json                   {"projects":{"<cwd>":{"history":[prompts]}}}
//
// <id> is the 8-character session id that `neovate --resume <id>` takes (the
// file stem). <enc> is the launch folder encoded lossily (every run of
// characters outside [A-Za-z0-9] becomes "-", then lowercased), so it is never
// decoded. Neovate looks the session up under the folder of the CURRENT
// directory, so resuming needs the exact launch folder as Cwd.
//
// Cwd comes from the first record that carries a "cwd" field (0.12.7 writes
// none) or else from data.json: its project keys are the verbatim launch
// folders, and the key whose encoding equals <enc> is the Cwd. When two
// different paths encode to the same <enc> the Cwd stays empty. data.json is
// part of every source's fingerprint so a Cwd appears once data.json has it.
//
// Records (one JSON object per line):
//   - {"type":"config","config":{"summary":...,"approvalMode":...}}: the
//     session config, rewritten at the top of the file; summary is the title
//     the agent generated (TitleAgent).
//   - {"type":"message","uuid","parentUuid","role","content","timestamp",
//     "sessionId"}: role user|assistant|system (a future "tool" role is also
//     accepted); content is a string or parts: text, image, reasoning,
//     tool_use {id,name,input,description}, tool_result {id,name,input,
//     result{llmContent: string | parts}}. Tool results are logged as role
//     "user" messages; they become RoleTool/KindToolOutput here.
//
// Skipped on purpose: reasoning parts, images, system messages, hidden user
// messages, the "[Request interrupted by user]" marker and compaction
// summaries (a user message with parentUuid null after the first message, or
// uiContent "Chat history compacted successfully."). The prompt history in
// data.json has no session ids, so it creates no prompt-log sessions.
package neovate

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/model"
)

// ID is the adapter id stored in the archive.
const ID = "neovate"

type adapter struct {
	mu    sync.Mutex
	cache projectsCache // parsed data.json, reused while its size and mtime are unchanged
}

// New returns the Neovate adapter (used by internal/registry).
func New() adapters.Adapter { return &adapter{} }

func (*adapter) ID() string   { return ID }
func (*adapter) Name() string { return "Neovate" }

// Version is bumped whenever parsing changes.
func (*adapter) Version() int { return 1 }

// Caps: `neovate --resume <id>` must run in the session's launch folder.
func (*adapter) Caps() adapters.Caps {
	return adapters.Caps{
		Resume:            []string{"neovate", "--resume", "{id}"},
		ProgramCandidates: []string{`%APPDATA%\npm\neovate.cmd`},
		SkipPermissions:   []string{"--approval-mode", "yolo"},
	}
}

// rootDir is Neovate's data folder (os.homedir()/.neovate; there is no env override).
func rootDir(env adapters.Env) string { return filepath.Join(env.Home, ".neovate") }

// Sources lists every projects\<enc>\*.jsonl file, one source each. Missing folders are skipped.
func (*adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	if env.Home == "" {
		return nil, nil
	}
	root := rootDir(env)
	projects := filepath.Join(root, "projects")
	dirs, err := os.ReadDir(projects)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	dataJSON := filepath.Join(root, "data.json")
	var out []adapters.Source
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(projects, d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue // vanished or unreadable folder: skip it, the others still count
		}
		for _, f := range files {
			if !f.Type().IsRegular() || !strings.EqualFold(filepath.Ext(f.Name()), ".jsonl") {
				continue
			}
			p := filepath.Join(dir, f.Name())
			out = append(out, adapters.Source{
				Adapter: ID,
				Path:    p,
				Files:   []string{p, dataJSON},
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Read parses one session file.
func (a *adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	var res adapters.ReadResult
	id := strings.TrimSuffix(filepath.Base(src.Path), filepath.Ext(src.Path))
	s, skipped, err := parseSession(src.Path, id)
	if err != nil {
		return res, err
	}
	res.Skipped = skipped
	if s == nil {
		return res, nil // no messages (e.g. a config-only file)
	}
	if s.Cwd == "" {
		// src.Path = <root>\projects\<enc>\<id>.jsonl
		projDir := filepath.Dir(src.Path)
		root := filepath.Dir(filepath.Dir(projDir))
		keys, warn := a.projectKeys(filepath.Join(root, "data.json"))
		if warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
		s.Cwd = matchProject(keys, filepath.Base(projDir))
	}
	res.Sessions = []model.Session{*s}
	return res, nil
}
