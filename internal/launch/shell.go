package launch

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// shBare matches words a POSIX shell takes literally without quotes.
var shBare = regexp.MustCompile(`^[A-Za-z0-9_./:@%+,=-]+$`)

// shSafe reports whether s may be embedded in a single-quoted shell word. It
// refuses control characters (C0 and C1), invisible format characters (bidi
// controls, zero-width), and '\' and '!': fish reads \' inside single quotes
// and csh expands !, so without them '…' means the same text in sh, bash,
// zsh, fish and csh.
func shSafe(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r < 0x20 || r >= 0x7f && r <= 0x9f:
			return false
		case r == '\\' || r == '!':
			return false
		case unicode.Is(unicode.Cf, r):
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
// It returns "" when a value cannot be embedded safely (see shSafe) or the
// folder is not an absolute Unix path. Every value is quoted, so nothing in
// it is interpreted by the shell.
func shellLine(cwd, program string, args []string, env map[string]string) string {
	var parts []string
	if cwd != "" {
		if !strings.HasPrefix(cwd, "/") || !shSafe(cwd) {
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

// posixArgv is the argv that runs c in a new terminal on Linux and macOS:
//
//	/bin/sh -c <keepOpenScript> athar <dir> /usr/bin/env [-u VAR]… [VAR=v]… <program> <args>…
//
// No shell parses any value. The program must be an absolute path (never
// looked up from the session folder) without '=' (env would read it as a
// variable), and the folder an absolute path (cd would read "-" as $OLDPWD
// and search $CDPATH for a relative one).
func posixArgv(c Command) ([]string, error) {
	if !strings.HasPrefix(c.Program, "/") || strings.Contains(c.Program, "=") || strings.ContainsRune(c.Program, 0) {
		return nil, fmt.Errorf("launch: program %q: %w", c.Program, ErrUnsafe)
	}
	if !strings.HasPrefix(c.Dir, "/") || strings.ContainsRune(c.Dir, 0) {
		return nil, fmt.Errorf("launch: folder %q: %w", c.Dir, ErrUnsafe)
	}
	argv := []string{"/bin/sh", "-c", keepOpenScript, "athar", c.Dir, envProgram}
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
// neither the terminal Athar runs in nor $TERMINAL is one of them. Every form
// takes the command as separate arguments: terminals whose -e takes one
// string to re-parse (mate-terminal, tilix, terminator, …) and unknown ones
// are never used, since they could run a value through a shell.
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
		// "--": foot's getopt would otherwise take the command's -c as its own.
		return append(append(opt("--working-directory=", dir), opt("--title=", title)...), "--")
	}},
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
// Order: the terminal Athar runs in, $TERMINAL when it names a known terminal,
// then terminals in order.
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
	if name := strings.TrimSpace(getenv("TERMINAL")); name != "" {
		if p, err := look(name); err == nil {
			for _, t := range terminals {
				if filepath.Base(p) == t.name {
					return build(p, t), nil
				}
			}
		}
	}
	for _, t := range terminals {
		if p, err := look(t.name); err == nil {
			return build(p, t), nil
		}
	}
	return nil, fmt.Errorf("no supported terminal found: install gnome-terminal, konsole, xfce4-terminal, kitty, wezterm, alacritty, foot or xterm")
}

// macLine is what macOS Terminal types into the user's login shell: one
// `exec` of posixArgv(c) with every word single-quoted. Being a single
// command of quoted words (see shSafe) it means the same in sh, bash, zsh,
// fish and csh. "" when a value cannot be embedded.
func macLine(c Command) (string, error) {
	argv, err := posixArgv(c)
	if err != nil {
		return "", err
	}
	words := []string{"exec"}
	for _, a := range argv {
		if !shSafe(a) {
			return "", fmt.Errorf("launch: %q cannot be written for Terminal: %w", a, ErrUnsafe)
		}
		words = append(words, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(words, " "), nil
}

// macTerminalArgv is the osascript call that runs line (macLine) in a new
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
