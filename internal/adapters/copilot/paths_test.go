package copilot

import (
	"encoding/json"
	"testing"
)

func TestWorkspacePath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"file:///c%3A/work/demo", `C:\work\demo`},
		{"file:///d%3A/notes%20app/", `D:\notes app`},
		{"FILE:///e:/x/y", `E:\x\y`},
		{"file:///c%3A/", `C:\`},
		{"file://server/share/dir", `\\server\share\dir`},
		{"file://localhost/c%3A/w", `C:\w`},
		{"file:///c%3A/w/file.go#12-20", `C:\w\file.go`},
		{"file:///home/dev/app", "/home/dev/app"},
		{"vscode-remote://wsl%2Bubuntu/home/dev/app", "vscode-remote://wsl%2Bubuntu/home/dev/app"},
		{"  ", ""},
		{"file:///c%3A/bad%zzescape", `C:\bad%zzescape`},
	}
	for _, tt := range tests {
		if got := workspacePath(tt.in); got != tt.want {
			t.Errorf("workspacePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUriPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{`{"$mid":1,"fsPath":"c:\\w\\a.go","path":"/c:/w/a.go","scheme":"file"}`, `C:\w\a.go`},
		{`{"$mid":1,"path":"/c:/w/a.go","scheme":"file"}`, `C:\w\a.go`},
		{`{"$mid":1,"path":"/x","scheme":"untitled","external":"untitled:/x"}`, "untitled:/x"},
		{`{"path":"/x","scheme":"vscode-remote"}`, "/x"},
		{`"not an object"`, ""},
		{``, ""},
	}
	for _, tt := range tests {
		if got := uriPath(json.RawMessage(tt.in)); got != tt.want {
			t.Errorf("uriPath(%s) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCleanLinks(t *testing.T) {
	tests := []struct {
		in   string
		all  bool
		want string
	}{
		{"Read [](file:///c%3A/w/a.go), lines 1 to 5", true, `Read C:\w\a.go, lines 1 to 5`},
		{"See [docs](https://example.com) and [](file:///c%3A/w/b.go#3)", false, `See [docs](https://example.com) and C:\w\b.go`},
		{"See [docs](https://example.com)", true, "See docs"},
		{"no links here", true, "no links here"},
	}
	for _, tt := range tests {
		if got := cleanLinks(tt.in, tt.all); got != tt.want {
			t.Errorf("cleanLinks(%q, %v) = %q, want %q", tt.in, tt.all, got, tt.want)
		}
	}
}

func TestReferenceName(t *testing.T) {
	tests := []struct {
		p    part
		want string
	}{
		{part{InlineReference: json.RawMessage(`{"$mid":1,"fsPath":"c:\\w\\lexer.go","scheme":"file"}`)}, "lexer.go"},
		{part{InlineReference: json.RawMessage(`{"uri":{"path":"/c:/w/x.go","scheme":"file"},"range":{}}`)}, "x.go"},
		{part{InlineReference: json.RawMessage(`{"name":"Scan","kind":12,"location":{"uri":{"fsPath":"c:\\w\\x.go"}}}`)}, "Scan"},
		{part{InlineReference: json.RawMessage(`{"location":{"uri":{"fsPath":"c:\\w\\y.go"}}}`)}, "y.go"},
		{part{Name: "custom", InlineReference: json.RawMessage(`{"fsPath":"c:\\w\\z.go"}`)}, "custom"},
		{part{InlineReference: json.RawMessage(`17`)}, ""},
	}
	for i, tt := range tests {
		if got := referenceName(tt.p); got != tt.want {
			t.Errorf("case %d: referenceName = %q, want %q", i, got, tt.want)
		}
	}
}

func TestToolCallText(t *testing.T) {
	tests := []struct {
		p    part
		want string
	}{
		{part{ToolID: "copilot_runInTerminal", ToolSpecificData: json.RawMessage(`{"kind":"terminal","commandLine":{"original":"go vet","toolEdited":"go vet ./..."}}`)}, "copilot_runInTerminal: go vet ./..."},
		{part{ToolID: "copilot_runInTerminal", ToolSpecificData: json.RawMessage(`{"kind":"terminal","commandLine":{"original":"ls","userEdited":"dir"}}`)}, "copilot_runInTerminal: dir"},
		{part{ToolID: "x", InvocationMessage: json.RawMessage(`"Doing it"`), PastTenseMessage: json.RawMessage(`{"value":"  "}`)}, "x: Doing it"},
		{part{ToolID: "x", ToolSpecificData: json.RawMessage(`{"kind":"todoList"}`), InvocationMessage: json.RawMessage(`{"value":"Plan"}`)}, "x: Plan"},
		{part{}, "tool"},
	}
	for i, tt := range tests {
		if got := toolCallText(tt.p); got != tt.want {
			t.Errorf("case %d: toolCallText = %q, want %q", i, got, tt.want)
		}
	}
}

func TestMsTime(t *testing.T) {
	if got := msTime(json.RawMessage(`1767225600000`)); !got.Equal(ms(1767225600000)) {
		t.Errorf("number: %v", got)
	}
	if got := msTime(json.RawMessage(`"1767225600000"`)); !got.Equal(ms(1767225600000)) {
		t.Errorf("string: %v", got)
	}
	for _, in := range []string{``, `null`, `0`, `-5`, `"x"`, `{}`} {
		if got := msTime(json.RawMessage(in)); !got.IsZero() {
			t.Errorf("msTime(%s) = %v, want zero", in, got)
		}
	}
}
