// Package codex reads the local history of the OpenAI Codex CLI and of the
// Codex VS Code extension (which writes to the same folder).
//
// Layout of a Codex root (%USERPROFILE%\.codex, and CODEX_HOME when it is set
// and points elsewhere; both are read):
//
//	sessions\YYYY\MM\DD\rollout-<local time>-<thread uuid>.jsonl  one thread per file
//	archived_sessions\rollout-*.jsonl                              threads moved by `codex archive`
//	rollout-*.jsonl.zst                                            zstd-compressed rollouts (newer CLIs)
//	history.jsonl                                                  prompt log {session_id, ts (unix s), text}
//	state_<N>.sqlite (+ -wal, -shm)                                derived thread index (titles, cwd)
//	session_index.jsonl                                            thread names {id, thread_name, updated_at}
//
// Rules (from the 2026-09 research and the owner's files):
//   - One Source per rollout file and one per history.jsonl. The rollout is the
//     source of truth; the state DB and session_index.jsonl only add titles
//     (and a cwd for prompt-log sessions). They are looked up while a rollout is
//     read, cached per root and refreshed when their fingerprint changes, so a
//     rename that happens without any rollout change is picked up the next time
//     that rollout changes (or on --rescan / an adapter version bump).
//   - A .jsonl.zst rollout is written whole: one that fails to decode and
//     changed less than 5 s ago is still being written, so Read returns
//     adapters.ErrRetryLater and the next sync reads it again. Plain .jsonl
//     rollouts are append-only; their unterminated last line is left for later.
//   - The file name and date folders are LOCAL time; record timestamps are UTC
//     and are the only times used.
//   - NativeID = the first session_meta payload.id (forked rollouts may embed
//     their parent's session_meta later on), else the UUID in the file name.
//   - Cwd = first session_meta cwd, else the first turn_context cwd, else the
//     <cwd> of an injected <environment_context> message.
//   - Messages: user prompts from event_msg user_message (kind "plain" or none;
//     the injected environment_context / user_instructions kinds are dropped,
//     and the VS Code "# Context from my IDE setup" preamble is cut down to the
//     text after "## My request for Codex:"), assistant text from event_msg
//     agent_message. Files without those events (older CLIs, and the legacy
//     pre-2025-08 format without the {timestamp,type,payload} envelope) fall
//     back to response_item messages, minus the injected context. Tool calls
//     come from response_item function_call / custom_tool_call /
//     local_shell_call / web_search_call, outputs from *_call_output (first
//     2 KB). Reasoning (encrypted or summarized) and compaction summaries are
//     never read.
//   - Title: the last thread_name_updated event, else the thread name from
//     session_index.jsonl, else the state DB title when it is not just the
//     first prompt (spec §2.2 counts Codex titles as agent titles), else the
//     first prompt.
//   - Skipped entirely: sub-agent threads (session_meta source {"subagent":…}),
//     non-interactive `codex exec` and `codex mcp-server` runs (source "exec" /
//     "mcp"; spec §1 non-goals: workflow transcripts), and rollouts with no
//     message at all (a thread opened and closed without a prompt).
package codex

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
)

const (
	agentID     = "codex"
	historyName = "history.jsonl"
	// extraRoot is the Source.Extra key holding the Codex root folder.
	extraRoot = "root"
)

type adapter struct {
	index *indexCache
}

// New returns the Codex adapter (used by internal/registry).
func New() adapters.Adapter { return &adapter{index: newIndexCache()} }

// ID implements adapters.Adapter.
func (*adapter) ID() string { return agentID }

// Name implements adapters.Adapter.
func (*adapter) Name() string { return "Codex" }

// Version implements adapters.Adapter.
func (*adapter) Version() int { return 1 }

// Caps implements adapters.Adapter (spec §6.3).
func (*adapter) Caps() adapters.Caps {
	return adapters.Caps{
		Resume:            []string{"codex", "resume", "{id}"},
		ProgramCandidates: []string{`%APPDATA%\npm\codex.cmd`},
		SkipPermissions:   []string{"--dangerously-bypass-approvals-and-sandbox"},
		RootEnv:           "CODEX_HOME",
	}
}

// root is one Codex data folder. id is "" for the default root, else the
// absolute folder (it becomes Session.Root and the CODEX_HOME for resume).
type root struct {
	dir, id string
}

// roots returns %USERPROFILE%\.codex and CODEX_HOME (when set and different).
func roots(env adapters.Env) []root {
	var out []root
	def := ""
	if env.Home != "" {
		def = filepath.Join(env.Home, ".codex")
		out = append(out, root{dir: def})
	}
	if env.Getenv != nil {
		if ch := strings.TrimSpace(env.Getenv("CODEX_HOME")); ch != "" {
			if abs, err := filepath.Abs(ch); err == nil {
				ch = abs
			}
			ch = filepath.Clean(ch)
			if def == "" || !samePath(ch, def) {
				out = append(out, root{dir: ch, id: ch})
			}
		}
	}
	return out
}

// samePath compares two folder paths the way the file system does.
func samePath(a, b string) bool {
	if abs, err := filepath.Abs(a); err == nil {
		a = abs
	}
	if abs, err := filepath.Abs(b); err == nil {
		b = abs
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Sources lists every rollout (sessions\ and archived_sessions\) of every
// root, then each root's history.jsonl, so prompt logs come after the
// transcripts they merge into. Missing folders are skipped silently.
func (*adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var rollouts, logs []adapters.Source
	for _, r := range roots(env) {
		if info, err := os.Stat(r.dir); err != nil || !info.IsDir() {
			continue
		}
		var files []string
		for _, sub := range []string{"sessions", "archived_sessions"} {
			files = append(files, rolloutFiles(filepath.Join(r.dir, sub))...)
		}
		sort.Strings(files)
		for _, f := range files {
			rollouts = append(rollouts, source(f, r))
		}
		h := filepath.Join(r.dir, historyName)
		if info, err := os.Stat(h); err == nil && info.Mode().IsRegular() {
			logs = append(logs, source(h, r))
		}
	}
	return append(rollouts, logs...), nil
}

func source(path string, r root) adapters.Source {
	return adapters.Source{
		Adapter: agentID,
		Path:    path,
		Files:   []string{path},
		Root:    r.id,
		Extra:   map[string]string{extraRoot: r.dir},
	}
}

// rolloutFiles walks dir for rollout-*.jsonl and rollout-*.jsonl.zst files.
// Unreadable folders are skipped.
func rolloutFiles(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // missing root or unreadable folder: skip it
		}
		if d.Type().IsRegular() && isRolloutName(d.Name()) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func isRolloutName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "rollout-") && (strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".jsonl.zst"))
}

// Read parses one rollout or one history.jsonl.
func (a *adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	rootDir := src.Extra[extraRoot]
	if rootDir == "" {
		rootDir = rootOf(src.Path)
	}
	idx, warnings := a.index.get(env, rootDir)

	var res adapters.ReadResult
	var err error
	if strings.EqualFold(filepath.Base(src.Path), historyName) {
		res, err = readHistory(ctx, src, idx)
	} else {
		res, err = readRollout(ctx, src, idx)
	}
	if err != nil {
		return adapters.ReadResult{}, err
	}
	res.Warnings = append(res.Warnings, warnings...)
	return res, nil
}

// rootOf finds the Codex root of a source path when Source.Extra lacks it:
// the parent of the "sessions" / "archived_sessions" folder, or the folder of history.jsonl.
func rootOf(path string) string {
	if strings.EqualFold(filepath.Base(path), historyName) {
		return filepath.Dir(path)
	}
	for dir := filepath.Dir(path); ; {
		base := strings.ToLower(filepath.Base(dir))
		if base == "sessions" || base == "archived_sessions" {
			return filepath.Dir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// errNoID is returned (as a warning) for a rollout without any session id.
var errNoID = errors.New("no session id in session_meta or file name")
