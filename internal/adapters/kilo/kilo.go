// Package kilo reads the history of Kilo Code 7 (the OpenCode-based CLI and
// extension backend, spec §7), read-only.
//
// Where: Kilo's data folder is %USERPROFILE%\.local\share\kilo (or
// %XDG_DATA_HOME%\kilo; both are read when XDG_DATA_HOME is set, since Athar's
// environment may differ from Kilo's). The sessions live in SQLite:
//
//   - kilo.db for the latest/beta/prod channels, kilo-<channel>.db for other
//     channels, and opencode-<channel>.db, which Kilo keeps using when no
//     kilo-<channel>.db exists;
//   - KILO_DB, absolute or relative to the data folder (":memory:" is ignored).
//
// The schema is OpenCode's (tables session, message and part with JSON in
// their data columns), so the database is copied with fsread.CopySQLite and
// read by opencode.ReadDB; the mapping (ids, titles, subagent sessions,
// parts) is documented in package opencode. The log\, bin\ and repos\
// folders next to the database hold no conversations.
//
// The legacy Kilo Code 5.x task folders in VS Code-family hosts are a
// different store, read by package vscodetasks under the same agent id: its
// native ids are task UUIDs, never "ses_…", so the two do not collide.
package kilo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/opencode"
	"github.com/AbuDergham/Athar/internal/fsread"
)

type adapter struct{}

// New returns the Kilo Code 7 adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return "kilo" }
func (adapter) Name() string { return "Kilo Code" }
func (adapter) Version() int { return 1 }

// Caps: `kilo --session {id}` (spec §6.3, from kilo --help 7.4.21), run in the
// session's folder. The CLI is installed by npm (@kilocode/cli) as kilo.cmd;
// the copy bundled with the editor extension sits in a versioned folder that
// cannot be listed here. RootEnv KILO_DB points Kilo at the database a
// session came from when it is not the default kilo.db (sessions of a
// channel database or of a KILO_DB database carry that path as Root).
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		Resume:            []string{"kilo", "--session", "{id}"},
		ProgramCandidates: []string{`%APPDATA%\npm\kilo.cmd`},
		RootEnv:           "KILO_DB",
	}
}

// Sources lists every Kilo 7 database: one source per database file.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	defaultDB := ""
	if env.Home != "" {
		defaultDB = key(filepath.Join(env.Home, ".local", "share", "kilo", "kilo.db"))
	}
	var out []adapters.Source
	seen := map[string]bool{}
	add := func(path string) {
		k := key(path)
		if seen[k] {
			return
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return
		}
		seen[k] = true
		root := path
		if k == defaultDB {
			root = "" // Kilo's own default: KILO_DB is cleared on resume
		}
		out = append(out, adapters.Source{
			Adapter: "kilo",
			Path:    path,
			Files:   fsread.SQLiteSidecars(path),
			Root:    root,
		})
	}

	roots := dataRoots(env)
	for _, root := range roots {
		for _, db := range databases(root) {
			add(db)
		}
	}
	if p := getenv(env, "KILO_DB"); p != "" && p != ":memory:" {
		if !filepath.IsAbs(p) && len(roots) > 0 {
			p = filepath.Join(roots[0], p) // Kilo resolves it against its data folder
		}
		if filepath.IsAbs(p) {
			add(p)
		}
	}
	return out, nil
}

// Read copies one database with fsread.CopySQLite into env.TempDir, reads the
// copy with opencode.ReadDB and removes it. Sessions carry the source's Root.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	cp, cleanup, err := fsread.CopySQLite(src.Path, env.TempDir)
	defer cleanup()
	if err != nil {
		return adapters.ReadResult{}, err
	}
	sessions, skipped, err := opencode.ReadDB(cp, "kilo")
	if err != nil {
		return adapters.ReadResult{}, fmt.Errorf("%s: %w", src.Path, err)
	}
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	for i := range sessions {
		sessions[i].Root = src.Root
	}
	return adapters.ReadResult{Sessions: sessions, Skipped: skipped}, nil
}

// dataRoots returns Kilo's data folders: %XDG_DATA_HOME%\kilo first (what
// Kilo itself uses when the variable is set), then the default
// %USERPROFILE%\.local\share\kilo. Missing folders are left out.
func dataRoots(env adapters.Env) []string {
	var cands []string
	if xdg := getenv(env, "XDG_DATA_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		cands = append(cands, filepath.Join(xdg, "kilo"))
	}
	if env.Home != "" {
		cands = append(cands, filepath.Join(env.Home, ".local", "share", "kilo"))
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range cands {
		k := key(c)
		if seen[k] {
			continue
		}
		if info, err := os.Stat(c); err != nil || !info.IsDir() {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

// databases returns kilo.db, kilo-<channel>.db and opencode-<channel>.db in
// root, sorted by name.
func databases(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := strings.ToLower(e.Name())
		if name == "kilo.db" || isChannelDB(name, "kilo-") || isChannelDB(name, "opencode-") {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// isChannelDB reports whether name (lower case) is <prefix><channel>.db.
func isChannelDB(name, prefix string) bool {
	return strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".db") && len(name) > len(prefix)+len(".db")
}

// key is the case-insensitive identity of a Windows path.
func key(path string) string { return strings.ToLower(filepath.Clean(path)) }

func getenv(env adapters.Env, name string) string {
	if env.Getenv == nil {
		return ""
	}
	return strings.TrimSpace(env.Getenv(name))
}
