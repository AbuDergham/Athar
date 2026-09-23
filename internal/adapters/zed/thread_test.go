package zed

import (
	"encoding/json"
	"testing"
)

func TestFirstFolder(t *testing.T) {
	cases := []struct{ paths, order, want string }{
		{"", "", ""},
		{"C:\\a", "0", "C:\\a"},
		{"C:\\a\nC:\\b", "1,0", "C:\\b"},
		{"C:\\a\nC:\\b", "0,1", "C:\\a"},
		{"C:\\a\nC:\\b\nC:\\c", "2,0,1", "C:\\b"},
		{"C:\\a\nC:\\b", "x,y", "C:\\a"}, // malformed order: stored order
		{"C:\\a\nC:\\b", "", "C:\\a"},    // missing order
		{"/home/u/p\r\n", "0", "/home/u/p"},
	}
	for _, c := range cases {
		if got := firstFolder(c.paths, c.order); got != c.want {
			t.Errorf("firstFolder(%q, %q) = %q, want %q", c.paths, c.order, got, c.want)
		}
	}
}

func TestToolCallText(t *testing.T) {
	cases := []struct {
		name, raw, input, want string
	}{
		{"terminal", "", `{"command":"dir","cd":"x"}`, "terminal: dir"},
		{"find_path", "", `{"glob":"**/*.go"}`, "find_path: **/*.go"},
		{"now", "", `{}`, "now"},
		{"now", "", `null`, "now"},
		{"spawn", "", `{"n":1}`, `spawn: {"n":1}`},
		{"read_file", `{"path":"a.go"}`, `null`, "read_file: a.go"}, // streaming: raw_input only
		{"x", `{"a":`, `null`, `x: {"a":`},
		{"", "", `{}`, "tool"},
	}
	for _, c := range cases {
		tu := toolUse{Name: c.name, Input: json.RawMessage(c.input)}
		if c.raw != "" {
			b, _ := json.Marshal(c.raw)
			tu.RawInput = b
		}
		if got := toolCallText(tu); got != c.want {
			t.Errorf("toolCallText(%s, %q, %s) = %q, want %q", c.name, c.raw, c.input, got, c.want)
		}
	}
}

func TestMentionLabel(t *testing.T) {
	cases := []struct{ uri, want string }{
		{`{"File":{"abs_path":"C:\\x\\y.go"}}`, "y.go"},
		{`{"Directory":{"abs_path":"/p/dir/"}}`, "dir"},
		{`{"Symbol":{"abs_path":"/p/a.rs","name":"main","line_range":{"start":1,"end":2}}}`, "main"},
		{`{"Thread":{"id":"t","name":"Old  chat"}}`, "Old chat"},
		{`{"Fetch":{"url":"https://example.com/a"}}`, "https://example.com/a"},
		{`{"Diagnostics":{}}`, "diagnostics"},
		{`"file:///p/z.txt"`, "z.txt"},
		{`null`, "mention"},
	}
	for _, c := range cases {
		if got := mentionLabel(json.RawMessage(c.uri)); got != c.want {
			t.Errorf("mentionLabel(%s) = %q, want %q", c.uri, got, c.want)
		}
	}
}

func TestJoiner(t *testing.T) {
	var j joiner
	j.text("see")
	j.marker("@a.go")
	j.text("and")
	j.marker("[image]")
	j.text(", done")
	if got, want := j.String(), "see @a.go and [image], done"; got != want {
		t.Errorf("joiner = %q, want %q", got, want)
	}
}
