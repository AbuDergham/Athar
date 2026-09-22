// Package launch builds and starts resume/open commands on Windows (spec §6).
package launch

import (
	"errors"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
)

var errTODO = errors.New("launch: not implemented")

// Action is what the user picked in the action menu.
type Action int

const (
	ActionResume Action = iota
	ActionFork
)

// Target is the session to act on.
type Target struct {
	Agent    string
	NativeID string
	Title    string
	Cwd      string
	Root     string // agent data root ("" = default)
}

// Command is a fully prepared launch.
type Command struct {
	Title   string            // tab/window title (already sanitized)
	Dir     string            // working directory
	Env     map[string]string // variables to set ("" value = clear)
	Program string            // resolved program (name on PATH or absolute path)
	Args    []string
	IsApp   bool   // GUI app (started directly, no terminal)
	AppName string // for the "Opened <project> in <App>" message

	CmdLine    string // the one-token command for `cmd /d /s /k` (validated)
	PowerShell string // copyable PowerShell form (§6.4)
}

// Build prepares the command for a target using the agent's caps and optional
// overrides from athar.toml. skip adds the agent's skip-permissions arguments.
// It validates every embedded value (§6.1) and returns an error if a value is unsafe
// and cannot be passed another way.
func Build(t Target, caps adapters.Caps, action Action, skip bool, override []string) (Command, error) {
	return Command{}, errTODO
}

// Preflight checks the folder, the drive and that the program exists (lookup only).
func Preflight(c Command) error { return errTODO }

// Start opens a Windows Terminal tab (wt -w 0 new-tab ...) or, without wt.exe,
// a new console window; GUI apps are started directly.
func Start(c Command) error { return errTODO }

// CopyToClipboard puts text on the Windows clipboard.
func CopyToClipboard(text string) error { return errTODO }

// OpenFolder opens a folder in Explorer.
func OpenFolder(path string) error { return errTODO }

// OpenFile opens a file with its default app (used for the reading page).
func OpenFile(path string) error { return errTODO }

// LiveInfo describes a Claude session that is running now (spec §6.5).
type LiveInfo struct {
	PID         int
	Interactive bool
	StartedAt   time.Time
}

// LiveClaudeSessions reads <root>\sessions\*.json for every Claude root and returns
// live sessions by sessionId. A record is live only if the pid exists and its creation
// time equals procStart.
func LiveClaudeSessions(roots []string) map[string]LiveInfo { return map[string]LiveInfo{} }
