package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/registry"
	"github.com/AbuDergham/Athar/internal/syncer"
	"github.com/AbuDergham/Athar/internal/tui"
	"github.com/AbuDergham/Athar/internal/update"
)

// system is everything run touches besides its arguments and the data
// folder, so tests can replace it.
type system struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// console: stdin is a console, so Athar may ask a question or wait for
	// Enter (a double-clicked window would otherwise vanish, spec §2.8).
	console bool
	// progressTTY: stdout is a console, so --sync shows a progress counter.
	progressTTY bool

	goos          string // runtime.GOOS; tests pick the system they need
	executable    func() (string, error)
	homeDir       func() (string, error)
	getenv        func(string) string
	tempDir       func() string
	now           func() time.Time
	machineID     func() string
	hostname      func() (string, error)
	cleanupOld    func()
	runTUI        func(tui.App) error
	sync          func(ctx context.Context, db *archive.DB, opts syncer.Options, progress func(syncer.Progress)) (syncer.Report, error)
	restart       func(exe string, args []string) int
	signalContext func() (context.Context, context.CancelFunc)

	in *bufio.Reader // stdin, buffered once so no typed-ahead input is lost
}

// realSystem is the running process.
func realSystem() *system {
	return &system{
		stdin:       os.Stdin,
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		console:     isConsole(os.Stdin),
		progressTTY: isConsole(os.Stdout),
		goos:        runtime.GOOS,
		executable:  executablePath,
		homeDir:     os.UserHomeDir,
		getenv:      os.Getenv,
		tempDir:     os.TempDir,
		now:         time.Now,
		machineID:   syncer.MachineID,
		hostname:    os.Hostname,
		cleanupOld:  update.CleanupOld,
		runTUI:      tui.Run,
		sync:        syncAll,
		restart:     restartExe,
		signalContext: func() (context.Context, context.CancelFunc) {
			return signal.NotifyContext(context.Background(), os.Interrupt)
		},
	}
}

// executablePath is athar.exe's absolute path, symlinks resolved.
func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Abs(exe)
}

// syncAll runs every registered adapter once, as the background sync of the UI does.
func syncAll(ctx context.Context, db *archive.DB, opts syncer.Options, progress func(syncer.Progress)) (syncer.Report, error) {
	return syncer.Run(ctx, db, registry.All(), adapters.CurrentEnv(""), opts, progress)
}

// restartExe starts the updated athar.exe in this console with args, waits
// for it and returns its exit code. Waiting keeps the window (or Windows
// Terminal tab) open, which closes when its first process exits.
func restartExe(exe string, args []string) int {
	signal.Ignore(os.Interrupt) // Ctrl+C belongs to the new process now
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "Athar was updated but could not restart: %v\nStart athar.exe again.\n", err)
	return 1
}

// readLine reads one line of input ("" at end of input or on error).
func (s *system) readLine() string {
	if s.in == nil {
		s.in = bufio.NewReader(s.stdin)
	}
	line, _ := s.in.ReadString('\n')
	return strings.TrimSpace(line)
}

// waitEnter prints prompt and waits for Enter when stdin is a console.
func (s *system) waitEnter(prompt string) {
	if !s.console {
		return
	}
	fmt.Fprint(s.stderr, prompt)
	s.readLine()
}

// fatal prints err, waits for Enter when a window would vanish (never for
// --sync, which runs unattended) and returns exit code 1.
func (s *system) fatal(o options, err error) int {
	fmt.Fprintln(s.stderr, err)
	if !o.sync {
		s.waitEnter("Press Enter to close.")
	}
	return 1
}

// yes reports whether an answer confirms: y or yes, or غ, the character of
// the Y key on the Arabic (101) layout (spec §2.3).
func yes(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes", "غ":
		return true
	}
	return false
}
