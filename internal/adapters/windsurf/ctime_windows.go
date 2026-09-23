//go:build windows

package windsurf

import (
	"io/fs"
	"syscall"
	"time"
)

// creationTime returns the file's creation time from its Windows attributes.
func creationTime(info fs.FileInfo) (time.Time, bool) {
	d, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || d == nil {
		return time.Time{}, false
	}
	return time.Unix(0, d.CreationTime.Nanoseconds()), true
}
