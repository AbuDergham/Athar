package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestAthar checks the committed resources: they are what the generator
// writes for winres/athar.ico, and every data entry points at an image.
func TestAthar(t *testing.T) {
	dir := filepath.Join("..", "..", "cmd", "athar")
	ico, err := os.ReadFile(filepath.Join(dir, "winres", "athar.ico"))
	if err != nil {
		t.Fatal(err)
	}
	images, group, err := parseICO(ico)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) < 4 || binary.LittleEndian.Uint16(group[4:]) != uint16(len(images)) {
		t.Fatalf("%d images, group %x", len(images), group[:6])
	}
	section, relocs := buildSection(images, group)
	if len(relocs) != len(images)+1 {
		t.Fatalf("%d data entries, want %d", len(relocs), len(images)+1)
	}
	for i, r := range relocs {
		off, size := binary.LittleEndian.Uint32(section[r:]), binary.LittleEndian.Uint32(section[r+4:])
		want := group
		if i < len(images) {
			want = images[i]
		}
		if off%8 != 0 || !bytes.Equal(section[off:off+size], want) {
			t.Errorf("data entry %d points at the wrong bytes (offset %d, size %d)", i, off, size)
		}
	}

	out := t.TempDir()
	if err := run(filepath.Join(dir, "winres", "athar.ico"), out); err != nil {
		t.Fatal(err)
	}
	for _, a := range arches {
		name := "rsrc_windows_" + a.goarch + ".syso"
		got, _ := os.ReadFile(filepath.Join(out, name))
		committed, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, committed) {
			t.Errorf("cmd/athar/%s is stale: run go generate ./cmd/athar", name)
		}
	}
}

func TestParseICORejects(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":     nil,
		"png":       []byte("\x89PNG\r\n\x1a\n"),
		"no images": {0, 0, 1, 0, 0, 0},
		"truncated": {0, 0, 1, 0, 1, 0, 16, 16},
		"outside":   append([]byte{0, 0, 1, 0, 1, 0}, 16, 16, 0, 0, 1, 0, 32, 0, 0xff, 0, 0, 0, 22, 0, 0, 0),
	} {
		if _, _, err := parseICO(b); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
