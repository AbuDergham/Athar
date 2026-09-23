package config

import (
	"reflect"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		ok   bool
	}{
		{"codex resume {id}", []string{"codex", "resume", "{id}"}, true},
		{"  codex\tresume   {id}  ", []string{"codex", "resume", "{id}"}, true},
		{`"C:\Program Files\X\x.exe" --resume {id}`, []string{`C:\Program Files\X\x.exe`, "--resume", "{id}"}, true},
		{`x --dir "{cwd}"`, []string{"x", "--dir", "{cwd}"}, true},
		{`x --name="a b"c`, []string{"x", "--name=a bc"}, true},
		{`x ""`, []string{"x", ""}, true},
		{`C:\bin\x.exe`, []string{`C:\bin\x.exe`}, true},
		{"", []string{}, true},
		{"   ", []string{}, true},
		{`"unterminated`, nil, false},
		{`x "a b`, nil, false},
	}
	for _, tc := range cases {
		got, err := SplitCommand(tc.in)
		if !tc.ok {
			if err == nil {
				t.Errorf("SplitCommand(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("SplitCommand(%q): %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) || got == nil {
			t.Errorf("SplitCommand(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestCheckArgs(t *testing.T) {
	cases := []struct {
		args []string
		ok   bool
	}{
		{[]string{"codex", "resume", "{id}"}, true},
		{[]string{`C:\Program Files\x.exe`, "--dir", "{cwd}", "--id={id}"}, true},
		{[]string{"x", "{"}, true},   // a lone brace is not a placeholder
		{[]string{"x", "a}b"}, true}, // nor is a lone closing brace
		{[]string{"x", "{id}{cwd}"}, true},
		{[]string{"x", "{Id}"}, false},
		{[]string{"x", "{}"}, false},
		{[]string{"x", "a&b"}, false},
		{[]string{"x", "a|b"}, false},
		{[]string{"x", "<in"}, false},
		{[]string{"x", ">out"}, false},
		{[]string{"x", "^"}, false},
		{[]string{"x", "100%"}, false},
		{[]string{"x", "hi!"}, false},
		{[]string{"x", `a"b`}, false},
		{[]string{"x", "a\tb"}, false},
		{[]string{"x", "a\x00b"}, false},
		{[]string{"x", "a\x7fb"}, false},
	}
	for _, tc := range cases {
		err := checkArgs(tc.args)
		if (err == nil) != tc.ok {
			t.Errorf("checkArgs(%q) err = %v, want ok=%v", tc.args, err, tc.ok)
		}
	}
}
