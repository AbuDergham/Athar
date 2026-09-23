package agy

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestURIToPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"file:///C:/work/alpha%20one", `C:\work\alpha one`},
		{"file:///d:/x/y/", `D:\x\y`},
		{"file:///c%3A/Users/me", `C:\Users\me`},
		{"file:///C:/", `C:\`},
		{"file:///C:", `C:\`},
		{"file://server/share/dir", `\\server\share\dir`},
		{"file:///home/me/proj", "/home/me/proj"},
		{`C:\already\a\path`, `C:\already\a\path`},
		{"", ""},
		{"file:///C:/100%25/%D8%B9%D8%B1%D8%A8%D9%8A", `C:\100%\عربي`},
	}
	for _, c := range cases {
		if got := uriToPath(c.in); got != c.want {
			t.Errorf("uriToPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToolCallText(t *testing.T) {
	long := `{"Other":"` + strings.Repeat("x", 500) + `"}`
	cases := []struct{ name, args, want string }{
		{"run_command", `{"CommandLine":"go test ./...","Cwd":"C:\\p","toolAction":"a","toolSummary":"s"}`, "run_command: go test ./..."},
		{"view_file", `{"AbsolutePath":"C:\\p\\a.go","StartLine":3}`, `view_file: C:\p\a.go`},
		{"write_to_file", `{"TargetFile":"C:\\p\\b.go","CodeContent":"package b"}`, `write_to_file: C:\p\b.go`},
		{"replace_file_content", `{"TargetFile":"C:\\p\\c.go","TargetContent":"x"}`, `replace_file_content: C:\p\c.go`},
		{"list_dir", `{"DirectoryPath":"C:\\p"}`, `list_dir: C:\p`},
		{"grep_search", `{"Query":"TODO","SearchPath":"C:\\p","IsRegex":false}`, `grep_search: TODO C:\p`},
		{"search_web", `{"query":"go protowire"}`, "search_web: go protowire"},
		{"read_url_content", `{"Url":"https://example.com/x"}`, "read_url_content: https://example.com/x"},
		{"manage_task", `{"Action":"list","toolAction":"a","toolSummary":"s"}`, `manage_task: {"Action":"list","toolAction":"a","toolSummary":"s"}`},
		{"odd", "not json", "odd: not json"},
		{"bare", "", "bare"},
		{"bare", "{}", "bare"},
	}
	for _, c := range cases {
		if got := toolCallText(c.name, c.args); got != c.want {
			t.Errorf("toolCallText(%q, %q) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
	got := toolCallText("other", long)
	if n := utf8.RuneCountInString(strings.TrimPrefix(got, "other: ")); n != 200 {
		t.Errorf("long args kept %d runes, want 200", n)
	}
}

func TestShortTitle(t *testing.T) {
	if got := shortTitle("  hello \n\t world  "); got != "hello world" {
		t.Errorf("shortTitle = %q", got)
	}
	long := strings.Repeat("ب", 150)
	if got := shortTitle(long); utf8.RuneCountInString(got) != 100 {
		t.Errorf("shortTitle kept %d runes", utf8.RuneCountInString(got))
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := strings.Repeat("ع", 2000) // 2 bytes each
	got := truncateUTF8(s, 2047)
	if len(got) > 2047 || !utf8.ValidString(got) || len(got) != 2046 {
		t.Errorf("truncateUTF8 len %d valid %v", len(got), utf8.ValidString(got))
	}
	if truncateUTF8("abc", 10) != "abc" {
		t.Error("short string changed")
	}
}

func TestPBTimestamp(t *testing.T) {
	want := time.Date(2026, 6, 17, 8, 0, 1, 250000000, time.UTC)
	b := cat(pbVarint(1, uint64(want.Unix())), pbVarint(2, uint64(want.Nanosecond())))
	if got, ok := pbTimestamp(b); !ok || !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("pbTimestamp = %v %v", got, ok)
	}
	if _, ok := pbTimestamp(nil); ok {
		t.Error("empty timestamp accepted")
	}
	if _, ok := pbTimestamp([]byte{0xff}); ok {
		t.Error("garbage timestamp accepted")
	}
}
