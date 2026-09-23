package launch

import (
	"strings"
	"testing"
)

func TestValidID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"0f3c9a52-1b2c-4d5e-8f90-a1b2c3d4e5f6", true},
		{"ses_3a1b2c", true},
		{"agent:abc.def", true},
		{"", false},
		{`abc"&calc`, false},
		{"abc%PATH%", false},
		{"abc!x!", false},
		{"abc^&x", false},
		{"abc def", false},
		{"abc\ndef", false},
		{"abc&calc", false},
		{"أحمد", false},
	}
	for _, c := range cases {
		if got := validID(c.id); got != c.want {
			t.Errorf("validID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

func TestCmdSafe(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{`D:\Projects\Alpha`, true},
		{`D:\My Projects\it's & x;y (1)`, true},
		{`C:\Users\u\مشروع`, true},
		{"", false},
		{`D:\a"b`, false},
		{`D:\100% done`, false},
		{`D:\wow!`, false},
		{`D:\a^b`, false},
		{"D:\\a\rb", false},
		{"D:\\a\nb", false},
		{"D:\\a\tb", false},
		{"D:\\a\x00b", false},
	}
	for _, c := range cases {
		if got := cmdSafe(c.s); got != c.want {
			t.Errorf("cmdSafe(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestSanitizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{`Fix the "deploy" & tests | 100%`, "Fix the deploy tests 100"},
		{"a.b-c_d·e", "a.b-c_d·e"},
		{"إصلاح النشر ٢٠٢٦", "إصلاح النشر ٢٠٢٦"},
		{"line1\nline2\tx", "line1 line2 x"},
		{"--rm -rf /", "rm -rf"},
		{"(^!<>)", ""},
		{"  spaced   out  ", "spaced out"},
		{strings.Repeat("ab", 50), strings.Repeat("ab", 30)},
		{strings.Repeat("م", 70), strings.Repeat("م", 60)},
	}
	for _, c := range cases {
		if got := SanitizeTitle(c.in); got != c.want {
			t.Errorf("SanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuoteCmd(t *testing.T) {
	cases := []struct{ in, want string }{
		{"claude", "claude"},
		{"--resume", "--resume"},
		{`D:\Projects\Alpha`, `D:\Projects\Alpha`},
		{`D:\My Projects\Beta`, `"D:\My Projects\Beta"`},
		{`D:\a&b`, `"D:\a&b"`},
		{`D:\a;b`, `"D:\a;b"`},
		{`--x=y`, `"--x=y"`},
		{`D:\My Projects\`, `"D:\My Projects\\"`},
		{`D:\x y\\`, `"D:\x y\\\\"`},
		{"", `""`},
	}
	for _, c := range cases {
		if got := quoteCmd(c.in); got != c.want {
			t.Errorf("quoteCmd(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuotePS(t *testing.T) {
	cases := []struct{ in, want string }{
		{`D:\O'Brien`, `'D:\O''Brien'`},
		{`D:\$HOME`, `'D:\$HOME'`},
		{"D:\\a`b", "'D:\\a`b'"},
		{"D:\\it\u2019s", "'D:\\it\u2019\u2019s'"},
		{"", "''"},
	}
	for _, c := range cases {
		if got := quotePS(c.in); got != c.want {
			t.Errorf("quotePS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	args := []struct {
		in, want string
	}{
		{"--resume", "--resume"},
		{"resume", "resume"},
		{"ses_3a1b2c", "ses_3a1b2c"},
		{"gpt-4.1", "gpt-4.1"},
		{"0f3c9a52-1b2c", "'0f3c9a52-1b2c'"},
		{"1e5", "'1e5'"},
		{"-1", "'-1'"},
		{"a,b", "'a,b'"},
		{"@x", "'@x'"},
		{`D:\x`, `'D:\x'`},
		{"--%", "'--%'"},
	}
	for _, c := range args {
		if got := psArg(c.in); got != c.want {
			t.Errorf("psArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
