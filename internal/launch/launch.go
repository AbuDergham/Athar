// Package launch builds and starts resume/open commands (spec §6). On Linux
// and macOS (plan 2026-10-26-linux-macos.md, phase 3) the same Command runs
// in a new terminal through argv (Linux) or an sh line (macOS Terminal), see
// shell.go and start_unix.go.
//
// Build turns a session and its agent's caps into a Command: a validated
// one-token command line for `cmd /d /s /k` (CmdLine), a copyable PowerShell
// form, the working directory and the environment. Values from agent data are
// embedded in command text only when they pass the whitelists of §6.1; anything
// else travels through the working directory or the environment block.
// Preflight checks folder, drive and program (lookup only) and Start opens a
// Windows Terminal tab, a console window or a GUI app.
package launch

import (
	"runtime"
	"time"
)

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
	// Shell is the sh form (Linux, macOS): what the C action copies there and
	// what macOS Terminal runs. "" when a value cannot be embedded safely.
	Shell string
}

// CopyText is the command the C action copies: the PowerShell form on
// Windows, the sh form elsewhere ("" when there is none).
func (c Command) CopyText() string {
	if runtime.GOOS == "windows" {
		return c.PowerShell
	}
	return c.Shell
}

// CopyFormat names the form CopyText returns, for the action menu.
func CopyFormat() string {
	if runtime.GOOS == "windows" {
		return "PowerShell"
	}
	return "sh"
}

// LiveInfo describes a Claude session that is running now (spec §6.5).
type LiveInfo struct {
	PID         int
	Interactive bool
	StartedAt   time.Time
}

// openableExt lists the file types OpenFile hands to their default app; it
// never opens executables, scripts or shortcuts.
var openableExt = map[string]bool{".html": true, ".htm": true, ".txt": true, ".md": true}
