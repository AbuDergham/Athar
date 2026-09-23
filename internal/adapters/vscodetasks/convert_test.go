package vscodetasks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

func TestParseAssistant(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // "text:<text>" or "tool:<name>|<body>"
	}{
		{"plain", "Hello there.", []string{"text:Hello there."}},
		{"tool after text", "Let me read.\n\n<read_file>\n<path>a.go</path>\n</read_file>",
			[]string{"text:Let me read.\n\n", "tool:read_file|\n<path>a.go</path>\n"}},
		{"thinking and task_progress dropped", "<thinking>secret</thinking>\nDone.\n<task_progress>\n- [x] a\n</task_progress>",
			[]string{"text:\nDone.\n"}},
		{"unterminated tool runs to the end", "<execute_command>\n<command>ls", []string{"tool:execute_command|\n<command>ls"}},
		{"html and prose are text", "Use <div> and <b>bold</b>; see <read_file> docs", []string{"text:Use <div> and <b>bold</b>; see <read_file> docs"}},
		{"unknown underscore tool at line start", "Ok\n<my_custom_tool>\n<arg>x</arg>\n</my_custom_tool>",
			[]string{"text:Ok\n", "tool:my_custom_tool|\n<arg>x</arg>\n"}},
		{"unknown underscore tag inside prose", "call it like foo <my_tool> bar", []string{"text:call it like foo <my_tool> bar"}},
		{"unclosed unknown tag is text", "<some_tag>\nno end", []string{"text:<some_tag>\nno end"}},
		{"file_content is not a tool", "<file_content path=\"x\">\nabc\n</file_content>", []string{"text:<file_content path=\"x\">\nabc\n</file_content>"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, s := range parseAssistant(c.in) {
				if s.tool == "" {
					got = append(got, "text:"+s.text)
				} else {
					got = append(got, "tool:"+s.tool+"|"+s.body)
				}
			}
			if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
				t.Errorf("parseAssistant(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

func TestToolParams(t *testing.T) {
	body := "\n<path>src/a.go</path>\n<content>\nline </content> inside\n</content>\n<path>second</path>\n"
	p := toolParams(body)
	if p["path"] != "src/a.go" || p["content"] != "line </content> inside" {
		t.Errorf("toolParams = %q", p)
	}
	if p := toolParams("<command>go test\n"); p["command"] != "go test" {
		t.Errorf("unterminated param = %q", p)
	}
}

func TestUserSections(t *testing.T) {
	cases := []struct {
		in       string
		sections []string
		rest     string
	}{
		{"<task>\nDo it\n</task>", []string{"Do it"}, ""},
		{"intro\n<feedback>\nmore\n</feedback>\ntail <answer>a</answer>", []string{"more", "a"}, "intro\n\ntail "},
		{"<user_message>\nhi\n<environment_details>x</environment_details>\n</user_message>", []string{"hi"}, ""},
		{"<task>\nunterminated", []string{"unterminated"}, ""},
		{"<tasks>not a wrapper</tasks>", nil, "<tasks>not a wrapper</tasks>"},
		{"<feedback>\n\n</feedback>", nil, ""},
	}
	for _, c := range cases {
		secs, rest := userSections(c.in)
		if strings.Join(secs, "|") != strings.Join(c.sections, "|") || rest != c.rest {
			t.Errorf("userSections(%q) = %q, %q; want %q, %q", c.in, secs, rest, c.sections, c.rest)
		}
	}
}

func TestRemoveSections(t *testing.T) {
	cases := []struct{ in, tag, want string }{
		{"a<file_content path=\"x\">b</file_content>c", "file_content", "ac"},
		{"a<file_contents>b", "file_content", "a<file_contents>b"},
		{"a<file_content>b", "file_content", "a"},
		{"x<environment_details>\n1\n</environment_details>y<environment_details>2</environment_details>", "environment_details", "xy"},
	}
	for _, c := range cases {
		if got := removeSections(c.in, c.tag); got != c.want {
			t.Errorf("removeSections(%q, %q) = %q, want %q", c.in, c.tag, got, c.want)
		}
	}
}

func TestResultHeaderAndNotices(t *testing.T) {
	cases := []struct {
		in, name, rest string
		ok             bool
	}{
		{"[execute_command for 'go test'] Result:", "execute_command", "", true},
		{"[attempt_completion] Result:\nfeedback", "attempt_completion", "\nfeedback", true},
		{"[ask_followup_question for 'Is [x] ok?\nreally'] Result:", "ask_followup_question", "", true},
		{"[ERROR] You did not use a tool", "", "", false},
		{"I wrote [read_file] Result: myself", "", "", false},
	}
	for _, c := range cases {
		name, rest, ok := resultHeader(c.in)
		if ok != c.ok || (ok && (name != c.name || rest != c.rest)) {
			t.Errorf("resultHeader(%q) = %q, %q, %v", c.in, name, rest, ok)
		}
	}
	for in, want := range map[string]bool{
		"[ERROR] x": true, "\n[TASK RESUMPTION] x": true, "# task_progress RECOMMENDED\n": true,
		"# TODO LIST UPDATE REQUIRED": true, "# Heading of mine": false, "[see] this": false, "plain": false,
	} {
		if got := isNotice(in); got != want {
			t.Errorf("isNotice(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestConverterUserMessages(t *testing.T) {
	var c converter
	c.message(json.RawMessage(`{"role":"user","content":[
		{"type":"text","text":"<environment_details>\n# Current Working Directory (d:/Projects/My App) Files\n</environment_details>"},
		{"type":"text","text":"Please fix the bug in 'src/a.go' (see below for file content)\n\n<file_content path=\"src/a.go\">\npackage a\n</file_content>"}
	]}`))
	c.message(json.RawMessage(`{"role":"user","content":[
		{"type":"text","text":"[read_file for 'a.go'] Result:"},
		{"type":"text","text":"package a"},
		{"type":"text","text":"# TODO LIST UPDATE REQUIRED\nupdate it"},
		{"type":"text","text":"a later prompt"}
	]}`))
	c.message(json.RawMessage(`{"role":"system","content":"ignored"}`))
	c.message(json.RawMessage(`[1]`))
	if c.cwd != `d:\Projects\My App` {
		t.Errorf("cwd = %q", c.cwd)
	}
	want := []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, Text: "Please fix the bug in 'src/a.go' (see below for file content)"},
		{Role: model.RoleTool, Kind: model.KindToolOutput, Text: "package a"},
		{Role: model.RoleUser, Kind: model.KindText, Text: "a later prompt"},
	}
	if len(c.msgs) != len(want) {
		t.Fatalf("messages = %+v", c.msgs)
	}
	for i := range want {
		if c.msgs[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, c.msgs[i], want[i])
		}
	}
	if c.skipped != 1 {
		t.Errorf("skipped = %d, want 1", c.skipped)
	}
}

func TestCallText(t *testing.T) {
	if got := callText("", nil, ""); got != "tool" {
		t.Errorf("callText empty = %q", got)
	}
	if got := callText("search_files", []string{"src", "TODO", "extra"}, ""); got != "search_files: src TODO" {
		t.Errorf("callText = %q", got)
	}
	long := callText("x", []string{strings.Repeat("é", 3000)}, "")
	if len(long) > model.MaxToolOutput || !strings.HasPrefix(long, "x: é") {
		t.Errorf("long call text: %d bytes", len(long))
	}
}

func TestTimes(t *testing.T) {
	for in, want := range map[string]time.Time{
		`1767225600000`:          time.UnixMilli(1767225600000).UTC(),
		`1767225600000.5`:        time.UnixMilli(1767225600000).UTC(),
		`"1767225600000"`:        time.UnixMilli(1767225600000).UTC(),
		`"2026-01-02T10:00:00Z"`: time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC),
		`1767225600`:             {}, // seconds are not milliseconds
		`null`:                   {},
		`"soon"`:                 {},
		``:                       {},
	} {
		if got := msTime(json.RawMessage(in)); !got.Equal(want) {
			t.Errorf("msTime(%s) = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string]time.Time{
		"1767225600000":                        time.UnixMilli(1767225600000).UTC(),
		"019b76da-a800-7000-8000-000000000001": time.UnixMilli(1767225600000).UTC(),
		"019b76da-a800-4000-8000-000000000001": {}, // UUIDv4 carries no time
		"task-1":                               {},
		"12345":                                {},
	} {
		if got := idTime(in); !got.Equal(want) {
			t.Errorf("idTime(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestNativePath(t *testing.T) {
	for in, want := range map[string]string{
		"c:/work/a b":   `c:\work\a b`,
		`C:\work`:       `C:\work`,
		"/home/u/proj":  "/home/u/proj",
		"relative/path": "relative/path",
	} {
		if got := nativePath(in); got != want {
			t.Errorf("nativePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPromptTitle(t *testing.T) {
	if got := promptTitle("  a\n\tb  "); got != "a b" {
		t.Errorf("promptTitle = %q", got)
	}
	if got := promptTitle(strings.Repeat("ب", 300)); len([]rune(got)) != titleRunes {
		t.Errorf("promptTitle keeps %d runes", len([]rune(got)))
	}
}
