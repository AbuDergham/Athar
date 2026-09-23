// Package launch builds and starts resume/open commands on Windows (spec §6).
//
// Build turns a session and its agent's caps into a Command: a validated
// one-token command line for `cmd /d /s /k` (CmdLine), a copyable PowerShell
// form, the working directory and the environment. Values from agent data are
// embedded in command text only when they pass the whitelists of §6.1; anything
// else travels through the working directory or the environment block.
// Preflight checks folder, drive and program (lookup only) and Start opens a
// Windows Terminal tab, a console window or a GUI app.
package launch

import "time"

// Action is what the user picked in the action menu.
type Action int

const (
	ActionResume Action = iota // continue the session (R)
	ActionFork                 // resume as a new session id (F); needs Caps.Fork
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

// LiveInfo describes a Claude session that is running now (spec §6.5).
type LiveInfo struct {
	PID         int
	Interactive bool
	StartedAt   time.Time
}
