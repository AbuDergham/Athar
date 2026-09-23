package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// rename moves files; replaced by tests to inject failures.
var rename = os.Rename

// Windows errors that antivirus scanners or indexers cause for a moment after
// a file is written; renames are retried when they occur.
const (
	errAccessDenied     = syscall.Errno(5)
	errSharingViolation = syscall.Errno(32)
	errLockViolation    = syscall.Errno(33)
)

// assetName is the GoReleaser name of the Windows exe for a release tag:
// athar_<version-without-v>_windows_<arch>.exe (.goreleaser.yaml).
func assetName(version string) (string, error) {
	if _, ok := parseVersion(version); !ok || strings.TrimSpace(version) != version {
		return "", fmt.Errorf("update: %q is not a release version", version)
	}
	return "athar_" + strings.TrimPrefix(version, "v") + "_windows_" + runtime.GOARCH + ".exe", nil
}

// replaceExe swaps the verified download next into place: any previous
// <exe>.old is removed, the running exe is renamed to <exe>.old (Windows allows
// renaming a running exe, not deleting it) and next is renamed to exe. When
// the last step fails the previous exe is moved back.
func replaceExe(exe, next string) error {
	old := exe + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("update: cannot remove %s (is an older Athar still running?): %w", filepath.Base(old), err)
	}
	if err := renameRetry(exe, old); err != nil {
		return fmt.Errorf("update: cannot move %s aside: %w", filepath.Base(exe), err)
	}
	if err := renameRetry(next, exe); err != nil {
		if rerr := renameRetry(old, exe); rerr != nil {
			return fmt.Errorf("update: cannot install the new exe (%v) nor restore the previous one, which is at %s: %w", err, old, rerr)
		}
		return fmt.Errorf("update: cannot install the new exe: %w", err)
	}
	return nil
}

// renameRetry renames from to to, retrying briefly on transient sharing errors.
func renameRetry(from, to string) error {
	var err error
	for attempt := 1; attempt <= 5; attempt++ {
		if err = rename(from, to); err == nil || !transientFileError(err) {
			return err
		}
		time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
	}
	return err
}

// transientFileError reports whether err is a sharing/lock violation that may
// clear up when another process closes its handle.
func transientFileError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case errAccessDenied, errSharingViolation, errLockViolation:
		return true
	}
	return false
}
