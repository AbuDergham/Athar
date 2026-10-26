package launch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/AbuDergham/Athar/internal/adapters"
)

// hostile are values that would run commands, expand variables or split
// words if a shell interpreted them.
var hostile = []string{
	"it's", `"quoted"`, "a b  c", "$(touch pwned)", "`touch pwned`", "${HOME}", "$HOME",
	"; touch pwned", "&& touch pwned", "| touch pwned", "> pwned", "*", "~", `back\slash`,
	"-n", "--help", "مشروع قديم", "'; touch pwned; '", `\'`, "%PATH%", "!x", "#comment",
}

// shHostile is hostile without the characters shSafe refuses ('\\', '!').
func shHostile() []string {
	var out []string
	for _, h := range hostile {
		if !strings.ContainsAny(h, `\!`) {
			out = append(out, h)
		}
	}
	return out
}

func needSh(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
}

func TestQuoteShRoundTrip(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	for _, s := range append(hostile, "", "plain", "a/b.c-d_e:f@g%h+i,j=k") {
		out, err := exec.Command("/bin/sh", "-c", `printf '%s' `+quoteSh(s)).Output()
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("quoteSh(%q) = %s reads back as %q", s, quoteSh(s), out)
		}
	}
	assertNoPwned(t, dir)
}

// fakeProgram writes a script that records its folder, two variables and its
// arguments into out, one per line, and returns its path.
func fakeProgram(t *testing.T, dir, out string) string {
	t.Helper()
	p := filepath.Join(dir, "agent")
	script := "#!/bin/sh\n{ pwd; printf '%s\\n' \"${CLAUDE_CONFIG_DIR-unset}\" \"${CODEX_HOME-unset}\"; " +
		"for a in \"$@\"; do printf '<%s>\\n' \"$a\"; done; } > " + quoteSh(out) + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertNoPwned(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range append(dirs, ".") {
		if _, err := os.Stat(filepath.Join(d, "pwned")); err == nil {
			t.Fatalf("a value was executed: %s/pwned exists", d)
		}
	}
}

// want is what fakeProgram records for folder dir, the two variables and args.
func want(dir, claude, codex string, args ...string) string {
	lines := []string{dir, claude, codex}
	for _, a := range args {
		lines = append(lines, "<"+a+">")
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestShellLineRuns(t *testing.T) {
	needSh(t)
	base := t.TempDir()
	out := filepath.Join(base, "out.txt")
	prog := fakeProgram(t, base, out)
	for _, name := range []string{"plain", "it's a $(touch pwned) `x` folder; ~ *", "مشروع"} {
		cwd := filepath.Join(base, name)
		if err := os.Mkdir(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		args := append([]string{"--resume"}, shHostile()...)
		env := map[string]string{"CLAUDE_CONFIG_DIR": "/x/it's $(touch pwned)", "CODEX_HOME": ""}
		line := shellLine(cwd, prog, args, env)
		if line == "" {
			t.Fatalf("%s: no shell line", name)
		}
		os.Remove(out)
		cmd := exec.Command("/bin/sh", "-c", line)
		cmd.Env = append(os.Environ(), "CODEX_HOME=/must/be/cleared")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s\n%s", name, err, line, b)
		}
		got, _ := os.ReadFile(out)
		if w := want(cwd, env["CLAUDE_CONFIG_DIR"], "unset", args...); string(got) != w {
			t.Errorf("%s:\n%s\nran as\n%s\nwant\n%s", name, line, got, w)
		}
		assertNoPwned(t, base, cwd)
	}
}

func TestShellLineRefusesControlCharacters(t *testing.T) {
	ok := shellLine("/w", "claude", []string{"--resume", "x"}, map[string]string{"A": "v", "B": ""})
	if ok != "cd -- /w && export A=v && unset B && claude --resume x" {
		t.Errorf("shellLine = %q", ok)
	}
	if got := shellLine("", "claude", nil, nil); got != "claude" {
		t.Errorf("no folder: %q", got)
	}
	for name, line := range map[string]string{
		"cwd":        shellLine("/w\nrm -rf /", "claude", nil, nil),
		"program":    shellLine("/w", "cla\x00ude", nil, nil),
		"argument":   shellLine("/w", "claude", []string{"a\rb"}, nil),
		"value":      shellLine("/w", "claude", nil, map[string]string{"A": "x\ty"}),
		"name":       shellLine("/w", "claude", nil, map[string]string{"A;rm": "x"}),
		"no program": shellLine("/w", "", nil, nil),
		"bad UTF-8":  shellLine("/w", "claude", []string{"\xff"}, nil),
		// fish reads \' inside single quotes, csh expands !
		"backslash":       shellLine("/w", "claude", []string{`/x\'; touch pwned; echo \'`}, nil),
		"bang":            shellLine("/w", "claude", []string{"!!"}, nil),
		"bidi override":   shellLine("/w", "claude", []string{"a\u202eb"}, nil),
		"zero width":      shellLine("/w", "claude", []string{"a\u200bb"}, nil),
		"C1 control":      shellLine("/w", "claude", []string{"a\u0085b"}, nil),
		"relative folder": shellLine("work", "claude", nil, nil),
		"dash folder":     shellLine("-", "claude", nil, nil),
	} {
		if line != "" {
			t.Errorf("%s: shellLine = %q, want none", name, line)
		}
	}
}

func TestPosixArgvRuns(t *testing.T) {
	needSh(t)
	base := t.TempDir()
	out := filepath.Join(base, "out.txt")
	prog := fakeProgram(t, base, out)
	cwd := filepath.Join(base, "it's $(touch pwned) dir")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--resume", "x"}, hostile...)
	c := Command{Dir: cwd, Program: prog, Args: args,
		Env: map[string]string{"CLAUDE_CONFIG_DIR": "/r/$(touch pwned)", "CODEX_HOME": ""}}
	argv, err := posixArgv(c)
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/bin/sh" || argv[1] != "-c" || argv[2] != keepOpenScript {
		t.Fatalf("argv = %q", argv)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	// SHELL=true: the shell kept open after the program exits at once.
	cmd.Env = append(os.Environ(), "CODEX_HOME=/must/be/cleared", "SHELL=/bin/true")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	got, _ := os.ReadFile(out)
	if w := want(cwd, "/r/$(touch pwned)", "unset", args...); string(got) != w {
		t.Errorf("ran as\n%s\nwant\n%s", got, w)
	}
	assertNoPwned(t, base, cwd)
}

func TestPosixArgvRefuses(t *testing.T) {
	for name, c := range map[string]Command{
		"empty program":     {Dir: "/w"},
		"option program":    {Dir: "/w", Program: "-e"},
		"relative program":  {Dir: "/w", Program: "claude"},
		"assignment":        {Dir: "/w", Program: "/bin/A=b"},
		"no folder":         {Program: "/bin/p"},
		"relative folder":   {Dir: "work", Program: "/bin/p"},
		"dash folder":       {Dir: "-", Program: "/bin/p"},
		"bad variable name": {Dir: "/w", Program: "/bin/p", Env: map[string]string{"A B": "x"}},
		"NUL in value":      {Dir: "/w", Program: "/bin/p", Env: map[string]string{"A": "x\x00y"}},
		"NUL in argument":   {Dir: "/w", Program: "/bin/p", Args: []string{"x\x00y"}},
	} {
		if _, err := posixArgv(c); !errors.Is(err, ErrUnsafe) {
			t.Errorf("%s: err = %v, want ErrUnsafe", name, err)
		}
	}
}

func TestTerminalArgv(t *testing.T) {
	c := Command{Dir: "/w/p", Title: "Fix -- it", Program: "/usr/bin/claude", Args: []string{"--resume", "x"}}
	cmd, err := posixArgv(c)
	if err != nil {
		t.Fatal(err)
	}
	installed := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, m := range names {
				if n == m || n == "/opt/bin/"+m {
					return "/opt/bin/" + m, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	vars := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	cases := []struct {
		name string
		env  func(string) string
		look func(string) (string, error)
		pre  []string // argv before the command
	}{
		{"first installed", vars(), installed("xterm", "konsole"),
			[]string{"/opt/bin/konsole", "--new-tab", "--workdir", "/w/p", "-e"}},
		{"the terminal Athar runs in", vars("KITTY_WINDOW_ID", "1"), installed("gnome-terminal", "kitty"),
			[]string{"/opt/bin/kitty", "--directory", "/w/p", "--title", "Fix -- it"}},
		{"its marker without the program", vars("KITTY_WINDOW_ID", "1"), installed("gnome-terminal"),
			[]string{"/opt/bin/gnome-terminal", "--tab", "--working-directory=/w/p", "--"}},
		{"$TERMINAL unknown is never used", vars("TERMINAL", "mate-terminal"), installed("mate-terminal", "xterm"),
			[]string{"/opt/bin/xterm", "-T", "Fix -- it", "-e"}},
		{"$TERMINAL with words is not a known form", vars("TERMINAL", "gnome-terminal --x"), installed("gnome-terminal --x", "xterm"),
			[]string{"/opt/bin/xterm", "-T", "Fix -- it", "-e"}},
		{"$TERMINAL known", vars("TERMINAL", "kitty"), installed("kitty", "gnome-terminal"),
			[]string{"/opt/bin/kitty", "--directory", "/w/p", "--title", "Fix -- it"}},
		{"$TERMINAL known keeps its own form", vars("TERMINAL", "gnome-terminal"), installed("gnome-terminal", "xterm"),
			[]string{"/opt/bin/gnome-terminal", "--tab", "--working-directory=/w/p", "--"}},
		{"$TERMINAL missing", vars("TERMINAL", "nope"), installed("xterm"),
			[]string{"/opt/bin/xterm", "-T", "Fix -- it", "-e"}},
		{"wezterm", vars(), installed("wezterm"), []string{"/opt/bin/wezterm", "start", "--cwd", "/w/p", "--"}},
		{"alacritty", vars(), installed("alacritty"),
			[]string{"/opt/bin/alacritty", "--working-directory", "/w/p", "--title", "Fix -- it", "-e"}},
		{"foot", vars(), installed("foot"), []string{"/opt/bin/foot", "--working-directory=/w/p", "--title=Fix -- it", "--"}},
		{"xfce4", vars(), installed("xfce4-terminal"),
			[]string{"/opt/bin/xfce4-terminal", "--tab", "--title=Fix -- it", "--working-directory=/w/p", "-x"}},
	}
	for _, tc := range cases {
		got, err := terminalArgv(c, tc.env, tc.look)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if w := append(append([]string{}, tc.pre...), cmd...); !reflect.DeepEqual(got, w) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, w)
		}
	}

	if _, err := terminalArgv(c, vars(), installed("x-terminal-emulator", "mate-terminal")); err == nil || !strings.Contains(err.Error(), "no supported terminal") {
		t.Errorf("no known terminal: err = %v", err)
	}
	rel := c
	rel.Dir = "-rf"
	if _, err := terminalArgv(rel, vars(), installed("konsole")); !errors.Is(err, ErrUnsafe) {
		t.Errorf("relative folder: err = %v, want ErrUnsafe", err)
	}
}

func TestMacLineRuns(t *testing.T) {
	needSh(t)
	base := t.TempDir()
	out := filepath.Join(base, "out.txt")
	prog := fakeProgram(t, base, out)
	cwd := filepath.Join(base, "it's $(touch pwned) dir")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--resume", "x"}, shHostile()...)
	c := Command{Dir: cwd, Program: prog, Args: args, Env: map[string]string{"CLAUDE_CONFIG_DIR": "/r/'$(touch pwned)'", "CODEX_HOME": ""}}
	line, err := macLine(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "exec '/bin/sh' '-c' ") {
		t.Errorf("line = %s", line)
	}
	// Terminal types the line into the login shell; sh stands in for it here.
	cmd := exec.Command("/bin/sh", "-c", line)
	cmd.Env = append(os.Environ(), "CODEX_HOME=/must/be/cleared", "SHELL=/bin/true")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	got, _ := os.ReadFile(out)
	if w := want(cwd, "/r/'$(touch pwned)'", "unset", args...); string(got) != w {
		t.Errorf("ran as\n%s\nwant\n%s", got, w)
	}
	assertNoPwned(t, base, cwd)

	for name, bad := range map[string]Command{
		"backslash": {Dir: "/w", Program: "/bin/p", Args: []string{`a\'b`}},
		"bang":      {Dir: "/w", Program: "/bin/p", Args: []string{"!x"}},
		"newline":   {Dir: "/w", Program: "/bin/p", Env: map[string]string{"A": "x\ny"}},
		"relative":  {Dir: "w", Program: "/bin/p"},
	} {
		if _, err := macLine(bad); !errors.Is(err, ErrUnsafe) {
			t.Errorf("%s: err = %v, want ErrUnsafe", name, err)
		}
	}
}

func TestCwdIsNeverAnOption(t *testing.T) {
	fakeResolver(t, []string{"code"}, nil)
	app := adapters.Caps{App: &adapters.AppCommand{Name: "VS Code", Program: "code", Args: []string{"{cwd}"}}}
	for _, cwd := range []string{"--install-extension=/tmp/evil.vsix", "-", "work/proj", "--user-data-dir=/tmp/x"} {
		c, err := Build(Target{Agent: "copilot", NativeID: "x", Cwd: cwd}, app, ActionResume, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(c.Args, []string{"."}) {
			t.Errorf("cwd %q became the arguments %q", cwd, c.Args)
		}
	}
	for _, cwd := range []string{"/home/u/p", `D:\Work\p`, `d:/work/p`, `\\server\share\p`} {
		c, err := Build(Target{Agent: "copilot", NativeID: "x", Cwd: cwd}, app, ActionResume, false, nil)
		if err != nil || !reflect.DeepEqual(c.Args, []string{cwd}) {
			t.Errorf("cwd %q: args %q, %v", cwd, c.Args, err)
		}
	}
}

func TestMacTerminalArgv(t *testing.T) {
	line := `cd -- '/w/it'\''s' && claude --resume x`
	argv := macTerminalArgv(line)
	if argv[0] != "/usr/bin/osascript" || argv[len(argv)-1] != line {
		t.Fatalf("argv = %q", argv)
	}
	// The line is data (item 1 of argv), never part of the AppleScript source.
	for _, a := range argv[1 : len(argv)-1] {
		if strings.Contains(a, "claude") {
			t.Errorf("command text inside the script: %q", a)
		}
	}
}

func TestBuildShell(t *testing.T) {
	fakeResolver(t, []string{"claude", "code"}, nil)
	claude := adapters.Caps{Resume: []string{"claude", "--resume", "{id}"}, Fork: []string{"--fork-session"},
		SkipPermissions: []string{"--dangerously-skip-permissions"}, RootEnv: "CLAUDE_CONFIG_DIR"}
	c, err := Build(Target{Agent: "claude", NativeID: testID, Title: "t", Cwd: "/home/u/it's here", Root: "/home/u/.claude-work"},
		claude, ActionFork, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := `cd -- '/home/u/it'\''s here' && export CLAUDE_CONFIG_DIR=/home/u/.claude-work && claude --resume ` + testID +
		` --fork-session --dangerously-skip-permissions`
	if c.Shell != w {
		t.Errorf("Shell =\n%s\nwant\n%s", c.Shell, w)
	}
	c, err = Build(Target{Agent: "claude", NativeID: testID, Cwd: "/w"}, claude, ActionResume, false, nil)
	if err != nil || c.Shell != "cd -- /w && unset CLAUDE_CONFIG_DIR && claude --resume "+testID {
		t.Errorf("default root: Shell = %q, %v", c.Shell, err)
	}
	app := adapters.Caps{App: &adapters.AppCommand{Name: "VS Code", Program: "code", Args: []string{"{cwd}"}}}
	c, err = Build(Target{Agent: "copilot", NativeID: "x", Cwd: "/home/u/My Project"}, app, ActionResume, false, nil)
	if err != nil || c.Shell != "cd -- '/home/u/My Project' && code '/home/u/My Project'" {
		t.Errorf("app: Shell = %q, %v", c.Shell, err)
	}
}

func TestCopyText(t *testing.T) {
	c := Command{PowerShell: "ps", Shell: "sh"}
	want, format := "sh", "sh"
	if runtime.GOOS == "windows" {
		want, format = "ps", "PowerShell"
	}
	if c.CopyText() != want || CopyFormat() != format {
		t.Errorf("CopyText = %q, CopyFormat = %q", c.CopyText(), CopyFormat())
	}
}
