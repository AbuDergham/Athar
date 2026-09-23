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
