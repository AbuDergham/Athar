//go:build !windows

package continuedev

import (
	"os"
	"time"
)

// fileBirth is not portable outside Windows; callers fall back to the mtime.
func fileBirth(os.FileInfo) time.Time { return time.Time{} }
