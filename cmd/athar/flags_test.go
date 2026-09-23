package main

import (
	"errors"
	"flag"
	"io"
	"testing"
)

func TestParseFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    options
		wantErr bool
		help    bool
	}{
		{name: "none", args: nil, want: options{}},
		{name: "all", args: []string{"--sync", "--rescan", "--data-dir", `D:\x`, "--no-update-check"},
			want: options{sync: true, rescan: true, dataDir: `D:\x`, noUpdateCheck: true}},
		{name: "equals form", args: []string{"--data-dir=D:\\y"}, want: options{dataDir: `D:\y`}},
		{name: "single dash", args: []string{"-version"}, want: options{version: true}},
		{name: "version", args: []string{"--version"}, want: options{version: true}},
		{name: "unknown flag", args: []string{"--bogus"}, wantErr: true},
		{name: "missing value", args: []string{"--data-dir"}, wantErr: true},
		{name: "empty data dir", args: []string{"--data-dir", ""}, wantErr: true},
		{name: "positional", args: []string{"telegram"}, wantErr: true},
		{name: "help", args: []string{"--help"}, wantErr: true, help: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseFlags(c.args, io.Discard)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseFlags(%q) = %+v, want an error", c.args, got)
				}
				if errors.Is(err, flag.ErrHelp) != c.help {
					t.Fatalf("parseFlags(%q) error %v, help = %v", c.args, err, c.help)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFlags(%q): %v", c.args, err)
			}
			if got != c.want {
				t.Fatalf("parseFlags(%q) = %+v, want %+v", c.args, got, c.want)
			}
		})
	}
}

func TestRestartArgs(t *testing.T) {
	got := restartArgs(options{rescan: true, noUpdateCheck: true}, `D:\data`)
	want := []string{"--data-dir", `D:\data`, "--no-update-check"}
	if len(got) != len(want) {
		t.Fatalf("restartArgs = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("restartArgs = %q, want %q", got, want)
		}
	}
	if got := restartArgs(options{}, ""); len(got) != 0 {
		t.Fatalf("restartArgs(default) = %q, want none", got)
	}
}
