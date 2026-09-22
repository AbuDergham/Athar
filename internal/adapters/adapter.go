// Package adapters contains one reader per AI agent. Every adapter is
// read-only: it never writes, creates or deletes anything in an agent's
// folders, and it never executes an agent binary. Files are read through
// internal/fsread, SQLite databases only through fsread.CopySQLite.
package adapters

import (
	"context"
	"os"
	"path/filepath"

	"github.com/AbuDergham/Athar/internal/model"
)

// Env tells adapters where to look. Tests point it at testdata trees.
type Env struct {
	Home         string // %USERPROFILE%
	AppData      string // %APPDATA%
	LocalAppData string // %LOCALAPPDATA%
	Documents    string // the user's Documents folder
	TempDir      string // private temp dir for SQLite copies (created by sync)
	Getenv       func(string) string
}

// CurrentEnv returns the Env of the running user.
func CurrentEnv(tempDir string) Env {
	home, _ := os.UserHomeDir()
	docs := filepath.Join(home, "Documents")
	return Env{
		Home:         home,
		AppData:      os.Getenv("APPDATA"),
		LocalAppData: os.Getenv("LOCALAPPDATA"),
		Documents:    docs,
		TempDir:      tempDir,
		Getenv:       os.Getenv,
	}
}

// Source is one unit that sync fingerprints and reads: a file, a database,
// or a folder (a Cline task folder, Antigravity's index + brain folders).
type Source struct {
	Adapter string
	// Path identifies the source (unique per machine). For folder sources it is the folder.
	Path string
	// Files are the files whose (path, size, mtime) make up the fingerprint.
	// For single-file sources this is []string{Path}. SQLite sources list db, -wal, -shm (existing ones).
	Files []string
	// Root is the agent data root for this source ("" = default root).
	Root string
	// Extra carries adapter-specific hints (e.g. the host editor name for VS Code-family stores).
	Extra map[string]string
}

// SyncState is what the archive remembers about a source from the last sync.
type SyncState struct {
	Fingerprint    string
	AdapterVersion int
}

// ReadResult is what an adapter returns for one source.
type ReadResult struct {
	Sessions []model.Session
	Skipped  int      // records that could not be parsed (skipped, never fatal)
	Warnings []string // shown on the Sources screen
}

// AppCommand starts a GUI app on a folder. Args may contain "{cwd}".
type AppCommand struct {
	Name    string   // "Cursor", "VS Code", ...
	Program string   // program name on PATH, or an absolute path
	Args    []string // e.g. []string{"{cwd}"}
	// Candidates are absolute install paths tried when Program is not on PATH.
	// They may contain %LOCALAPPDATA%, %APPDATA%, %USERPROFILE% and %ProgramFiles%.
	Candidates []string
}

// Caps describes what Athar can do with an agent's sessions.
type Caps struct {
	// Resume is the program and arguments to resume a session, with "{id}".
	// nil when the agent cannot resume a specific session from a terminal.
	Resume []string
	// ProgramCandidates are absolute paths tried when Resume[0] is not on PATH.
	ProgramCandidates []string
	Fork              []string // extra arguments for a fork; nil if unsupported
	SkipPermissions   []string // extra arguments to skip permission prompts; nil if none
	App               *AppCommand
	// RootEnv is the environment variable that selects the agent's data root
	// (e.g. "CLAUDE_CONFIG_DIR"). Launch sets it for non-default roots and clears it otherwise.
	RootEnv string
}

// Adapter reads one agent's local history.
type Adapter interface {
	ID() string   // stable id stored in the archive, e.g. "claude"
	Name() string // display name, e.g. "Claude Code"
	// Version is bumped whenever parsing changes; sources are then re-read in full.
	Version() int
	// Sources lists what to read. Missing locations are skipped silently.
	Sources(env Env) ([]Source, error)
	// Read parses one source. It must be tolerant: bad records are counted in
	// Skipped, never returned as an error. An error means the whole source failed.
	Read(ctx context.Context, env Env, src Source) (ReadResult, error)
	Caps() Caps
}
