package fsread

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeZstd(t *testing.T, name string, data []byte) string {
	t.Helper()
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return writeTemp(t, name, buf.Bytes())
}

// collect keeps the slices handed to fn without copying them, so the test also
// proves that fn may retain a line after returning.
func collect(read func(string, func([]byte) error) error, path string) ([]string, error) {
	var kept [][]byte
	err := read(path, func(line []byte) error {
		kept = append(kept, line)
		return nil
	})
	var out []string
	for _, l := range kept {
		out = append(out, string(l))
	}
	return out, err
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var bom = string(rune(0xFEFF))

var bigLine = `{"type":"user","text":"` + strings.Repeat("x", 3<<20) + `"}`

func TestLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"lf", "a\nb\n", []string{"a", "b"}},
		{"crlf stripped", "a\r\nb\r\n", []string{"a", "b"}},
		{"unterminated last line not delivered", "a\nb", []string{"a"}},
		{"only an unterminated line", "abc", nil},
		{"empty file", "", nil},
		{"empty and blank lines skipped", "\n\na\n\r\n  \t\nb\n", []string{"a", "b"}},
		{"utf8 bom stripped", bom + "{\"a\":1}\n{\"b\":2}\n", []string{`{"a":1}`, `{"b":2}`}},
		{"inner CR kept", "a\rb\n", []string{"a\rb"}},
		{"arabic text", "{\"t\":\"مرحبا\"}\n", []string{`{"t":"مرحبا"}`}},
		{"3 MB single line", bigLine + "\n" + `{"half":`, []string{bigLine}},
		{"3 MB line with CRLF", "a\r\n" + bigLine + "\r\nb\r\n", []string{"a", bigLine, "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeTemp(t, "f.jsonl", []byte(c.in))
			got, err := collect(Lines, p)
			if err != nil {
				t.Fatalf("Lines: %v", err)
			}
			if !equalLines(got, c.want) {
				t.Errorf("Lines got %d lines %q, want %d lines", len(got), abbreviate(got), len(c.want))
			}
		})
	}
}

func TestZstdLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"lf", "a\nb\n", []string{"a", "b"}},
		{"crlf stripped", "a\r\nb\r\n", []string{"a", "b"}},
		{"final line without newline IS delivered", "a\nb", []string{"a", "b"}},
		{"final line with CR only", "a\nb\r", []string{"a", "b"}},
		{"empty and blank lines skipped", "\n\na\n\r\n  \nb\n", []string{"a", "b"}},
		{"utf8 bom stripped", bom + "a\n", []string{"a"}},
		{"3 MB single line", bigLine, []string{bigLine}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeZstd(t, "f.jsonl.zst", []byte(c.in))
			got, err := collect(ZstdLines, p)
			if err != nil {
				t.Fatalf("ZstdLines: %v", err)
			}
			if !equalLines(got, c.want) {
				t.Errorf("ZstdLines got %d lines %q, want %d lines", len(got), abbreviate(got), len(c.want))
			}
		})
	}
}

func abbreviate(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) > 40 {
			l = l[:40] + "…"
		}
		out[i] = l
	}
	return out
}

func TestLinesStopsOnCallbackError(t *testing.T) {
	errStop := errors.New("stop")
	for name, read := range map[string]func(string, func([]byte) error) error{
		"Lines":     Lines,
		"ZstdLines": ZstdLines,
	} {
		t.Run(name, func(t *testing.T) {
			data := []byte("a\nb\nc\n")
			var p string
			if name == "ZstdLines" {
				p = writeZstd(t, "f.zst", data)
			} else {
				p = writeTemp(t, "f.jsonl", data)
			}
			var seen []string
			err := read(p, func(line []byte) error {
				seen = append(seen, string(line))
				if string(line) == "b" {
					return errStop
				}
				return nil
			})
			if !errors.Is(err, errStop) {
				t.Fatalf("err = %v, want errStop", err)
			}
			if !equalLines(seen, []string{"a", "b"}) {
				t.Errorf("seen = %q, want [a b]", seen)
			}
		})
	}
}

func TestLinesMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.jsonl")
	if err := Lines(missing, func([]byte) error { return nil }); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lines(missing) = %v, want fs.ErrNotExist", err)
	}
	if err := ZstdLines(missing, func([]byte) error { return nil }); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ZstdLines(missing) = %v, want fs.ErrNotExist", err)
	}
}

func TestZstdLinesCorrupt(t *testing.T) {
	p := writeTemp(t, "bad.zst", []byte("this is not zstd data at all\n"))
	if err := ZstdLines(p, func([]byte) error { return nil }); err == nil {
		t.Error("ZstdLines on non-zstd data: want error, got nil")
	}
}

func TestReadFile(t *testing.T) {
	want := []byte("{\"a\":\"ب\"}\r\n" + strings.Repeat("z", 100000))
	p := writeTemp(t, "f.json", want)
	got, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("ReadFile returned %d bytes, want %d", len(got), len(want))
	}
	if _, err := ReadFile(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile(missing) = %v, want fs.ErrNotExist", err)
	}
}
