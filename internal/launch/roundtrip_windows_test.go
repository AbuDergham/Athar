//go:build windows

package launch

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/AbuDergham/Athar/internal/adapters"
)

const testRootEnv = "ATHAR_LAUNCH_TEST_ROOT"

// wtDelimiter is Windows Terminal's subcommand delimiter (AppCommandlineArgs.cpp).
var wtDelimiter = regexp.MustCompile(`(^;|[^\\];)`)

// simulateWT does what wt.exe does with the command line Start gives it:
// CommandLineToArgvW, split on unescaped ';', unescape '\;', then join the
// positional command line quoting every token that contains a space.
func simulateWT(t *testing.T, line string) (title, dir string, cmdTokens []string, joined string) {
	t.Helper()
	argv, err := windows.DecomposeCommandLine(line)
	if err != nil {
		t.Fatalf("DecomposeCommandLine: %v", err)
	}
	args := argv[1:]
	for i, a := range args {
		if wtDelimiter.MatchString(a) {
			t.Fatalf("argument %q would split the wt command line", a)
		}
		args[i] = strings.ReplaceAll(a, `\;`, `;`)
	}
	cmdAt := -1
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--title":
			i++
			title = args[i]
		case "-d":
			i++
			dir = args[i]
		case "cmd.exe":
			cmdAt = i
		}
		if cmdAt >= 0 {
			break
		}
	}
	if cmdAt < 0 {
		t.Fatalf("no cmd.exe in %q", args)
	}
	cmdTokens = args[cmdAt:]
	parts := make([]string, len(cmdTokens))
	for i, a := range cmdTokens {
		if i == 3 && a == "/k" {
			a = "/c" // tests must not leave a shell waiting
		}
		if strings.Contains(a, " ") {
			a = `"` + a + `"`
		}
		parts[i] = a
	}
	return title, dir, cmdTokens, strings.Join(parts, " ")
}

// runCmd runs cmd.exe with a raw command line and returns what the helper saw.
func runCmd(t *testing.T, cmdLine, dir string, env []string) helperReport {
	t.Helper()
	out := filepath.Join(t.TempDir(), "report.json")
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdLine, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Dir = dir
	cmd.Env = append(env, helperOutEnv+"="+out)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cmd failed: %v\n%s\ncommand line: %s", err, b, cmdLine)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("helper did not run (%v); cmd output: %s\ncommand line: %s", err, b, cmdLine)
	}
	var r helperReport
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	progDir := filepath.Join(tmp, "dir with space")
	if err := os.MkdirAll(progDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join("testdata", "echoargs.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(progDir, "echoargs.cmd")
	if err := os.WriteFile(prog, script, 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	caps := adapters.Caps{
		Resume:          []string{prog, "--resume", "{id}", "--cwd", "{cwd}"},
		Fork:            []string{"--fork"},
		SkipPermissions: []string{"--skip-perms"},
		RootEnv:         testRootEnv,
	}
	base := append(os.Environ(),
		helperEnv+"=echo",
		helperVarEnv+"="+testRootEnv,
		"ATHAR_LAUNCH_HELPER_EXE="+self,
		testRootEnv+"=WRONG",
	)

	cases := []struct {
		name     string
		cwd      string // folder name under tmp
		root     string // "" = default root
		action   Action
		skip     bool
		wantCwd  string // expected {cwd} argument ("" = the folder itself)
		embedded bool   // root travels inside CmdLine (not only via the environment block)
	}{
		{name: "plain", cwd: "proj", embedded: true},
		{name: "quote ampersand semicolon", cwd: "it's & x;y (1)", root: "root & co;1", embedded: true},
		{name: "leading semicolon", cwd: ";lead", root: `C:\x\;y`, embedded: true},
		{name: "specials without space", cwd: "a&b;c=d,e(f)", root: `C:\r&s|t`, embedded: true},
		{name: "percent and bang", cwd: "100% done!", root: "r%PATH%x", wantCwd: ".", embedded: false},
		{name: "arabic fork skip", cwd: "مشروع ٢", root: `C:\جذر`, action: ActionFork, skip: true, embedded: true},
		{name: "caret", cwd: "a^b", wantCwd: ".", embedded: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cwd := filepath.Join(tmp, c.cwd)
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatal(err)
			}
			root := c.root
			if root != "" && !filepath.IsAbs(root) {
				root = filepath.Join(tmp, root)
			}
			cmdObj, err := Build(Target{Agent: "echo", NativeID: "id-1:2.3", Title: `T "x" & y | 5%`, Cwd: cwd, Root: root}, caps, c.action, c.skip, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := Preflight(cmdObj); err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			wantArgArg := cwd
			if c.wantCwd != "" {
				wantArgArg = c.wantCwd
			}
			wantArgs := []string{"--resume", "id-1:2.3", "--cwd", wantArgArg}
			if c.action == ActionFork {
				wantArgs = append(wantArgs, "--fork")
			}
			if c.skip {
				wantArgs = append(wantArgs, "--skip-perms")
			}
			wantVar, wantSet := root, root != ""

			line := wtCommandLine(`C:\Users\u\AppData\Local\Microsoft\WindowsApps\wt.exe`, WTArgs(cmdObj))
			title, dir, tokens, joined := simulateWT(t, line)
			if title != cmdObj.Title || title != "T x y 5" {
				t.Errorf("title = %q, want %q", title, cmdObj.Title)
			}
			if dir != cwd {
				t.Errorf("dir = %q, want %q", dir, cwd)
			}
			if !reflect.DeepEqual(tokens[:4], []string{"cmd.exe", "/d", "/s", "/k"}) || len(tokens) != 5 || tokens[4] != cmdObj.CmdLine {
				t.Fatalf("cmd tokens = %q", tokens)
			}

			check := func(label string, r helperReport, varWanted string, setWanted bool) {
				t.Helper()
				if !reflect.DeepEqual(r.Args, wantArgs) {
					t.Errorf("%s: args = %q, want %q", label, r.Args, wantArgs)
				}
				if !strings.EqualFold(filepath.Clean(r.Cwd), filepath.Clean(cwd)) {
					t.Errorf("%s: cwd = %q, want %q", label, r.Cwd, cwd)
				}
				if r.VarSet != setWanted || r.Var != varWanted {
					t.Errorf("%s: %s = %q (set=%v), want %q (set=%v)", label, testRootEnv, r.Var, r.VarSet, varWanted, setWanted)
				}
			}

			// As Start runs it: environment block prepared too.
			check("wt+env", runCmd(t, joined, dir, mergeEnv(base, cmdObj.Env)), wantVar, wantSet)
			// Without the environment block: only the embedded `set` can fix the variable.
			if c.embedded {
				check("wt embedded", runCmd(t, joined, dir, base), wantVar, wantSet)
			} else {
				check("wt embedded", runCmd(t, joined, dir, base), "WRONG", true)
			}
			// Fallback without Windows Terminal: cmd.exe /d /s /k "title T && C".
			check("console", runCmd(t, consoleCmdLine(cmdObj, "/c"), cmdObj.Dir, mergeEnv(base, cmdObj.Env)), wantVar, wantSet)
		})
	}
}

// TestRoundTripNoCwdHijack: a program with the agent's bare name planted in the
// session folder (e.g. a cloned repo with claude.cmd) must not run; cmd.exe
// otherwise searches the current folder before PATH.
func TestRoundTripNoCwdHijack(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	proj := filepath.Join(tmp, "proj")
	script, err := os.ReadFile(filepath.Join("testdata", "echoargs.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{bin, proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "athartestprog.cmd"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	evil := "@echo off\r\necho {\"args\":[\"HIJACKED\"]}>\"%ATHAR_LAUNCH_HELPER_OUT%\"\r\n"
	if err := os.WriteFile(filepath.Join(proj, "athartestprog.cmd"), []byte(evil), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	caps := adapters.Caps{Resume: []string{"athartestprog", "--resume", "{id}"}}
	c, err := Build(Target{Agent: "x", NativeID: "abc", Title: "t", Cwd: proj}, caps, ActionResume, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Program != "athartestprog" {
		t.Fatalf("Program = %q, want the bare name", c.Program)
	}
	// This test's own environment may already carry the protective variable;
	// remove it so only the command line can provide it.
	env := mergeEnv(os.Environ(), map[string]string{
		noCwdSearchEnv: "",
		"PATH":         bin + ";" + filepath.Join(os.Getenv("SystemRoot"), "System32"),
	})
	env = append(env, helperEnv+"=echo", helperVarEnv+"="+testRootEnv, "ATHAR_LAUNCH_HELPER_EXE="+self)

	_, dir, _, joined := simulateWT(t, wtCommandLine(`C:\wt.exe`, WTArgs(c)))
	for label, line := range map[string]string{"wt": joined, "console": consoleCmdLine(c, "/c")} {
		r := runCmd(t, line, dir, env)
		if !reflect.DeepEqual(r.Args, []string{"--resume", "abc"}) {
			t.Errorf("%s: ran %q, want the program from PATH", label, r.Args)
		}
	}
}

// TestPowerShellParses feeds the copy commands to the real PowerShell parser.
func TestPowerShellParses(t *testing.T) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("powershell.exe not available")
	}
	tmp := t.TempDir()
	odd := filepath.Join(tmp, "O'Brien $HOME `n & x;y ’q‘")
	exe := filepath.Join(odd, "opencode", "opencode-cli.exe")
	touch(t, exe)
	fakeResolver(t, []string{"claude"}, map[string]string{"LOCALAPPDATA": odd})

	type psCase struct {
		cmd  Command
		want [][]string
	}
	var cases []psCase
	add := func(tg Target, caps adapters.Caps, want [][]string) {
		c, err := Build(tg, caps, ActionResume, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, psCase{c, want})
	}
	add(Target{Agent: "opencode", NativeID: "ses_1", Cwd: odd}, opencodeCaps, [][]string{
		{"Unknown", "Set-Location", "-LiteralPath", odd},
		{"Ampersand", exe, "--session", "ses_1"},
	})
	add(Target{Agent: "claude", NativeID: testID, Cwd: `D:\a;b & c`, Root: `C:\it's $x ` + "`" + `;y`}, claudeCaps, [][]string{
		{"Unknown", "Set-Location", "-LiteralPath", `D:\a;b & c`},
		{"assign", "$env:CLAUDE_CONFIG_DIR", `C:\it's $x ` + "`" + `;y`},
		{"Unknown", "claude", "--resume", testID},
	})
	add(Target{Agent: "claude", NativeID: testID}, claudeCaps, [][]string{
		{"assign", "$env:CLAUDE_CONFIG_DIR", ""},
		{"Unknown", "claude", "--resume", testID},
	})

	scripts := make([]string, len(cases))
	for i, c := range cases {
		scripts[i] = c.cmd.PowerShell
	}
	in := filepath.Join(tmp, "in.json")
	outPath := filepath.Join(tmp, "out.json")
	b, _ := json.Marshal(scripts)
	if err := os.WriteFile(in, b, 0o644); err != nil {
		t.Fatal(err)
	}
	analyzer := filepath.Join(tmp, "analyze.ps1")
	const src = `$ErrorActionPreference = 'Stop'
$scripts = Get-Content -Raw -Encoding UTF8 -LiteralPath $env:ATHAR_PS_IN | ConvertFrom-Json
$results = New-Object System.Collections.ArrayList
foreach ($s in $scripts) {
  $toks = $null; $errs = $null
  $ast = [System.Management.Automation.Language.Parser]::ParseInput($s, [ref]$toks, [ref]$errs)
  $stmts = New-Object System.Collections.ArrayList
  foreach ($st in $ast.EndBlock.Statements) {
    $row = New-Object System.Collections.ArrayList
    if ($st -is [System.Management.Automation.Language.AssignmentStatementAst]) {
      [void]$row.Add('assign'); [void]$row.Add($st.Left.Extent.Text); [void]$row.Add([string]$st.Right.Expression.Value)
    } elseif ($st -is [System.Management.Automation.Language.PipelineAst] -and $st.PipelineElements[0] -is [System.Management.Automation.Language.CommandAst]) {
      $c = $st.PipelineElements[0]
      [void]$row.Add($c.InvocationOperator.ToString())
      foreach ($e in $c.CommandElements) {
        if ($e -is [System.Management.Automation.Language.StringConstantExpressionAst]) { [void]$row.Add($e.Value) } else { [void]$row.Add($e.Extent.Text) }
      }
    } else { [void]$row.Add('other:' + $st.GetType().Name) }
    [void]$stmts.Add($row.ToArray())
  }
  [void]$results.Add([pscustomobject]@{ errors = @($errs).Count; stmts = $stmts.ToArray() })
}
$json = ConvertTo-Json -InputObject $results.ToArray() -Depth 6 -Compress
[System.IO.File]::WriteAllText($env:ATHAR_PS_OUT, $json, (New-Object System.Text.UTF8Encoding $false))
`
	if err := os.WriteFile(analyzer, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", analyzer)
	cmd.Env = append(os.Environ(), "ATHAR_PS_IN="+in, "ATHAR_PS_OUT="+outPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("powershell: %v\n%s", err, out)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Errors int        `json:"errors"`
		Stmts  [][]string `json:"stmts"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	if len(got) != len(cases) {
		t.Fatalf("got %d results, want %d", len(got), len(cases))
	}
	for i, c := range cases {
		if got[i].Errors != 0 {
			t.Errorf("%s: %d parse errors", c.cmd.PowerShell, got[i].Errors)
		}
		if !reflect.DeepEqual(got[i].Stmts, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.cmd.PowerShell, got[i].Stmts, c.want)
		}
	}
}
