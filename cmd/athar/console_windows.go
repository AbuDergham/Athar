//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// isConsole reports whether f is a Windows console (not a pipe, a file or a
// mintty pseudo-terminal).
func isConsole(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
