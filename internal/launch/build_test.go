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

const testID = "0f3c9a52-1b2c-4d5e-8f90-a1b2c3d4e5f6"

// noCwd starts every CmdLine (see noCwdSearchEnv).
const noCwd = `set "NoDefaultCurrentDirectoryInExePath=1" && `

var (
	claudeCaps = adapters.Caps{
		Resume:            []string{"claude", "--resume", "{id}"},
		ProgramCandidates: []string{`%USERPROFILE%\.local\bin\claude.exe`},
		Fork:              []string{"--fork-session"},
		SkipPermissions:   []string{"--dangerously-skip-permissions"},
		RootEnv:           "CLAUDE_CONFIG_DIR",
	}
	codexCaps = adapters.Caps{
		Resume:            []string{"codex", "resume", "{id}"},
		ProgramCandidates: []string{`%APPDATA%\npm\codex.cmd`},
		SkipPermissions:   []string{"--dangerously-bypass-approvals-and-sandbox"},
		RootEnv:           "CODEX_HOME",
	}
	geminiCaps = adapters.Caps{
		Resume:          []string{"gemini", "--resume", "{id}"},
		SkipPermissions: []string{"--yolo"},
		RootEnv:         "GEMINI_CLI_HOME",
	}
	opencodeCaps = adapters.Caps{
		Resume:            []string{"opencode", "--session", "{id}"},
		ProgramCandidates: []string{`%LOCALAPPDATA%\opencode\opencode-cli.exe`},
	}
	codeCaps = adapters.Caps{
		App: &adapters.AppCommand{
			Name:       "VS Code",
			Program:    "code",
			Args:       []string{"{cwd}"},
			Candidates: []string{`%LOCALAPPDATA%\Programs\Microsoft VS Code\bin\code.cmd`},
		},
	}
)

// fakeResolver makes program resolution independent of the test machine:
// only the names in onPath are "installed", and %VARS% come from vars.
func fakeResolver(t *testing.T, onPath []string, vars map[string]string) {
	t.Helper()
	oldLook, oldGetenv := lookPath, getenv
	t.Cleanup(func() { lookPath, getenv = oldLook, oldGetenv })
	lookPath = func(name string) (string, error) {
		for _, p := range onPath {
			if strings.EqualFold(p, name) {
				return `C:\bin\` + name + ".exe", nil
			}
		}
		if filepath.IsAbs(name) {
			if fi, err := os.Stat(name); err == nil && !fi.IsDir() {
				return name, nil
			}
		}
		return "", exec.ErrNotFound
	}
	getenv = func(k string) string {
		for kk, v := range vars {
			if strings.EqualFold(kk, k) {
				return v
			}
		}
		return ""
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildTerminal(t *testing.T) {
	tmp := t.TempDir()
	localData := filepath.Join(tmp, "Local Data")
	opencodeExe := filepath.Join(localData, "opencode", "opencode-cli.exe")
	touch(t, opencodeExe)
	appData := filepath.Join(tmp, "Roaming")
	codexCmd := filepath.Join(appData, "npm", "codex.cmd")
	touch(t, codexCmd)
	fakeResolver(t, []string{"claude", "gemini", "mycodex"}, map[string]string{
		"LOCALAPPDATA": localData, "APPDATA": appData, "USERPROFILE": filepath.Join(tmp, "nohome"),
	})

	cases := []struct {
		name     string
		target   Target
		caps     adapters.Caps
		action   Action
		skip     bool
		override []string
		want     Command
		winOnly  bool // the agent's install path candidates use Windows separators
	}{
		{
			name:   "claude default root clears the variable",
			target: Target{Agent: "claude", NativeID: testID, Title: `Fix the "deploy" & tests | 100%`, Cwd: `D:\Work\Alpha`},
			caps:   claudeCaps,
			want: Command{
				Title: "Fix the deploy tests 100", Dir: `D:\Work\Alpha`,
				Env:     map[string]string{"CLAUDE_CONFIG_DIR": ""},
				Program: "claude", Args: []string{"--resume", testID},
				CmdLine:    noCwd + `set "CLAUDE_CONFIG_DIR=" && claude --resume ` + testID,
				PowerShell: `Set-Location -LiteralPath 'D:\Work\Alpha'; $env:CLAUDE_CONFIG_DIR=''; claude --resume '` + testID + `'`,
			},
		},
		{
			name:   "claude non-default root sets the variable, fork and skip",
			target: Target{Agent: "claude", NativeID: testID, Title: "مشروع التحميل", Cwd: `D:\Work\Alpha`, Root: `C:\Users\u\.claude-local`},
			caps:   claudeCaps, action: ActionFork, skip: true,
			want: Command{
				Title: "مشروع التحميل", Dir: `D:\Work\Alpha`,
				Env:     map[string]string{"CLAUDE_CONFIG_DIR": `C:\Users\u\.claude-local`},
				Program: "claude", Args: []string{"--resume", testID, "--fork-session", "--dangerously-skip-permissions"},
				CmdLine:    noCwd + `set "CLAUDE_CONFIG_DIR=C:\Users\u\.claude-local" && claude --resume ` + testID + ` --fork-session --dangerously-skip-permissions`,
				PowerShell: `Set-Location -LiteralPath 'D:\Work\Alpha'; $env:CLAUDE_CONFIG_DIR='C:\Users\u\.claude-local'; claude --resume '` + testID + `' --fork-session --dangerously-skip-permissions`,
			},
		},
		{
			name:   "codex from npm candidate with skip",
			target: Target{Agent: "codex", NativeID: "rollout-abc", Title: "Bot", Cwd: `D:\Work\Bot`},
			caps:   codexCaps, skip: true,
			want: Command{
				Title: "Bot", Dir: `D:\Work\Bot`,
				Env:     map[string]string{"CODEX_HOME": ""},
				Program: codexCmd, Args: []string{"resume", "rollout-abc", "--dangerously-bypass-approvals-and-sandbox"},
				CmdLine:    noCwd + `set "CODEX_HOME=" && ` + codexCmd + ` resume rollout-abc --dangerously-bypass-approvals-and-sandbox`,
				PowerShell: `Set-Location -LiteralPath 'D:\Work\Bot'; $env:CODEX_HOME=''; & '` + codexCmd + `' resume rollout-abc --dangerously-bypass-approvals-and-sandbox`,
			},
			winOnly: true,
		},
		{
			name:   "gemini in a folder with spaces",
			target: Target{Agent: "gemini", NativeID: "c0ffee00-1111-2222-3333-444455556666", Title: "", Cwd: `D:\My Projects\Beta`},
			caps:   geminiCaps,
			want: Command{
				Title: "gemini", Dir: `D:\My Projects\Beta`,
				Env:     map[string]string{"GEMINI_CLI_HOME": ""},
				Program: "gemini", Args: []string{"--resume", "c0ffee00-1111-2222-3333-444455556666"},
				CmdLine:    noCwd + `set "GEMINI_CLI_HOME=" && gemini --resume c0ffee00-1111-2222-3333-444455556666`,
				PowerShell: `Set-Location -LiteralPath 'D:\My Projects\Beta'; $env:GEMINI_CLI_HOME=''; gemini --resume c0ffee00-1111-2222-3333-444455556666`,
			},
		},
		{
			name:   "opencode from install path with spaces",
			target: Target{Agent: "opencode", NativeID: "ses_3a1b2c", Title: "Scraper", Cwd: `D:\Work\Scraper`},
			caps:   opencodeCaps,
			want: Command{
				Title: "Scraper", Dir: `D:\Work\Scraper`, Env: map[string]string{},
				Program: opencodeExe, Args: []string{"--session", "ses_3a1b2c"},
				CmdLine:    noCwd + `"` + opencodeExe + `" --session ses_3a1b2c`,
				PowerShell: `Set-Location -LiteralPath 'D:\Work\Scraper'; & '` + opencodeExe + `' --session ses_3a1b2c`,
			},
			winOnly: true,
		},
		{
			name:     "override with cwd placeholder",
			target:   Target{Agent: "codex", NativeID: "rollout-abc", Title: "Bot", Cwd: `D:\My Projects\it's & x;y`},
			caps:     codexCaps,
			override: []string{"mycodex", "resume", "{id}", "--cd", "{cwd}"},
			want: Command{
				Title: "Bot", Dir: `D:\My Projects\it's & x;y`,
				Env:     map[string]string{"CODEX_HOME": ""},
				Program: "mycodex", Args: []string{"resume", "rollout-abc", "--cd", `D:\My Projects\it's & x;y`},
				CmdLine:    noCwd + `set "CODEX_HOME=" && mycodex resume rollout-abc --cd "D:\My Projects\it's & x;y"`,
				PowerShell: `Set-Location -LiteralPath 'D:\My Projects\it''s & x;y'; $env:CODEX_HOME=''; mycodex resume rollout-abc --cd 'D:\My Projects\it''s & x;y'`,
			},
		},
		{
			name:     "unsafe cwd is only the working directory",
			target:   Target{Agent: "codex", NativeID: "rollout-abc", Title: "Bot", Cwd: `D:\100% done`},
			caps:     codexCaps,
			override: []string{"mycodex", "resume", "{id}", "--cd", "{cwd}"},
			want: Command{
				Title: "Bot", Dir: `D:\100% done`,
				Env:     map[string]string{"CODEX_HOME": ""},
				Program: "mycodex", Args: []string{"resume", "rollout-abc", "--cd", "."},
				CmdLine:    noCwd + `set "CODEX_HOME=" && mycodex resume rollout-abc --cd .`,
				PowerShell: `Set-Location -LiteralPath 'D:\100% done'; $env:CODEX_HOME=''; mycodex resume rollout-abc --cd .`,
			},
		},
		{
			name:   "unsafe root is passed only through the environment block",
			target: Target{Agent: "claude", NativeID: testID, Title: "x", Cwd: `D:\Work\Alpha`, Root: `C:\100%\.claude-x`},
			caps:   claudeCaps,
			want: Command{
				Title: "x", Dir: `D:\Work\Alpha`,
				Env:     map[string]string{"CLAUDE_CONFIG_DIR": `C:\100%\.claude-x`},
				Program: "claude", Args: []string{"--resume", testID},
				CmdLine:    noCwd + `claude --resume ` + testID,
				PowerShell: `Set-Location -LiteralPath 'D:\Work\Alpha'; $env:CLAUDE_CONFIG_DIR='C:\100%\.claude-x'; claude --resume '` + testID + `'`,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.winOnly && runtime.GOOS != "windows" {
				t.Skip("install path candidates are Windows paths")
			}
			got, err := Build(c.target, c.caps, c.action, c.skip, c.override)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Build mismatch\n got: %#v\nwant: %#v", got, c.want)
			}
		})
	}
}

func TestBuildProgramFallbacks(t *testing.T) {
	fakeResolver(t, nil, map[string]string{"LOCALAPPDATA": t.TempDir()})
	got, err := Build(Target{Agent: "opencode", NativeID: "ses_1", Cwd: `D:\w`}, opencodeCaps, ActionResume, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Program != "opencode" {
		t.Errorf("Program = %q, want bare name when nothing is installed", got.Program)
	}
}

func TestBuildEmptyCwdUsesHome(t *testing.T) {
	fakeResolver(t, []string{"claude"}, nil)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got, err := Build(Target{Agent: "claude", NativeID: testID}, claudeCaps, ActionResume, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != home {
		t.Errorf("Dir = %q, want home %q", got.Dir, home)
	}
	if strings.Contains(got.PowerShell, "Set-Location") {
		t.Errorf("PowerShell should not change location without a cwd: %s", got.PowerShell)
	}
}

func TestBuildApp(t *testing.T) {
	fakeResolver(t, []string{"code"}, nil)
	cases := []struct {
		name string
		cwd  string
		want Command
	}{
		{"folder with spaces", `D:\My Projects\Gamma`, Command{
			Title: "Chat", Dir: `D:\My Projects\Gamma`, Env: map[string]string{},
			Program: "code", Args: []string{`D:\My Projects\Gamma`}, IsApp: true, AppName: "VS Code",
			CmdLine:    noCwd + `code "D:\My Projects\Gamma"`,
			PowerShell: `Set-Location -LiteralPath 'D:\My Projects\Gamma'; code 'D:\My Projects\Gamma'`,
		}},
		{"unsafe folder becomes the working directory", `D:\100% done`, Command{
			Title: "Chat", Dir: `D:\100% done`, Env: map[string]string{},
			Program: "code", Args: []string{"."}, IsApp: true, AppName: "VS Code",
			CmdLine:    noCwd + `code .`,
			PowerShell: `Set-Location -LiteralPath 'D:\100% done'; code .`,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Build(Target{Agent: "copilot", NativeID: "whatever id with spaces", Title: "Chat", Cwd: c.cwd}, codeCaps, ActionResume, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Build mismatch\n got: %#v\nwant: %#v", got, c.want)
			}
		})
	}

	got, err := Build(Target{Agent: "lmstudio", NativeID: "x", Title: "Chat"}, codeCaps, ActionResume, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Args) != 0 {
		t.Errorf("Args = %q, want none without a cwd", got.Args)
	}
	if _, err := Build(Target{Agent: "copilot", NativeID: "x", Cwd: `D:\w`}, codeCaps, ActionFork, false, nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("fork of an app: err = %v, want ErrUnsupported", err)
	}
}

func TestBuildErrors(t *testing.T) {
	fakeResolver(t, []string{"claude", "gemini"}, nil)
	cases := []struct {
		name     string
		target   Target
		caps     adapters.Caps
		action   Action
		skip     bool
		override []string
		want     error
	}{
		{"id with quote", Target{NativeID: `abc"&calc`, Cwd: `D:\w`}, claudeCaps, ActionResume, false, nil, ErrUnsafe},
		{"id with percent", Target{NativeID: `abc%PATH%`, Cwd: `D:\w`}, claudeCaps, ActionResume, false, nil, ErrUnsafe},
		{"id with bang", Target{NativeID: `abc!x!`, Cwd: `D:\w`}, claudeCaps, ActionResume, false, nil, ErrUnsafe},
		{"id with caret", Target{NativeID: `abc^x`, Cwd: `D:\w`}, claudeCaps, ActionResume, false, nil, ErrUnsafe},
		{"empty id", Target{NativeID: "", Cwd: `D:\w`}, claudeCaps, ActionResume, false, nil, ErrUnsafe},
		{"override program with quote", Target{NativeID: "abc", Cwd: `D:\w`}, claudeCaps, ActionResume, false, []string{`my"prog`, "{id}"}, ErrUnsafe},
		{"override arg with percent", Target{NativeID: "abc", Cwd: `D:\w`}, claudeCaps, ActionResume, false, []string{"claude", "--x=%PATH%", "{id}"}, ErrUnsafe},
		{"no command", Target{NativeID: "abc"}, adapters.Caps{}, ActionResume, false, nil, ErrUnsupported},
		{"fork unsupported", Target{NativeID: "abc"}, geminiCaps, ActionFork, false, nil, ErrUnsupported},
		{"skip unsupported", Target{NativeID: "abc"}, opencodeCaps, ActionResume, true, nil, ErrUnsupported},
		{"bad env name", Target{NativeID: "abc"}, adapters.Caps{Resume: []string{"x", "{id}"}, RootEnv: "A=B"}, ActionResume, false, nil, ErrUnsafe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Build(c.target, c.caps, c.action, c.skip, c.override)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}
