package launch

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// shBare matches words a POSIX shell takes literally without quotes.
var shBare = regexp.MustCompile(`^[A-Za-z0-9_./:@%+,=-]+$`)

// shSafe reports whether s may be embedded in an sh command line: valid UTF-8
// without control characters (single quotes protect everything else).
func shSafe(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// quoteSh returns s as one sh word: bare when shBare allows it, otherwise in
// single quotes with every ' written '\”.
func quoteSh(s string) string {
	if s != "" && shBare.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellLine builds the copyable sh form (Linux, macOS), also what macOS
// Terminal runs:
//
//	cd -- '<cwd>' && unset VAR && export VAR='v' && program args…
//
// It returns "" when a value cannot be embedded safely (control characters).
// Every value is quoted, so nothing in it is interpreted by the shell.
func shellLine(cwd, program string, args []string, env map[string]string) string {
	var parts []string
	if cwd != "" {
		if !shSafe(cwd) {
			return ""
		}
		parts = append(parts, "cd -- "+quoteSh(cwd))
	}
	for _, k := range sortedKeys(env) {
		v := env[k]
		switch {
		case !envNamePattern.MatchString(k) || !shSafe(v):
			return ""
		case v == "":
			parts = append(parts, "unset "+k)
		default:
			parts = append(parts, "export "+k+"="+quoteSh(v))
		}
	}
	if program == "" || !shSafe(program) {
		return ""
	}
	cmd := []string{quoteSh(program)}
	for _, a := range args {
		if !shSafe(a) {
			return ""
		}
		cmd = append(cmd, quoteSh(a))
	}
	return strings.Join(append(parts, strings.Join(cmd, " ")), " && ")
}

// keepOpenScript runs in the new terminal as `sh -c keepOpenScript athar
// <dir> <argv…>`: it enters the folder, runs argv, then starts the user's
// shell so the window stays open (like `cmd /k` on Windows). The script is a
// constant: every value reaches it as a positional parameter, never as text.
const keepOpenScript = `cd -- "$1" && shift && "$@"; exec "${SHELL:-/bin/sh}"`

// envProgram sets the command's variables for the program it runs. Both GNU
// and BSD env accept -u NAME and NAME=VALUE operands.
const envProgram = "/usr/bin/env"

// posixArgv is the argv that runs c in a new terminal on Linux:
//
//	/bin/sh -c <keepOpenScript> athar <dir> /usr/bin/env [-u VAR]… [VAR=v]… <program> <args>…
//
// No shell parses any value. The program must not start with '-' or contain
// '=' (env would read it as an option or a variable).
func posixArgv(c Command) ([]string, error) {
	if c.Program == "" || strings.HasPrefix(c.Program, "-") || strings.Contains(c.Program, "=") {
		return nil, fmt.Errorf("launch: program %q: %w", c.Program, ErrUnsafe)
	}
	dir := c.Dir
	if dir == "" {
		dir = "."
	}
	argv := []string{"/bin/sh", "-c", keepOpenScript, "athar", dir, envProgram}
	keys := sortedKeys(c.Env)
	for _, k := range keys {
		if !envNamePattern.MatchString(k) {
			return nil, fmt.Errorf("launch: environment variable name %q: %w", k, ErrUnsafe)
		}
		if c.Env[k] == "" {
			argv = append(argv, "-u", k)
		}
	}
	for _, k := range keys {
		if v := c.Env[k]; v != "" {
			if strings.ContainsRune(v, 0) {
				return nil, fmt.Errorf("launch: environment variable %s: %w", k, ErrUnsafe)
			}
			argv = append(argv, k+"="+v)
		}
	}
	argv = append(argv, c.Program)
	for _, a := range c.Args {
		if strings.ContainsRune(a, 0) {
			return nil, fmt.Errorf("launch: argument %q: %w", a, ErrUnsafe)
		}
	}
	return append(argv, c.Args...), nil
}

// terminal is a Linux terminal emulator and how to run a command in it.
type terminal struct {
	name string // program on PATH
	// env, when set, is a variable the terminal sets in its own shells: Athar
	// running inside it prefers it, so the session opens next to Athar.
	env string
	// args returns the terminal's arguments before the command (dir is
	// absolute or "", title already sanitized or "").
	args func(dir, title string) []string
}

// terminals lists the Linux terminals Athar knows, in the order tried when
// neither $TERMINAL nor the terminal Athar runs in is available. Every form
// takes the command as separate arguments (no shell string).
var terminals = []terminal{
	{"gnome-terminal", "GNOME_TERMINAL_SCREEN", func(dir, _ string) []string {
		return append(append([]string{"--tab"}, opt("--working-directory=", dir)...), "--")
	}},
	{"konsole", "KONSOLE_VERSION", func(dir, _ string) []string {
		return append(append([]string{"--new-tab"}, pair("--workdir", dir)...), "-e")
	}},
	{"xfce4-terminal", "", func(dir, title string) []string {
		return append(append(append([]string{"--tab"}, opt("--title=", title)...), opt("--working-directory=", dir)...), "-x")
	}},
	{"kitty", "KITTY_WINDOW_ID", func(dir, title string) []string {
		return append(pair("--directory", dir), pair("--title", title)...)
	}},
	{"wezterm", "WEZTERM_PANE", func(dir, _ string) []string {
		return append(append([]string{"start"}, pair("--cwd", dir)...), "--")
	}},
	{"alacritty", "ALACRITTY_WINDOW_ID", func(dir, title string) []string {
		return append(append(pair("--working-directory", dir), pair("--title", title)...), "-e")
	}},
	{"foot", "", func(dir, title string) []string {
		return append(opt("--working-directory=", dir), opt("--title=", title)...)
	}},
	{"x-terminal-emulator", "", func(string, string) []string { return []string{"-e"} }},
	{"xterm", "", func(_, title string) []string { return append(pair("-T", title), "-e") }},
}

// opt is []string{flag+value}, or nothing for an empty value.
func opt(flag, value string) []string {
	if value == "" {
		return nil
	}
	return []string{flag + value}
}

// pair is []string{flag, value}, or nothing for an empty value.
func pair(flag, value string) []string {
	if value == "" {
		return nil
	}
	return []string{flag, value}
}

// terminalArgv picks the terminal for c and returns its full argv. getenv
// and look (exec.LookPath) are parameters so tests choose the system.
// Order: the terminal Athar runs in, $TERMINAL (with -e, the common
// convention), then terminals in order.
func terminalArgv(c Command, getenv func(string) string, look func(string) (string, error)) ([]string, error) {
	cmd, err := posixArgv(c)
	if err != nil {
		return nil, err
	}
	dir := c.Dir
	if !strings.HasPrefix(dir, "/") {
		dir = "" // the script enters the folder anyway
	}
	title := SanitizeTitle(c.Title)
	build := func(path string, t terminal) []string {
		return append(append([]string{path}, t.args(dir, title)...), cmd...)
	}
	for _, t := range terminals {
		if t.env != "" && getenv(t.env) != "" {
			if p, err := look(t.name); err == nil {
				return build(p, t), nil
			}
		}
	}
	if fields := strings.Fields(getenv("TERMINAL")); len(fields) > 0 {
		if p, err := look(fields[0]); err == nil {
			// A known terminal gets its own form: gnome-terminal's -e, for
			// one, takes a single string, not the command's arguments.
			for _, t := range terminals {
				if len(fields) == 1 && filepath.Base(p) == t.name {
					return build(p, t), nil
				}
			}
			return append(append(append([]string{p}, fields[1:]...), "-e"), cmd...), nil
		}
	}
	for _, t := range terminals {
		if p, err := look(t.name); err == nil {
			return build(p, t), nil
		}
	}
	return nil, fmt.Errorf("no terminal found: set $TERMINAL or install gnome-terminal, konsole, kitty or xterm")
}

// macTerminalArgv is the osascript call that runs line (shellLine) in a new
// macOS Terminal window. The line is passed as an argument (item 1 of argv),
// so it is never part of the AppleScript source.
func macTerminalArgv(line string) []string {
	return []string{"/usr/bin/osascript",
		"-e", "on run argv",
		"-e", `tell application "Terminal"`,
		"-e", "activate",
		"-e", "do script (item 1 of argv)",
		"-e", "end tell",
		"-e", "end run",
		line}
}
