// Package syncer runs adapters and applies their results to the archive (spec §4.3).
package syncer

import (
	"context"
	"errors"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
)

var errTODO = errors.New("sync: not implemented")

// ErrLocked is returned when another process holds the sync lock.
var ErrLocked = errors.New("another Athar window is syncing")

// Options control one sync run.
type Options struct {
	Machine string // machine id (MachineGuid)
	Rescan  bool   // ignore stored fingerprints
	LockDir string // folder for sync.lock (athar-data)
}

// Progress is reported after each source.
type Progress struct {
	Done, Total int
	Adapter     string
	Path        string
}

// Report summarizes a run.
type Report struct {
	Sources, Read, Unchanged, Failed int
	NewlyMissing                     int
	Duration                         time.Duration
}

// Run syncs every source of every adapter once. Sources whose fingerprint and
// adapter version are unchanged are skipped. Each source is applied in its own
// transaction; a panic or error in one adapter is recorded and does not stop others.
// At the end FinishSync marks sessions whose sources disappeared.
func Run(ctx context.Context, db *archive.DB, list []adapters.Adapter, env adapters.Env, opts Options, progress func(Progress)) (Report, error) {
	return Report{}, errTODO
}

// MachineID returns the Windows MachineGuid (read-only registry read), or the
// hostname when unavailable.
func MachineID() string { return "" }
