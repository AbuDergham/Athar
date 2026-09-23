//go:build windows

package syncer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

// lock takes the sync lock: an exclusive LockFileEx on the first byte of
// dir\sync.lock (dir is created when missing). Windows releases the lock when
// unlock closes the handle, or when the process ends, so a crash never leaves
// a stale lock. A lock held by any other handle, in this process or another,
// gives ErrLocked. The file is opened without FILE_SHARE_DELETE so it cannot
// be deleted (and recreated unlocked) while held.
func lock(dir string) (unlock func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("sync lock: %w", err)
	}
	path := filepath.Join(dir, lockName)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("sync lock %s: %w", path, err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("sync lock %s: %w", path, err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol); err != nil {
		windows.CloseHandle(h)
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("sync lock %s: %w", path, err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			windows.UnlockFileEx(h, 0, 1, 0, ol)
			windows.CloseHandle(h)
		})
	}, nil
}
