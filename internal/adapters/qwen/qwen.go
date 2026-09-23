// Package qwen reads Qwen Code history (spec §7).
//
// Roots: %USERPROFILE%\.qwen, plus QWEN_RUNTIME_DIR and QWEN_HOME when set
// (they relocate the runtime folder that holds projects\ and tmp\). A
// non-default root is recorded in Session.Root; resuming sets QWEN_RUNTIME_DIR.
//
// Three storage generations, all read:
//
//   - 0.4+: projects\<sanitized-cwd>\chats\<session-id>.jsonl (and chats\archive\
//     for archived sessions). One ChatRecord per line {uuid, parentUuid,
//     sessionId, timestamp, type user|assistant|tool_result|system, subtype,
//     cwd, message{role, parts[]}, systemPayload, toolCallResult}. NativeID =
//     file stem; Cwd = the FIRST record's cwd (the folder name is lowercased and
//     lossy, never decoded). User text prefers systemPayload.displayText (the
//     typed prompt) over the expanded parts; thought parts, isSidechain
//     (subagent) records and non-conversation subtypes are skipped. Title = the
//     last system custom_title (titleSource "auto" → agent, else user), else
//     the first prompt. Files whose name is not a session id (.runtime.json,
//     .ledger.jsonl, …) are ignored.
//   - 0.0.14–0.3: tmp\<hash>\chats\session-<time>-<id8>.json, one whole-file
//     JSON conversation {sessionId, startTime, lastUpdated, messages[{type
//     user|qwen, content, toolCalls}]}.
//   - every version: tmp\<hash>\logs.json, a JSON array of the prompts typed in
//     that folder {sessionId, messageId, type, message, timestamp} → one
//     prompt-log session per session id (coverage prompts). Qwen 0.0.8 (the
//     version on the owner's PC) wrote only this file.
//
// <hash> is sha256 of the launch folder; see resolveHash for how Cwd is found.
// Whole-file JSON sources (logs.json, session-*.json) modified in the last 5 s
// fail with an error wrapping adapters.ErrRetryLater so the next sync reads
// them once they are complete.
package qwen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// Source kinds (Source.Extra["kind"]).
const (
	kindChat    = "chat"    // projects\<enc>\chats\<id>.jsonl
	kindSession = "session" // tmp\<hash>\chats\session-*.json
	kindLogs    = "logs"    // tmp\<hash>\logs.json
)

// freshWindow: whole-file JSON modified more recently than this may be half-written.
const freshWindow = 5 * time.Second

// now is the clock of the freshness rule; tests replace it.
var now = time.Now

// errTooFresh wraps adapters.ErrRetryLater: the syncer retries the source on
// the next sync without storing its fingerprint or showing an error.
var errTooFresh = fmt.Errorf("modified in the last 5 s: %w", adapters.ErrRetryLater)

var (
	chatFileRE    = regexp.MustCompile(`^[0-9a-fA-F-]{32,36}\.jsonl$`)
	sessionFileRE = regexp.MustCompile(`^(session-.+|[0-9a-fA-F-]{32,36})\.json$`)
)

type adapter struct{}

// New returns the Qwen Code adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return "qwen" }
func (adapter) Name() string { return "Qwen Code" }
func (adapter) Version() int { return 1 }

// qwenRoot is one runtime folder: dir holds projects\ and tmp\; root is the
// Session.Root value ("" for the default %USERPROFILE%\.qwen).
type qwenRoot struct{ dir, root string }

func roots(env adapters.Env) []qwenRoot {
	var out []qwenRoot
	add := func(dir, root string) {
		if dir == "" || !filepath.IsAbs(dir) {
			return
		}
		dir = filepath.Clean(dir)
		for _, r := range out {
			if samePath(r.dir, dir) {
				return
			}
		}
		if root != "" {
			root = dir
		}
		out = append(out, qwenRoot{dir, root})
	}
	if env.Home != "" {
		add(filepath.Join(env.Home, ".qwen"), "")
	}
	for _, name := range []string{"QWEN_RUNTIME_DIR", "QWEN_HOME"} {
		if env.Getenv == nil {
			break
		}
		if d := expandHome(strings.TrimSpace(env.Getenv(name)), env.Home); d != "" {
			add(d, d)
		}
	}
	return out
}

func expandHome(p, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`):
		if home == "" {
			return ""
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Sources lists every transcript, old session file and prompt log.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	add := func(r qwenRoot, path, kind string) {
		out = append(out, adapters.Source{
			Adapter: "qwen",
			Path:    path,
			Files:   []string{path},
			Root:    r.root,
			Extra:   map[string]string{"kind": kind, "base": r.dir},
		})
	}
	for _, r := range roots(env) {
		projects := filepath.Join(r.dir, "projects")
		for _, pd := range subdirs(projects) {
			chats := filepath.Join(projects, pd, "chats")
			for _, dir := range []string{chats, filepath.Join(chats, "archive")} {
				for _, f := range matchingFiles(dir, chatFileRE) {
					add(r, f, kindChat)
				}
			}
		}
		tmp := filepath.Join(r.dir, "tmp")
		for _, hd := range subdirs(tmp) {
			logs := filepath.Join(tmp, hd, "logs.json")
			if info, err := os.Stat(logs); err == nil && info.Mode().IsRegular() {
				add(r, logs, kindLogs)
			}
			for _, f := range matchingFiles(filepath.Join(tmp, hd, "chats"), sessionFileRE) {
				add(r, f, kindSession)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Read parses one source; see the package doc for the formats.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	kind, base := src.Extra["kind"], src.Extra["base"]
	if kind == "" {
		kind = kindOf(src.Path)
	}
	if base == "" {
		base = baseOf(src.Path, kind)
	}
	switch kind {
	case kindChat:
		return readChat(ctx, src)
	case kindLogs, kindSession:
		if err := settled(src.Path); err != nil {
			return adapters.ReadResult{}, err
		}
		if kind == kindLogs {
			return readLogs(env, src, base)
		}
		return readSessionJSON(env, src, base)
	}
	return adapters.ReadResult{}, fmt.Errorf("qwen: unknown source kind %q", kind)
}

func kindOf(path string) string {
	switch name := filepath.Base(path); {
	case strings.HasSuffix(name, ".jsonl"):
		return kindChat
	case name == "logs.json":
		return kindLogs
	}
	return kindSession
}

// baseOf derives the root folder from a source path: tmp\<hash>\logs.json or
// tmp\<hash>\chats\<file>.json.
func baseOf(path, kind string) string {
	up := func(p string, n int) string {
		for ; n > 0; n-- {
			p = filepath.Dir(p)
		}
		return p
	}
	if kind == kindSession {
		return up(path, 4)
	}
	return up(path, 3)
}

// settled rejects a whole-file JSON source written in the last freshWindow.
func settled(path string) error {
	_, mtime, err := fsread.Stat(path)
	if err != nil {
		return err
	}
	if age := now().Sub(mtime); age >= 0 && age < freshWindow {
		return errTooFresh
	}
	return nil
}

// Caps: `qwen --resume <id>` exists since 0.4 (the projects\ store). The
// installed CLI on a PC with only legacy prompt logs (0.0.8) cannot resume,
// so resume is offered only when a 0.4+ transcript exists.
func (adapter) Caps() adapters.Caps { return capsFor(adapters.CurrentEnv("")) }

func capsFor(env adapters.Env) adapters.Caps {
	c := adapters.Caps{
		ProgramCandidates: []string{`%APPDATA%\npm\qwen.cmd`},
		RootEnv:           "QWEN_RUNTIME_DIR",
	}
	if hasTranscripts(env) {
		c.Resume = []string{"qwen", "--resume", "{id}"}
		c.Fork = []string{"--fork-session"}
		c.SkipPermissions = []string{"--yolo"}
	}
	return c
}

func hasTranscripts(env adapters.Env) bool {
	for _, r := range roots(env) {
		projects := filepath.Join(r.dir, "projects")
		for _, pd := range subdirs(projects) {
			if len(matchingFiles(filepath.Join(projects, pd, "chats"), chatFileRE)) > 0 {
				return true
			}
		}
	}
	return false
}

// subdirs lists the sub-folder names of dir (none when it is missing).
func subdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// matchingFiles lists the regular files directly in dir whose name matches re.
func matchingFiles(dir string, re *regexp.Regexp) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && re.MatchString(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}
