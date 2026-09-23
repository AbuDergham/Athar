//go:build !windows

package windsurf

import (
	"io/fs"
	"time"
)

// creationTime is unknown outside Windows (file systems do not reliably keep
// a birth time); StartedAt then falls back to the modification time.
func creationTime(info fs.FileInfo) (time.Time, bool) { return time.Time{}, false }
