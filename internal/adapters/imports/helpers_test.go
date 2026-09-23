package imports

import (
	"strings"
	"testing"
	"time"
)

func TestMapAgent(t *testing.T) {
	cases := []struct{ path, label, agent, root string }{
		{`C:\Users\u\.claude\projects\C--x\a.jsonl`, "claude", "claude", ""},
		{`C:\Users\u\.claude-local\projects\C--x\a.jsonl`, "pi", "claude", `C:\Users\u\.claude-local`},
		{`C:/Users/u/.claude-work/projects/C--x/a.jsonl`, "", "claude", `C:\Users\u\.claude-work`},
		{`C:\Users\u\.codex\sessions\2025\09\20\rollout-x.jsonl`, "codex", "codex", ""},
		{`C:\Users\u\.gemini\tmp\proj\chats\session-1.json`, "pi", "gemini", ""},
		{`C:\Users\u\.gemini\antigravity-cli\conversations\x.db`, "antigravity-cli", "agy", ""},
		{`C:\Users\u\.gemini\antigravity\conversations\x.pb`, "", "antigravity", ""},
		{`C:\Users\u\AppData\Roaming\Code\User\workspaceStorage\h\chatSessions\x.json`, "vscode-copilot", "copilot", ""},
		{`C:\Users\u\AppData\Roaming\Code\User\globalStorage\saoudrizwan.claude-dev\tasks\1\api_conversation_history.json`, "cline", "cline", ""},
		{`C:\Users\u\AppData\Roaming\Cursor\User\globalStorage\state.vscdb#c1`, "cursor", "cursor", ""},
		{`C:\Users\u\.local\share\opencode\opencode.db#ses_1`, "opencode", "opencode", ""},
		{`C:\Users\u\.local\share\kilo\kilo.db#ses_1`, "", "kilo", ""},
		{`C:\Users\u\.qoder\projects\C--x\a.jsonl`, "qoder", "qoder", ""},
		{`C:\Users\u\.zcode\cli\db\db.sqlite#sess_1`, "zcode", "zcode", ""},
		{`C:\Users\u\AppData\Local\Zed\threads\threads.db#1`, "zed", "zed", ""},
		{`C:\Users\u\AppData\Roaming\Trae\User\workspaceStorage\h\state.vscdb`, "trae", "trae", ""},
		{`/home/u/elsewhere/file.jsonl`, "vscode-copilot", "copilot", ""},
		{`/home/u/elsewhere/file.jsonl`, "antigravity-cli", "agy", ""},
		{`/home/u/elsewhere/file.jsonl`, "claude-code", "claude", ""},
		{`/home/u/elsewhere/file.jsonl`, "Amp", "amp", ""},
		{``, "", "unknown", ""},
	}
	for _, c := range cases {
		agent, root := mapAgent(c.path, c.label)
		if agent != c.agent || root != c.root {
			t.Errorf("mapAgent(%q, %q) = %q, %q; want %q, %q", c.path, c.label, agent, root, c.agent, c.root)
		}
	}
}

func TestNativeIDs(t *testing.T) {
	strip := []struct{ id, label, agent, want string }{
		{"codex:3333", "codex", "codex", "3333"},
		{"vscode-copilot:4444", "vscode-copilot", "copilot", "4444"},
		{"antigravity-cli:6666", "antigravity-cli", "agy", "6666"},
		{"1111-uuid", "claude", "claude", "1111-uuid"},
		{"codebuff:proj:chat", "codebuff", "codebuff", "proj:chat"}, // only one prefix is removed
		{"zcode:sess_1", "zcode", "zcode", "sess_1"},
		{"codex:", "codex", "codex", "codex:"},
	}
	for _, c := range strip {
		if got := stripPrefix(c.id, c.label, c.agent); got != c.want {
			t.Errorf("stripPrefix(%q) = %q, want %q", c.id, got, c.want)
		}
	}
	psm := []struct{ id, slug, want, wantSlug string }{
		{"claude-code:1111-uuid:0123456789ab", "claude-code", "1111-uuid", "claude-code"},
		{"cline:1742734460763:0123456789ab", "cline", "1742734460763", "cline"},
		{"pi0001", "pi", "pi0001", ""},
		{"codex:3333", "codex", "3333", ""},
	}
	for _, c := range psm {
		got, slug := psmNativeID(c.id, c.slug, "")
		if got != c.want || slug != c.wantSlug {
			t.Errorf("psmNativeID(%q) = %q, %q; want %q, %q", c.id, got, slug, c.want, c.wantSlug)
		}
	}
}

func TestStripThinking(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"[Thinking]\nx\n[/Thinking]", ""},
		{"[Thinking]\nx\n[/Thinking]\nanswer", "answer"},
		{"before\n[Thinking]\nx\n[/Thinking]\nafter\n[Thinking]\ny\n[/Thinking]", "before\nafter"},
		{"quote [Thinking] inline stays", "quote [Thinking] inline stays"},
		{"a\n[Thinking]\nunterminated", "a"},
	}
	for _, c := range cases {
		if got := stripThinking(c.in); got != c.want {
			t.Errorf("stripThinking(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUserText(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"  hello  ", "hello", true},
		{"", "", false},
		{"<command-name>/clear</command-name>", "", false},
		{"<local-command-stdout>x</local-command-stdout>", "", false},
		{"[Request interrupted by user]", "", false},
		{"<environment_context>cwd</environment_context>", "", false},
		{"<bash-input>ls -la</bash-input>", "!ls -la", true},
		{"<div>html the user pasted</div>", "<div>html the user pasted</div>", true},
	}
	for _, c := range cases {
		got, ok := userText(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("userText(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 9, 1, 8, 0, 0, 500_000_000, time.UTC)
	for _, s := range []string{
		"2026-09-01T08:00:00.5Z",
		"2026-09-01T08:00:00.500+00:00",
		"2026-09-01T10:00:00.5+02:00",
		"2026-09-01T08:00:00.500000000Z",
		"2026-09-01 08:00:00.5",
		"1788249600500",
	} {
		if got := parseTime(s); !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("parseTime(%q) = %v", s, got)
		}
	}
	for _, s := range []string{"", "garbage", "0"} {
		if got := parseTime(s); !got.IsZero() {
			t.Errorf("parseTime(%q) = %v, want zero", s, got)
		}
	}
}

func TestToolCallText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"Bash", `{"command":"go test ./..."}`, "Bash: go test ./..."},
		{"shell", `{"command":["bash","-lc","ls"]}`, "shell: bash -lc ls"},
		{"Read", `{"file_path":"C:\\x\\a.go"}`, `Read: C:\x\a.go`},
		{"noargs", `{}`, "noargs"},
		{"other", `{"a":1}`, `other: {"a":1}`},
		{"raw", `not json`, "raw: not json"},
	}
	for _, c := range cases {
		if got := toolCallText(c.name, c.in); got != c.want {
			t.Errorf("toolCallText(%q, %q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestPromptTitleAndTrunc(t *testing.T) {
	long := strings.Repeat("ع", 150)
	if got := []rune(promptTitle(long)); len(got) != titleRunes {
		t.Errorf("promptTitle length %d", len(got))
	}
	if got := promptTitle("  a \n\t b  "); got != "a b" {
		t.Errorf("promptTitle = %q", got)
	}
	if got := truncBytes("aé", 2); got != "a" {
		t.Errorf("truncBytes = %q", got)
	}
	for _, c := range []struct{ m, host, want string }{
		{"local", "HOST", ""}, {"", "HOST", ""}, {"host", "HOST", ""}, {"OTHER", "HOST", "OTHER"},
	} {
		if got := machineName(c.m, c.host); got != c.want {
			t.Errorf("machineName(%q, %q) = %q, want %q", c.m, c.host, got, c.want)
		}
	}
}
