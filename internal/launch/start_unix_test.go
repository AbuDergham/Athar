//go:build linux

package launch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTools puts scripts named after real tools on a PATH that holds nothing
// else, and clears the variables that would pick another terminal.
func fakeTools(t *testing.T, scripts map[string]string) string {
	t.Helper()
	bin := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	for _, term := range terminals {
		if term.env != "" {
			t.Setenv(term.env, "")
		}
	}
	t.Setenv("TERMINAL", "")
	return bin
}

// waitFile waits up to 5 s for a detached program to write path.
func waitFile(t *testing.T, path string) string {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 && strings.HasSuffix(string(b), "\n") {
			time.Sleep(50 * time.Millisecond) // let the writer finish
			b, _ = os.ReadFile(path)
			return string(b)
		}
	}
	t.Fatalf("%s was not written", path)
	return ""
}

func TestStartLinuxTerminal(t *testing.T) {
	base := t.TempDir()
	out := filepath.Join(base, "out.txt")
	prog := fakeProgram(t, base, out)
	argvFile := filepath.Join(base, "xterm-argv.txt")
	// The fake xterm records its arguments and runs what follows -e.
	fakeTools(t, map[string]string{"xterm": `printf '%s\n' "$@" > ` + quoteSh(argvFile) + `
while [ "$#" -gt 0 ] && [ "$1" != "-e" ]; do shift; done
shift
exec "$@"
`})
	t.Setenv("SHELL", "/bin/true") // the shell kept open after the program exits at once
	t.Setenv("CODEX_HOME", "/must/be/cleared")
	cwd := filepath.Join(base, "it's $(touch pwned) ~ dir")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--resume", "x"}, hostile...)
	c := Command{Title: "Résumé `x`", Dir: cwd, Program: prog, Args: args,
		Env: map[string]string{"CLAUDE_CONFIG_DIR": "/r/$(touch pwned)", "CODEX_HOME": ""}}
	if err := Start(c); err != nil {
		t.Fatal(err)
	}
	if got, w := waitFile(t, out), want(cwd, "/r/$(touch pwned)", "unset", args...); got != w {
		t.Errorf("the program ran as\n%s\nwant\n%s", got, w)
	}
	if got := waitFile(t, argvFile); !strings.HasPrefix(got, "-T\nRésumé x\n-e\n/bin/sh\n-c\n") {
		t.Errorf("xterm arguments:\n%s", got)
	}
	assertNoPwned(t, base, cwd)
}

func TestStartLinuxNoTerminal(t *testing.T) {
	bin := fakeTools(t, map[string]string{"claude": "exit 0\n"})
	err := Start(Command{Dir: bin, Program: "claude"})
	if err == nil || !strings.Contains(err.Error(), "no supported terminal") {
		t.Errorf("err = %v", err)
	}
	if err := Start(Command{Dir: bin, Program: "missing-agent"}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("missing program: err = %v", err)
	}
}

// TestStartLinuxIgnoresPlantedProgram: a program in the session folder never
// runs in place of the agent, even with "." or "" on PATH (finding of the
// phase 3 security review).
func TestStartLinuxIgnoresPlantedProgram(t *testing.T) {
	base := t.TempDir()
	out := filepath.Join(base, "out.txt")
	session := filepath.Join(base, "session")
	if err := os.Mkdir(session, 0o755); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(session, "agent")
	if err := os.WriteFile(planted, []byte("#!/bin/sh\necho PLANTED > "+quoteSh(out)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := fakeTools(t, map[string]string{
		"xterm": "while [ \"$#\" -gt 0 ] && [ \"$1\" != \"-e\" ]; do shift; done\nshift\nexec \"$@\"\n",
		"agent": "echo REAL > " + quoteSh(out) + "\n",
	})
	t.Setenv("PATH", ".::"+bin+":node_modules/.bin")
	t.Setenv("SHELL", "/bin/true")
	if err := Start(Command{Dir: session, Program: "agent"}); err != nil {
		t.Fatal(err)
	}
	if got := waitFile(t, out); got != "REAL\n" {
		t.Errorf("ran %q, want the agent on the absolute PATH entry", got)
	}
	env := childEnv(nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") && kv != "PATH="+bin {
			t.Errorf("child %s, want only the absolute entries", kv)
		}
	}
}

func TestStartLinuxApp(t *testing.T) {
	base := t.TempDir()
	out := filepath.Join(base, "out.txt")
	prog := fakeProgram(t, base, out)
	fakeTools(t, nil)
	cwd := filepath.Join(base, "My Project")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Start(Command{IsApp: true, Dir: cwd, Program: prog, Args: []string{cwd}}); err != nil {
		t.Fatal(err)
	}
	if got, w := waitFile(t, out), want(cwd, "unset", "unset", cwd); got != w {
		t.Errorf("the app ran as\n%s\nwant\n%s", got, w)
	}
	if err := Start(Command{IsApp: true, Program: "no-such-app"}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("missing app: err = %v", err)
	}
}

func TestOpenFolderAndFileLinux(t *testing.T) {
	base := t.TempDir()
	rec := filepath.Join(base, "opened.txt")
	fakeTools(t, map[string]string{"xdg-open": `printf '%s\n' "$@" >> ` + quoteSh(rec) + "\n"})
	folder := filepath.Join(base, "-it's a folder")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := OpenFolder(folder); err != nil {
		t.Fatal(err)
	}
	if got := waitFile(t, rec); got != folder+"\n" {
		t.Errorf("xdg-open got %q", got)
	}
	if err := OpenFolder(filepath.Join(base, "gone")); err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("missing folder: err = %v", err)
	}

	page := filepath.Join(base, "page.html")
	script := filepath.Join(base, "run.sh")
	for _, f := range []string{page, script} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.Remove(rec)
	if err := OpenFile(page); err != nil {
		t.Fatal(err)
	}
	if got := waitFile(t, rec); got != page+"\n" {
		t.Errorf("xdg-open got %q", got)
	}
	if err := OpenFile(script); !errors.Is(err, ErrUnsafe) {
		t.Errorf("script: err = %v, want ErrUnsafe", err)
	}
	if err := OpenFile(folder); err == nil {
		t.Error("a folder was opened as a file")
	}
}

func TestCopyToClipboardLinux(t *testing.T) {
	base := t.TempDir()
	rec := filepath.Join(base, "clip.txt")
	fakeTools(t, map[string]string{
		"xclip":   `printf '%s|' "$@" > ` + quoteSh(rec) + `; /bin/cat >> ` + quoteSh(rec) + "\n",
		"wl-copy": `echo wayland > ` + quoteSh(rec) + "\n",
	})
	text := "cd -- '/w' && claude --resume x\nمشروع"
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")
	if err := CopyToClipboard(text); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(rec); string(b) != "-selection|clipboard|-in|"+text {
		t.Errorf("xclip got %q", b)
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if err := CopyToClipboard(text); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(rec); string(b) != "wayland\n" {
		t.Errorf("Wayland session: %q, want wl-copy", b)
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	if err := CopyToClipboard(text); err == nil || !strings.Contains(err.Error(), "xclip") {
		t.Errorf("no display: err = %v", err)
	}
}
