//go:build windows

package launch

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
)

// waitReport waits for the helper started by Start to write its report.
func waitReport(t *testing.T, path string) helperReport {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			var r helperReport
			if err := json.Unmarshal(data, &r); err == nil {
				return r
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("helper wrote no report to %s", path)
	return helperReport{}
}

func helperSetup(t *testing.T) (self, out string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out = filepath.Join(t.TempDir(), "report.json")
	t.Setenv(helperEnv, "echo")
	t.Setenv(helperOutEnv, out)
	t.Setenv(helperVarEnv, testRootEnv)
	t.Setenv("ATHAR_LAUNCH_HELPER_EXE", self)
	t.Setenv(testRootEnv, "WRONG")
	return self, out
}

func TestWTCommandLine(t *testing.T) {
	got := wtCommandLine(`C:\Program Files\wt.exe`, []string{"--title", "a b", `set "V=x" && y`, `D:\a b\`})
	want := `"C:\Program Files\wt.exe" --title "a b" "set \"V=x\" && y" "D:\a b\\"`
	if got != want {
		t.Errorf("wtCommandLine = %q, want %q", got, want)
	}
}

// TestStartWT runs Start with a fake wt.exe (this test binary) and checks the
// argv it receives and the environment block.
func TestStartWT(t *testing.T) {
	self, out := helperSetup(t)
	old := lookPath
	t.Cleanup(func() { lookPath = old })
	lookPath = func(name string) (string, error) {
		if strings.EqualFold(name, "wt.exe") {
			return self, nil
		}
		return old(name)
	}
	dir := filepath.Join(t.TempDir(), "it's & x;y")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c := Command{
		Title:   "Deploy fix",
		Dir:     dir,
		Env:     map[string]string{testRootEnv: `C:\root dir`},
		Program: "claude",
		Args:    []string{"--resume", "abc"},
		CmdLine: `set "` + testRootEnv + `=C:\root dir" && claude --resume abc`,
	}
	if err := Start(c); err != nil {
		t.Fatal(err)
	}
	r := waitReport(t, out)
	if !reflect.DeepEqual(r.Args, WTArgs(c)) {
		t.Errorf("wt argv = %q, want %q", r.Args, WTArgs(c))
	}
	if r.Var != `C:\root dir` {
		t.Errorf("environment block: %s = %q", testRootEnv, r.Var)
	}
}

// TestStartApp starts a GUI "app" directly (.exe) and through a .cmd shim.
func TestStartApp(t *testing.T) {
	self, out := helperSetup(t)
	dir := filepath.Join(t.TempDir(), "proj & co")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c := Command{Title: "x", Dir: dir, Env: map[string]string{}, Program: self, Args: []string{dir, "--flag"}, IsApp: true}
	c.CmdLine = quoteCmd(self) + " " + quoteCmd(dir) + " --flag"
	if err := Start(c); err != nil {
		t.Fatal(err)
	}
	r := waitReport(t, out)
	if !reflect.DeepEqual(r.Args, c.Args) || !strings.EqualFold(filepath.Clean(r.Cwd), filepath.Clean(dir)) {
		t.Errorf("exe app: args %q cwd %q", r.Args, r.Cwd)
	}
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}

	script, err := os.ReadFile(filepath.Join("testdata", "echoargs.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(t.TempDir(), "bin dir", "code.cmd")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, script, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeResolver(t, nil, nil)
	caps := adapters.Caps{App: &adapters.AppCommand{Name: "VS Code", Program: "code", Args: []string{"{cwd}"}, Candidates: []string{shim}}}
	cmdObj, err := Build(Target{Agent: "copilot", NativeID: "x", Title: "t", Cwd: dir}, caps, ActionResume, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Start(cmdObj); err != nil {
		t.Fatal(err)
	}
	r = waitReport(t, out)
	if !reflect.DeepEqual(r.Args, []string{dir}) || !strings.EqualFold(filepath.Clean(r.Cwd), filepath.Clean(dir)) {
		t.Errorf("cmd app: args %q cwd %q", r.Args, r.Cwd)
	}
}

// TestOpenFileRejects checks validation only; opening real files is manual.
func TestOpenRejects(t *testing.T) {
	dir := t.TempDir()
	if err := OpenFile(filepath.Join(dir, "missing.html")); err == nil {
		t.Error("OpenFile of a missing file succeeded")
	}
	if err := OpenFile(dir); err == nil {
		t.Error("OpenFile of a folder succeeded")
	}
	exe := filepath.Join(dir, "evil.exe")
	touch(t, exe)
	if err := OpenFile(exe); err == nil {
		t.Error("OpenFile of an executable succeeded")
	}
	if err := OpenFolder(filepath.Join(dir, "missing")); err == nil {
		t.Error("OpenFolder of a missing folder succeeded")
	}
	if err := OpenFolder(exe); err == nil {
		t.Error("OpenFolder of a file succeeded")
	}
	if _, err := exec.LookPath("explorer.exe"); err != nil {
		t.Log("explorer.exe not on PATH")
	}
}
