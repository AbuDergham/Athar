package codex

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCallText(t *testing.T) {
	cases := []struct{ name, args, want string }{
		{"shell", `{"command":["powershell.exe","-Command","Get-ChildItem"],"workdir":"C:\\x"}`, "shell: Get-ChildItem"},
		{"shell", `{"command":["bash","-lc","make test"]}`, "shell: make test"},
		{"shell", `{"command":["git","status","--short"]}`, "shell: git status --short"},
		{"shell", `{"command":"npm test"}`, "shell: npm test"},
		{"shell_command", `{"command":"dir /b","workdir":"C:\\x"}`, "shell_command: dir /b"},
		{"exec_command", `{"cmd":"ls"}`, "exec_command: ls"},
		{"read_file", `{"file_path":"a/b.go","offset":1}`, "read_file: a/b.go"},
		{"list_dir", `{"dir_path":"src"}`, "list_dir: src"},
		{"grep_files", `{"pattern":"TODO","path":"."}`, "grep_files: TODO ."},
		{"view_image", `{"path":"shot.png"}`, "view_image: shot.png"},
		{"apply_patch", `{"input":"*** Begin Patch\n*** Delete File: old.txt\n*** End Patch"}`, "apply_patch: old.txt"},
		{"update_plan", `{}`, "update_plan"},
		{"update_plan", ``, "update_plan"},
		{"mcp_tool", `{"q":"x"}`, `mcp_tool: {"q":"x"}`},
		{"weird", `not json`, "weird: not json"},
		{"long", `{"k":"` + strings.Repeat("a", 500) + `"}`, "long: " + string([]rune(`{"k":"` + strings.Repeat("a", 500))[:200])},
	}
	for _, c := range cases {
		if got := callText(c.name, c.args); got != c.want {
			t.Errorf("callText(%q, %.40q) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

func TestCustomCallText(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: a.go\n*** Move to: b.go\n@@\n*** Add File: c.go\n+x\n*** Update File: a.go\n*** End Patch\n"
	if got, want := customCallText("apply_patch", patch), "apply_patch: a.go, b.go, c.go"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := customCallText("js_repl", "  console.log(1)\n"), "js_repl: console.log(1)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := customCallText("noop", ""), "noop"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOutputText(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`"plain text"`, "plain text"},
		{`"{\"output\":\"inner\\n\",\"metadata\":{\"exit_code\":0}}"`, "inner\n"},
		{`"{\"not_output\":1}"`, `{"not_output":1}`},
		{`[{"type":"input_text","text":"a"},{"type":"input_image","image_url":"data:x"},{"type":"output_text","text":"b"}]`, "a\n[image]\nb"},
		{`{"content":"from content","success":true}`, "from content"},
		{`{"content":[{"type":"input_text","text":"nested"}]}`, "nested"},
		{`null`, ""},
		{``, ""},
	}
	for _, c := range cases {
		if got := outputText(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("outputText(%s) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestTruncateBytes(t *testing.T) {
	s := strings.Repeat("ب", 1500) // 3000 bytes
	got := truncateBytes(s, 2048)
	if len(got) > 2048 || !utf8.ValidString(got) || len(got) != 2048 {
		t.Errorf("len %d valid %v", len(got), utf8.ValidString(got))
	}
	got = truncateBytes("a"+strings.Repeat("😀", 600), 2048) // 1 + 2400 bytes; cut inside an emoji
	if len(got) != 2045 || !utf8.ValidString(got) {
		t.Errorf("len %d valid %v", len(got), utf8.ValidString(got))
	}
	if truncateBytes("short", 2048) != "short" {
		t.Error("short strings must be unchanged")
	}
}

func TestUserPromptText(t *testing.T) {
	ide := "# Context from my IDE setup:\n\n## Active file: x.go\n\n## My request for Codex:\n  do the thing \n"
	if got := userPromptText(ide); got != "do the thing" {
		t.Errorf("IDE prompt = %q", got)
	}
	if got := userPromptText("  keep   me\nverbatim "); got != "  keep   me\nverbatim " {
		t.Errorf("plain prompt changed: %q", got)
	}
	if got := userPromptText("# Context from my IDE setup:\n\n## Active file: x.go\n"); !strings.HasPrefix(got, "# Context") {
		t.Errorf("IDE context without a request must be kept: %q", got)
	}
}

func TestTitleFrom(t *testing.T) {
	if got := titleFrom("  a\n\tb   c\n[image]\n"); got != "a b c" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("ع", 150)
	if got := titleFrom(long); utf8.RuneCountInString(got) != 100 {
		t.Errorf("title has %d runes, want 100", utf8.RuneCountInString(got))
	}
}

func TestSkipSource(t *testing.T) {
	cases := map[string]bool{
		``:                                 false,
		`"cli"`:                            false,
		`"vscode"`:                         false,
		`"something-new"`:                  false,
		`"exec"`:                           true,
		`"mcp"`:                            true,
		`{"subagent":"review"}`:            true,
		`{"subagent":{"thread_spawn":{}}}`: true,
		`{"other":1}`:                      false,
	}
	for raw, want := range cases {
		if got := skipSource(json.RawMessage(raw)); got != want {
			t.Errorf("skipSource(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestEnvContextCwd(t *testing.T) {
	if got := envContextCwd("<environment_context>\n  <cwd>C:\\a b\\c</cwd>\n  <shell>pwsh</shell>\n</environment_context>"); got != `C:\a b\c` {
		t.Errorf("got %q", got)
	}
	if got := envContextCwd("no context here"); got != "" {
		t.Errorf("got %q", got)
	}
}
