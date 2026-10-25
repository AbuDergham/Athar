//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestUnderTempShortNames: %TEMP% often holds an 8.3 short name
// (C:\Users\LONGUS~1\AppData\Local\Temp); the check must still match.
func TestUnderTempShortNames(t *testing.T) {
	long := filepath.Join(t.TempDir(), "A long temporary folder name")
	exeDir := filepath.Join(long, "Temp1_athar.zip")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	short := shortPath(t, long)
	if strings.EqualFold(short, long) {
		t.Skip("8.3 names are disabled on this volume")
	}
	if !isTemporary(exeDir, []string{short}) {
		t.Errorf("isTemporary(%q, [%q]) = false, want true", exeDir, short)
	}
	if !isTemporary(shortPath(t, exeDir), []string{long}) {
		t.Errorf("isTemporary(short exe dir, [%q]) = false, want true", long)
	}
}

func shortPath(t *testing.T, p string) string {
	t.Helper()
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Skipf("GetShortPathName: %v", err)
	}
	return windows.UTF16ToString(buf[:n])
}

func TestUnderAny(t *testing.T) {
	roots := []string{`C:\Users\u\AppData\Local\Temp`, `D:\Scratch\`}
	cases := []struct {
		dir  string
		want bool
	}{
		{`C:\Users\u\AppData\Local\Temp\Temp1_athar.zip`, true},
		{`c:\users\U\appdata\LOCAL\temp\7zS1234`, true},
		{`C:\Users\u\AppData\Local\Temp`, true},
		{`C:\Users\u\AppData\Local\Temp2\x`, false},
		{`C:\Users\u\AppData\Local`, false},
		{`D:\Scratch\a\b`, true},
		{`D:\ScratchPad`, false},
		{`D:\Tools\Athar`, false},
	}
	for _, c := range cases {
		if got := underAny(c.dir, roots); got != c.want {
			t.Errorf("underAny(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
}

func TestTempRoots(t *testing.T) {
	env := map[string]string{
		"TEMP":         `C:\T1`,
		"TMP":          `C:\T2\`,
		"LOCALAPPDATA": `C:\Users\u\AppData\Local`,
	}
	sys := &system{getenv: func(k string) string { return env[k] }, tempDir: func() string { return `C:\T0` }}
	got := strings.Join(tempRoots(sys), "|")
	for _, want := range []string{`C:\T0`, `C:\T1`, `C:\T2`, filepath.Join(`C:\Users\u\AppData\Local`, "Temp")} {
		if !strings.Contains(got, want) {
			t.Errorf("tempRoots = %s, missing %s", got, want)
		}
	}

	// A drive root as TEMP would make every folder of the drive "temporary": ignored.
	env = map[string]string{"TEMP": `D:\`}
	sys = &system{getenv: func(k string) string { return env[k] }, tempDir: func() string { return "" }}
	if got := tempRoots(sys); len(got) != 0 {
		t.Errorf("tempRoots with TEMP=D:\\ = %q, want none", got)
	}
}
