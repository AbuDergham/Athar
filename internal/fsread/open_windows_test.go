//go:build windows

package fsread

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// While Open holds a file, the owning agent must still be able to append to,
// rename and delete it.
func TestOpenAllowsAgentToWriteRenameDelete(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, []byte("line1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	w, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("agent append while Open holds the file: %v", err)
	}
	if _, err := w.WriteString("line2\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()

	moved := filepath.Join(dir, "moved.jsonl")
	if err := os.Rename(p, moved); err != nil {
		t.Fatalf("os.Rename while Open holds the file: %v", err)
	}
	if err := os.Remove(moved); err != nil {
		t.Fatalf("os.Remove while Open holds the file: %v", err)
	}

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "line1\nline2\n" {
		t.Errorf("read %q through the held handle", got)
	}
}

func TestOpenMissing(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "missing.jsonl"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open(missing) = %v, want fs.ErrNotExist", err)
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		t.Errorf("Open(missing) error %T is not a *fs.PathError", err)
	}
}

func TestOpenLongPath(t *testing.T) {
	dir := t.TempDir()
	for len(dir) < 300 {
		dir = filepath.Join(dir, strings.Repeat("d", 40))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "0123456789abcdef0123456789abcdef0123.jsonl")
	if err := os.WriteFile(p, []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := collect(Lines, p)
	if err != nil {
		t.Fatalf("Lines on a %d-char path: %v", len(p), err)
	}
	if !equalLines(got, []string{"a", "b"}) {
		t.Errorf("got %q", got)
	}
}

// SQLite on Windows holds mandatory byte-range locks at the 1 GiB "pending
// byte" offset; copying a big database must zero-fill that range instead of
// failing with ERROR_LOCK_VIOLATION. The test uses a small lock offset.
func TestCopyDBFileSkipsLockedRange(t *testing.T) {
	const lockStart = 4096
	src := filepath.Join(t.TempDir(), "big.db")
	data := bytes.Repeat([]byte("0123456789abcdef"), 3*4096/16) // 12 KiB, no zero bytes
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}

	name, _ := windows.UTF16PtrFromString(src)
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	ol := &windows.Overlapped{Offset: lockStart}
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, sqliteLockSize, 0, ol); err != nil {
		t.Fatal(err)
	}
	defer windows.UnlockFileEx(h, 0, sqliteLockSize, 0, &windows.Overlapped{Offset: lockStart})

	// Sanity: a plain read of the locked range fails.
	f, err := openFile(src)
	if err != nil {
		t.Fatal(err)
	}
	_, rerr := f.ReadAt(make([]byte, 16), lockStart)
	f.Close()
	if !isLockViolation(rerr) {
		t.Fatalf("expected a lock violation reading the locked range, got %v", rerr)
	}

	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := copyDBFile(dst, src, lockStart); err != nil {
		t.Fatalf("copyDBFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(data) {
		t.Fatalf("copy has %d bytes, want %d", len(got), len(data))
	}
	want := append([]byte(nil), data...)
	copy(want[lockStart:lockStart+sqliteLockSize], make([]byte, sqliteLockSize))
	if !bytes.Equal(got, want) {
		t.Error("copy differs from source outside the zero-filled lock range")
	}
}
