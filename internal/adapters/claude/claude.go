// Package claude reads Claude Code's local history (spec §7).
//
// Every Claude root is read: %USERPROFILE%\.claude (the default), the folder
// named by CLAUDE_CONFIG_DIR, and every %USERPROFILE%\.claude* folder that has
// a projects\ subfolder (e.g. .claude-local). In each root:
//
//   - projects\<encoded-cwd>\<session-uuid>.jsonl is one transcript per session
//     (one Source per file). Only files directly inside a project folder are
//     read: subagent and workflow transcripts (<uuid>\subagents\...),
//     tool-results\ and memory\ are never touched. The folder name is a lossy
//     encoding of the cwd and is never decoded; Cwd is the FIRST cwd record.
//   - history.jsonl is the prompt log (one Source per root, Origin prompt-log).
//     Claude Code deletes transcripts after cleanupPeriodDays (30 by default)
//     but never sweeps this file, so it is often the only trace of old sessions.
//
// Headless runs (entrypoint sdk-cli, sdk-ts, sdk-py) are skipped. The live
// session registry <root>\sessions\ is read by internal/launch, not here.
package claude

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
)

// Source kinds carried in Source.Extra["kind"].
const (
	kindTranscript = "transcript"
	kindHistory    = "history"
)

type adapter struct{}

// New returns the Claude Code adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

// ID implements adapters.Adapter.
func (adapter) ID() string { return "claude" }

// Name implements adapters.Adapter.
func (adapter) Name() string { return "Claude Code" }

// Version implements adapters.Adapter. Bump it whenever parsing changes.
func (adapter) Version() int { return 1 }

// Caps implements adapters.Adapter (spec §6.3).
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		Resume:            []string{"claude", "--resume", "{id}"},
		ProgramCandidates: []string{`%USERPROFILE%\.local\bin\claude.exe`},
		Fork:              []string{"--fork-session"},
		SkipPermissions:   []string{"--dangerously-skip-permissions"},
		RootEnv:           "CLAUDE_CONFIG_DIR",
	}
}

// Read implements adapters.Adapter: a history.jsonl source becomes prompt-log
// sessions, anything else is parsed as one session transcript.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	if isHistory(src) {
		return readHistory(ctx, src)
	}
	return readTranscript(ctx, src)
}

func isHistory(src adapters.Source) bool {
	if k := src.Extra["kind"]; k != "" {
		return k == kindHistory
	}
	return strings.EqualFold(filepath.Base(src.Path), "history.jsonl")
}
