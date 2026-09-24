//go:build windows

package archive

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// A healthy archive that another program holds open without sharing
// (a sync client, a backup tool) is reported as damaged.
func TestReviewLockedArchiveReportedDamaged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "athar-data", "archive.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustApply(t, d, src("a"), live("s1", "t", "hello world"))
	d.Close()

	name, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0 /* no sharing */, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	windows.CloseHandle(h)
	var dmg *ErrDamaged
	t.Logf("Open while locked: %v (ErrDamaged=%v)", err, errors.As(err, &dmg))
	if errors.As(err, &dmg) {
		t.Errorf("BUG: a healthy but locked archive is reported damaged: %v", err)
	}
	// And after the lock is gone, it opens fine.
	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	d.Close()
}
