//go:build !windows

package fsread

import "os"

// openFile opens path read-only. POSIX systems let the owner rename and delete
// an open file, so plain os.Open already shares everything.
func openFile(path string) (*os.File, error) { return os.Open(path) }

// isLockViolation is always false: POSIX file locks are advisory and never
// block reads.
func isLockViolation(error) bool { return false }
