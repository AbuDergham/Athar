//go:build windows

package continuedev

import (
	"os"
	"syscall"
	"time"
)

// fileBirth is the file's creation time (UTC), or zero when unavailable.
// Continue rewrites session files in place, which keeps the creation time.
func fileBirth(fi os.FileInfo) time.Time {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok && d != nil {
		if ns := d.CreationTime.Nanoseconds(); ns > 0 {
			return time.Unix(0, ns).UTC()
		}
	}
	return time.Time{}
}
