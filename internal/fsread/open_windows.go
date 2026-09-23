package fsread

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// openFile opens path with GENERIC_READ and FILE_SHARE_READ|WRITE|DELETE, so the
// agent that owns the file can append to, rename or delete it while we read.
func openFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(longPath(path))
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// longPath returns an extended-length (\\?\) form of path when it is too long
// for the classic Win32 MAX_PATH limit; CreateFile does not add the prefix itself.
func longPath(path string) string {
	const limit = 248 // MAX_PATH minus room for an 8.3 file name, as the os package uses
	if len(path) < limit || strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return path
	}
	abs, err := filepath.Abs(path) // also cleans "." / ".." and converts "/" to "\"
	if err != nil {
		return path
	}
	if strings.HasPrefix(abs, `\\`) { // UNC \\server\share\...
		return `\\?\UNC\` + abs[2:]
	}
	return `\\?\` + abs
}

// isLockViolation reports whether err is ERROR_LOCK_VIOLATION, which ReadFile
// returns for a byte range another handle holds an exclusive LockFileEx lock on.
func isLockViolation(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
