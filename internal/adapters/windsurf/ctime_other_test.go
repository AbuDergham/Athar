//go:build !windows

package windsurf

import (
	"testing"
	"time"
)

// setCreationTime is not supported outside Windows.
func setCreationTime(t *testing.T, p string, created time.Time) bool { return false }
