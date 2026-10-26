//go:build unix

package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Start opens c in a new terminal; GUI apps are started directly. The
// program is resolved to an absolute path first, from the absolute PATH
// entries only, so nothing in the session folder runs in its place. On macOS
// a Terminal window runs macLine(c); elsewhere a terminal emulator runs
// posixArgv(c), so no shell parses any value (see shell.go). Start never
// waits for the program.
func Start(c Command) error {
	prog, err := absProgram(c.Program)
	if err != nil {
		return err
	}
	c.Program = prog
	if c.IsApp {
		return startApp(c)
	}
	var argv []string
	if runtime.GOOS == "darwin" {
		line, err := macLine(c)
		if err != nil {
			return err
		}
		argv = macTerminalArgv(line)
	} else {
		a, err := terminalArgv(c, getenv, lookPath)
		if err != nil {
			return err
		}
		argv = a
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if fi, err := os.Stat(c.Dir); err == nil && fi.IsDir() {
		cmd.Dir = c.Dir
	}
	cmd.Env = childEnv(c.Env)
	return startDetached(cmd)
}

// startApp starts a GUI program (c.Program is absolute) on its folder
// without a terminal.
func startApp(c Command) error {
	cmd := exec.Command(c.Program, c.Args...)
	if fi, err := os.Stat(c.Dir); err == nil && fi.IsDir() {
		cmd.Dir = c.Dir
	}
	cmd.Env = childEnv(c.Env)
	return startDetached(cmd)
}

// absProgram returns the absolute path of an executable program: p itself
// when absolute, else the first match in an absolute PATH entry. Relative
// entries ("", ".", "node_modules/.bin") are skipped: the new terminal starts
// in the session folder, where a planted program would otherwise win.
func absProgram(p string) (string, error) {
	notFound := fmt.Errorf("`%s` is not installed or not on PATH", programName(p))
	if p == "" {
		return "", notFound
	}
	if strings.Contains(p, "/") {
		if filepath.IsAbs(p) && isExecutable(p) {
			return p, nil
		}
		return "", notFound
	}
	for _, dir := range absPathList(getenv("PATH")) {
		if cand := filepath.Join(dir, p); isExecutable(cand) {
			return cand, nil
		}
	}
	return "", notFound
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// absPathList is the absolute entries of a PATH value, in order.
func absPathList(path string) []string {
	var out []string
	for _, dir := range filepath.SplitList(path) {
		if filepath.IsAbs(dir) {
			out = append(out, dir)
		}
	}
	return out
}

// childEnv is Athar's environment with the command's variables applied and
// PATH reduced to its absolute entries (the Unix side of what
// NoDefaultCurrentDirectoryInExePath does on Windows).
func childEnv(set map[string]string) []string {
	env := mergeEnv(os.Environ(), set)
	for i, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + strings.Join(absPathList(v), string(filepath.ListSeparator))
		}
	}
	return env
}

// startDetached starts cmd in its own session (it survives Athar and does not
// get Athar's terminal signals), with no standard streams, and reaps it when
// it exits.
func startDetached(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() //nolint:errcheck // nothing to report: the program runs on its own
	return nil
}

// opener is the program that opens folders and files with the desktop's
// default app.
func opener() (string, error) {
	if runtime.GOOS == "darwin" {
		return "/usr/bin/open", nil
	}
	if p, err := lookPath("xdg-open"); err == nil {
		return p, nil
	}
	return "", errors.New("`xdg-open` is not installed")
}

// OpenFolder opens a folder in the file manager.
func OpenFolder(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("Folder no longer exists: %s", path)
	}
	return openPath(abs)
}

// OpenFile opens a file with its default app (used for the reading page). It
// never opens executables or scripts: only the types in openableExt.
func OpenFile(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("launch: %s is not a file", abs)
	}
	if !openableExt[strings.ToLower(filepath.Ext(abs))] {
		return fmt.Errorf("launch: will not open %s: %w", filepath.Base(abs), ErrUnsafe)
	}
	return openPath(abs)
}

// openPath hands an absolute path (it starts with '/', so it is never read
// as an option) to the opener.
func openPath(abs string) error {
	if !strings.HasPrefix(abs, "/") || strings.ContainsRune(abs, 0) {
		return fmt.Errorf("launch: path %q: %w", abs, ErrUnsafe)
	}
	o, err := opener()
	if err != nil {
		return err
	}
	return startDetached(exec.Command(o, abs))
}

// clipboardTools are tried in order; the text goes to their standard input.
// wl-copy needs a Wayland session, the others an X display.
var clipboardTools = []struct {
	name, needs string
	args        []string
}{
	{"pbcopy", "", nil},
	{"wl-copy", "WAYLAND_DISPLAY", nil},
	{"xclip", "DISPLAY", []string{"-selection", "clipboard", "-in"}},
	{"xsel", "DISPLAY", []string{"--clipboard", "--input"}},
}

// CopyToClipboard puts text on the clipboard through pbcopy (macOS),
// wl-copy, xclip or xsel.
func CopyToClipboard(text string) error {
	for _, t := range clipboardTools {
		if t.needs != "" && getenv(t.needs) == "" {
			continue
		}
		p, err := lookPath(t.name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, p, t.args...)
		cmd.Stdin = strings.NewReader(text)
		// No pipes for stdout/stderr: xclip and wl-copy keep running in the
		// background to serve the clipboard, and Run must not wait for them.
		err = cmd.Run()
		cancel()
		if err != nil {
			return fmt.Errorf("copy to the clipboard with %s: %w", t.name, err)
		}
		return nil
	}
	if runtime.GOOS == "darwin" {
		return errors.New("`pbcopy` was not found")
	}
	return errors.New("no clipboard tool found: install wl-clipboard (Wayland) or xclip (X11)")
}

// processStart is not implemented on Unix yet (plan phase 2: Claude's
// procStart format there is unknown): no session is ever reported live.
func processStart(pid uint32) (uint64, bool) { return 0, false }
