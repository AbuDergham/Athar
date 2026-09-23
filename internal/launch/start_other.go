//go:build !windows

package launch

import "errors"

// errWindowsOnly is returned by the parts of launch that need Windows.
var errWindowsOnly = errors.New("launch: only supported on Windows")

// Start opens a Windows Terminal tab (wt -w 0 new-tab ...) or, without wt.exe,
// a new console window; GUI apps are started directly. Windows only.
func Start(c Command) error { return errWindowsOnly }

// OpenFolder opens a folder in Explorer. Windows only.
func OpenFolder(path string) error { return errWindowsOnly }

// OpenFile opens a file with its default app (used for the reading page). Windows only.
func OpenFile(path string) error { return errWindowsOnly }

// CopyToClipboard puts text on the Windows clipboard. Windows only.
func CopyToClipboard(text string) error { return errWindowsOnly }

// processStart is not implemented outside Windows: no session is ever live.
func processStart(pid uint32) (uint64, bool) { return 0, false }
