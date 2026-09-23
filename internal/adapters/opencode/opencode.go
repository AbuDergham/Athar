// Package opencode reads OpenCode's local history (spec §7), read-only.
//
// Where: OpenCode's data folder is %USERPROFILE%\.local\share\opencode (or
// %XDG_DATA_HOME%\opencode; both are read when XDG_DATA_HOME is set, since
// Athar's environment may differ from OpenCode's). Two store generations can
// coexist there without overlap, and both are read:
//
//   - SQLite (OpenCode 1.2+): opencode.db, or opencode-<channel>.db for
//     non-latest channels, plus OPENCODE_DB (absolute, or relative to the
//     data folder). Tables session, message and part; JSON lives in the
//     message.data and part.data TEXT columns. Databases are only read from a
//     private copy made by fsread.CopySQLite. OpenCode's schema changes often,
//     so tables and columns are detected before querying (see ReadDB).
//   - Legacy JSON tree (up to 1.1.x) under storage\ (see legacy.go).
//
// Mapping: NativeID = session id ("ses_…"); Cwd = session.directory (the
// database stores forward slashes, C:/…, which become backslashes on Windows;
// the legacy tree already has backslashes); Title = session.title
// (TitleAgent: OpenCode generates it and a rename cannot be told apart),
// except the "New session - <ISO time>" placeholder, which falls back to the
// first prompt. Timestamps are Unix milliseconds. Sessions with a parent id
// are subagent sessions and are skipped, as are sessions with no readable
// message. Parts: text → message text (synthetic/ignored parts, which carry
// file contents OpenCode inlines, are dropped); tool → a tool_call
// ("name: command/filePath/pattern/url/query/…") and a tool_output (output or
// error, first 2 KB); file → "[image]" / "[file: name]"; reasoning, step
// markers, patches and snapshots are dropped. Assistant messages flagged
// summary=true (compaction summaries) are dropped.
//
// The same reader serves Kilo 7 (kilo.db): see ReadDB.
package opencode

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

type adapter struct{}

// New returns the OpenCode adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return "opencode" }
func (adapter) Name() string { return "OpenCode" }
func (adapter) Version() int { return 1 }

// Caps: resume is not enabled yet (spec §13.3). The command would be
// `opencode --session {id}` run in the session's folder, with
// ProgramCandidates %LOCALAPPDATA%\opencode\opencode-cli.exe. On the owner's
// PC no opencode CLI is on PATH; the only CLI is the desktop app's bundled
// opencode-cli.exe 1.1.54, which writes the legacy JSON store, while the
// SQLite sessions were written by a newer build. Whether that CLI can resume
// them (or would migrate or refuse the database) is untested, and running it
// against the real database is not allowed, so Resume stays nil until a
// manual test against a copy proves it works.
func (adapter) Caps() adapters.Caps { return adapters.Caps{} }

// Sources lists every OpenCode database and every legacy session file.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	seen := map[string]bool{}
	addDB := func(path string) {
		key := strings.ToLower(filepath.Clean(path))
		if seen[key] {
			return
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return
		}
		seen[key] = true
		out = append(out, adapters.Source{
			Adapter: "opencode",
			Path:    path,
			Files:   fsread.SQLiteSidecars(path),
			Extra:   map[string]string{"store": "sqlite"},
		})
	}

	roots := dataRoots(env)
	for _, root := range roots {
		for _, db := range databases(root) {
			addDB(db)
		}
	}
	if p := getenv(env, "OPENCODE_DB"); p != "" {
		if !filepath.IsAbs(p) && len(roots) > 0 {
			p = filepath.Join(roots[0], p) // OpenCode resolves it against its data folder
		}
		if filepath.IsAbs(p) {
			addDB(p)
		}
	}
	for _, root := range roots {
		for _, s := range legacySources(root) {
			key := strings.ToLower(filepath.Clean(s.Path))
			if !seen[key] {
				seen[key] = true
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// Read parses one database or one legacy session file.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if src.Extra["store"] == "legacy" || (src.Extra["store"] == "" && strings.EqualFold(filepath.Ext(src.Path), ".json")) {
		return readLegacy(ctx, src.Path)
	}
	sessions, skipped, err := readDBFile(ctx, src.Path, "opencode", env.TempDir)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	return adapters.ReadResult{Sessions: sessions, Skipped: skipped}, nil
}

// dataRoots returns OpenCode's data folders: %XDG_DATA_HOME%\opencode first
// (what OpenCode itself uses when the variable is set), then the default
// %USERPROFILE%\.local\share\opencode. Missing folders are left out.
func dataRoots(env adapters.Env) []string {
	var cands []string
	if xdg := getenv(env, "XDG_DATA_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		cands = append(cands, filepath.Join(xdg, "opencode"))
	}
	if env.Home != "" {
		cands = append(cands, filepath.Join(env.Home, ".local", "share", "opencode"))
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range cands {
		key := strings.ToLower(filepath.Clean(c))
		if seen[key] {
			continue
		}
		if info, err := os.Stat(c); err != nil || !info.IsDir() {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// databases returns opencode.db and opencode-<channel>.db in root, sorted.
func databases(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if e.Type().IsRegular() && (name == "opencode.db" || strings.HasPrefix(name, "opencode-") && strings.HasSuffix(name, ".db")) {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

func getenv(env adapters.Env, key string) string {
	if env.Getenv == nil {
		return ""
	}
	return strings.TrimSpace(env.Getenv(key))
}
