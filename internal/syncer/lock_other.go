//go:build !windows

package syncer

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// heldLocks records the lock files this process holds. Athar ships for
// Windows only; elsewhere the lock only keeps two syncs of one process apart.
// It is the package's only mutable global state.
var heldLocks = struct {
	sync.Mutex
	paths map[string]bool
}{paths: map[string]bool{}}

// lock creates dir\sync.lock and takes the in-process lock on it; a lock
// already held gives ErrLocked.
func lock(dir string) (unlock func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("sync lock: %w", err)
	}
	path := filepath.Join(dir, lockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("sync lock %s: %w", path, err)
	}
	f.Close()
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	heldLocks.Lock()
	defer heldLocks.Unlock()
	if heldLocks.paths[path] {
		return nil, ErrLocked
	}
	heldLocks.paths[path] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			heldLocks.Lock()
			delete(heldLocks.paths, path)
			heldLocks.Unlock()
		})
	}, nil
}
