package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeConfig writes content to dir/athar.toml and returns dir.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadMissingFile(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load on a folder without athar.toml: %v", err)
	}
	if !reflect.DeepEqual(c, Config{}) {
		t.Errorf("Load = %+v, want zero Config", c)
	}
	if !c.UpdateCheckEnabled() {
		t.Error("UpdateCheckEnabled() = false for defaults, want true")
	}
}

func TestLoadEmptyFile(t *testing.T) {
	c, err := Load(writeConfig(t, "# only a comment\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(c, Config{}) {
		t.Errorf("Load = %+v, want zero Config", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	dir := writeConfig(t, `
[resume.codex]      # spec §8 form: one string
resume = "codex resume {id}"
skip_permissions = "--dangerously-bypass-approvals-and-sandbox"

[resume.Claude]     # array form, program path with spaces; agent id is lower-cased
resume = ['C:\Tools\My Claude\claude.exe', "--resume", "{id}"]
fork = ["--fork-session"]
skip_permissions = []

[resume.opencode]
resume = '"C:\Program Files\opencode\opencode-cli.exe" --session {id} --dir {cwd}'

[display]
arabic = "raw"

[update]
check = false
`)
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]Resume{
		"codex": {
			Resume:          []string{"codex", "resume", "{id}"},
			SkipPermissions: []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
		"claude": {
			Resume:          []string{`C:\Tools\My Claude\claude.exe`, "--resume", "{id}"},
			Fork:            []string{"--fork-session"},
			SkipPermissions: []string{},
		},
		"opencode": {
			Resume: []string{`C:\Program Files\opencode\opencode-cli.exe`, "--session", "{id}", "--dir", "{cwd}"},
		},
	}
	if !reflect.DeepEqual(c.Resume, want) {
		t.Errorf("Resume =\n%#v\nwant\n%#v", c.Resume, want)
	}
	// An explicitly empty list stays distinguishable from "not set".
	if c.Resume["claude"].SkipPermissions == nil {
		t.Error("skip_permissions = [] decoded as nil, want empty non-nil slice")
	}
	if c.Resume["codex"].Fork != nil {
		t.Error("unset fork decoded as non-nil")
	}
	if c.Display.Arabic != ArabicRaw {
		t.Errorf("Display.Arabic = %q, want %q", c.Display.Arabic, ArabicRaw)
	}
	if c.UpdateCheckEnabled() {
		t.Error("UpdateCheckEnabled() = true with check = false")
	}
}

func TestLoadSpecExample(t *testing.T) {
	// The example of spec §8, verbatim.
	c, err := Load(writeConfig(t, `[resume.codex]      # optional per agent: resume / fork / skip_permissions
resume = "codex resume {id}"       # program + arguments, no shell syntax; placeholders {id} {cwd}
[display]
arabic = "shaped"   # "shaped" | "raw"; default chosen by the M1 spike
[update]
check = true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Resume["codex"].Resume; !reflect.DeepEqual(got, []string{"codex", "resume", "{id}"}) {
		t.Errorf("resume.codex.resume = %q", got)
	}
	if c.Display.Arabic != ArabicShaped || !c.UpdateCheckEnabled() {
		t.Errorf("display/update = %q/%v", c.Display.Arabic, c.UpdateCheckEnabled())
	}
}

func TestLoadArabicValues(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{`"shaped"`, ArabicShaped, true},
		{`"raw"`, ArabicRaw, true},
		{`" Shaped "`, ArabicShaped, true},
		{`""`, "", true},
		{`"fancy"`, "", false},
		{`"bidi"`, "", false},
	}
	for _, tc := range cases {
		c, err := Load(writeConfig(t, "[display]\narabic = "+tc.in+"\n"))
		if tc.ok {
			if err != nil {
				t.Errorf("arabic = %s: unexpected error %v", tc.in, err)
			} else if c.Display.Arabic != tc.want {
				t.Errorf("arabic = %s: got %q, want %q", tc.in, c.Display.Arabic, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("arabic = %s: no error", tc.in)
			continue
		}
		if !strings.Contains(err.Error(), "display.arabic") {
			t.Errorf("arabic = %s: error %q does not name display.arabic", tc.in, err)
		}
		if !reflect.DeepEqual(c, Config{}) {
			t.Errorf("arabic = %s: on error Load returned %+v, want zero Config", tc.in, c)
		}
	}
}

func TestLoadUnknownKeys(t *testing.T) {
	dir := writeConfig(t, `
colour = "blue"
[display]
arabc = "raw"
[resume.codex]
resum = "codex resume {id}"
[updates]
check = false
[empty]
[a.b.c]
d = 1
`)
	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load accepted unknown keys")
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %T %v is not *config.Error", err, err)
	}
	if ce.Path != filepath.Join(dir, FileName) {
		t.Errorf("Error.Path = %q, want %q", ce.Path, filepath.Join(dir, FileName))
	}
	// Table headers whose keys are listed are not repeated; an empty unknown table is.
	want := []string{"unknown keys: colour, display.arabc, resume.codex.resum, updates.check, empty, a.b.c.d"}
	if !reflect.DeepEqual(ce.Problems, want) {
		t.Errorf("Problems = %q\nwant %q", ce.Problems, want)
	}
}

func TestLoadCollectsAllProblems(t *testing.T) {
	_, err := Load(writeConfig(t, `
typo = 1
[display]
arabic = "fancy"
`))
	if err == nil {
		t.Fatal("no error")
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %T is not *config.Error", err)
	}
	if len(ce.Problems) != 2 {
		t.Errorf("Problems = %q, want 2 entries (unknown key and bad value)", ce.Problems)
	}
}

func TestLoadBadCommands(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"number", `resume = 5`, "resume.x.resume"},
		{"number type", `resume = 5`, "an integer"},
		{"table", `resume = { program = "x" }`, "a table"},
		{"bool", `fork = true`, "a boolean"},
		{"date", `fork = 2026-09-23`, "a date/time"},
		{"array with number", `resume = ["codex", 1]`, "resume.x.resume"},
		{"unterminated quote", `resume = '"C:\Program Files\x.exe --resume {id}'`, "quote"},
		{"empty resume string", `resume = ""`, "resume.x.resume"},
		{"empty resume array", `resume = []`, "resume.x.resume"},
		{"blank program", `resume = ["  ", "{id}"]`, "resume.x.resume"},
		{"ampersand", `resume = "codex resume {id} & calc"`, "&"},
		{"pipe", `resume = ["codex", "a|b"]`, "|"},
		{"percent", `resume = '%LOCALAPPDATA%\x\x.exe {id}'`, "%"},
		{"caret", `fork = "--a^b"`, "resume.x.fork"},
		{"bang", `skip_permissions = "--yolo!"`, "resume.x.skip_permissions"},
		{"redirect", `resume = "codex > out.txt"`, ">"},
		{"double quote in array", `resume = ['codex', 'say"hi']`, `"`},
		{"newline", `resume = ["codex", "a\nb"]`, "control"},
		{"unknown placeholder", `resume = "codex resume {ID}"`, "{ID}"},
		{"unknown placeholder 2", `resume = "codex --dir {project}"`, "{project}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Load(writeConfig(t, "[resume.x]\n"+tc.body+"\n"))
			if err == nil {
				t.Fatalf("accepted %s -> %+v", tc.body, c.Resume)
			}
			t.Log(err)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadDuplicateAgentAfterLowercase(t *testing.T) {
	_, err := Load(writeConfig(t, `
[resume.codex]
resume = "codex resume {id}"
[resume.Codex]
resume = "codex2 resume {id}"
`))
	if err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("duplicate agent ids after lower-casing: err = %v", err)
	}
}

func TestLoadSyntaxError(t *testing.T) {
	dir := writeConfig(t, "[display\narabic = raw\n")
	c, err := Load(dir)
	if err == nil {
		t.Fatal("Load accepted invalid TOML")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, FileName)) {
		t.Errorf("error %q does not name the file", err)
	}
	if !strings.Contains(err.Error(), "line") {
		t.Errorf("error %q has no line number", err)
	}
	if !reflect.DeepEqual(c, Config{}) {
		t.Errorf("on error Load returned %+v, want zero Config", c)
	}
}

func TestLoadUTF8BOM(t *testing.T) {
	// Notepad may save UTF-8 with a byte-order mark.
	c, err := Load(writeConfig(t, "\xef\xbb\xbf[display]\narabic = \"shaped\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Display.Arabic != ArabicShaped {
		t.Errorf("Display.Arabic = %q", c.Display.Arabic)
	}
}

func TestLoadUnreadable(t *testing.T) {
	// athar.toml that is a folder is an error, not "missing".
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, FileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load accepted a folder named athar.toml")
	}
}

func TestUpdateCheckEnabled(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		check *bool
		want  bool
	}{{nil, true}, {&yes, true}, {&no, false}}
	for _, tc := range cases {
		var c Config
		c.Update.Check = tc.check
		if got := c.UpdateCheckEnabled(); got != tc.want {
			t.Errorf("check=%v: UpdateCheckEnabled() = %v, want %v", tc.check, got, tc.want)
		}
	}
	c, err := Load(writeConfig(t, "[update]\ncheck = true\n"))
	if err != nil || !c.UpdateCheckEnabled() {
		t.Errorf("check = true: enabled=%v err=%v", c.UpdateCheckEnabled(), err)
	}
}
