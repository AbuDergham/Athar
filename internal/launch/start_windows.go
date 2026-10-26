//go:build windows

package launch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// Start opens a Windows Terminal tab (wt -w 0 new-tab ...) or, without wt.exe,
// a new console window; GUI apps are started directly.
//
// Start never waits for the program. With wt.exe the command line is built by
// hand (syscall.EscapeArg per argument) so Go does not re-quote it; without it,
// cmd.exe gets CREATE_NEW_CONSOLE and `cmd.exe /d /s /k "title <T> && <CmdLine>"`.
func Start(c Command) error {
	if c.IsApp {
		return startApp(c)
	}
	env := childEnv(c.Env)
	if wt, err := lookPath("wt.exe"); err == nil {
		cmd := exec.Command(wt)
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: wtCommandLine(wt, WTArgs(c))}
		cmd.Env = env
		return startDetached(cmd)
	}
	cmd := exec.Command(systemCmd())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       consoleCmdLine(c, "/k"),
		CreationFlags: windows.CREATE_NEW_CONSOLE,
	}
	cmd.Dir = c.Dir
	cmd.Env = env
	return startDetached(cmd)
}

// wtCommandLine joins wt.exe and its arguments with syscall.EscapeArg, the
// quoting that CommandLineToArgvW (used by wt.exe) undoes exactly.
func wtCommandLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, syscall.EscapeArg(exe))
	for _, a := range args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}

// startApp starts a GUI program on its folder without a terminal. A .cmd/.bat
// shim (e.g. VS Code's code.cmd) runs through a hidden `cmd.exe /d /s /c`, with
// the same validated quoting as the terminal command line.
func startApp(c Command) error {
	path, err := lookPath(c.Program)
	if err != nil {
		if !programExists(c.Program) {
			return fmt.Errorf("`%s` is not installed or not on PATH", programName(c.Program))
		}
		path = c.Program
	}
	if !cmdSafe(path) {
		return fmt.Errorf("launch: program %q: %w", path, ErrUnsafe)
	}
	line := quoteCmd(path)
	for _, a := range c.Args {
		if !cmdSafe(a) {
			return fmt.Errorf("launch: argument %q: %w", a, ErrUnsafe)
		}
		line += " " + quoteCmd(a)
	}
	var cmd *exec.Cmd
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		cmd = exec.Command(systemCmd())
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + line + `"`}
	default:
		cmd = exec.Command(path)
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	}
	cmd.SysProcAttr.CreationFlags = windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP
	if fi, err := os.Stat(c.Dir); err == nil && fi.IsDir() {
		cmd.Dir = c.Dir
	}
	cmd.Env = childEnv(c.Env)
	return startDetached(cmd)
}

// startDetached starts cmd and lets it run on its own.
func startDetached(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// systemCmd is %SystemRoot%\System32\cmd.exe (never a cmd.exe found in the
// current folder or early on PATH).
func systemCmd() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "cmd.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "cmd.exe"
}

// OpenFolder opens a folder in Explorer.
func OpenFolder(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("Folder no longer exists: %s", path)
	}
	if strings.ContainsAny(abs, "\"\r\n\x00") {
		return fmt.Errorf("launch: folder %q: %w", abs, ErrUnsafe)
	}
	exe := "explorer.exe"
	if root := os.Getenv("SystemRoot"); root != "" {
		exe = filepath.Join(root, "explorer.exe")
	}
	// Explorer treats ',' as a separator, so the path is always quoted.
	n := len(abs) - len(strings.TrimRight(abs, `\`))
	cmd := exec.Command(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe "` + abs + strings.Repeat(`\`, n) + `"`}
	return startDetached(cmd)
}

// OpenFile opens a file with its default app (used for the reading page).
// It uses ShellExecute("open"), so no command line is parsed.
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
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// ShellExecute may use COM; S_FALSE (already initialized) also needs CoUninitialize.
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil || errors.Is(err, syscall.Errno(1)) {
		defer windows.CoUninitialize()
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// childEnv is Athar's environment with the command's variables applied and
// NoDefaultCurrentDirectoryInExePath=1, so no program is taken from the
// session folder ahead of PATH.
func childEnv(set map[string]string) []string {
	env := make(map[string]string, len(set)+1)
	for k, v := range set {
		env[k] = v
	}
	env[noCwdSearchEnv] = "1"
	return mergeEnv(os.Environ(), env)
}
