package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
)

// tempPrefix names the per-source folders Run creates under Env.TempDir.
const tempPrefix = "athar-src-"

// staleTemp is the age after which a per-source folder is taken for the
// leftover of a sync that crashed, and removed.
const staleTemp = 24 * time.Hour

// defaultTempDir is %TEMP%\athar\copy (spec §4.3).
func defaultTempDir() string { return filepath.Join(os.TempDir(), "athar", "copy") }

// prepareTemp creates the base temp folder and removes per-source folders
// left by a crashed sync. Failures are ignored: a source that needs the
// folder reports its own error.
func prepareTemp(base string) {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleTemp {
			os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}

// sourceEnv returns the Env for one source: TempDir is a new private folder
// under the base one, and cleanup removes it with whatever the adapter left
// there. If the folder cannot be created the base folder is used (CopySQLite
// makes its own subfolders there).
func (r *runner) sourceEnv() (adapters.Env, func()) {
	env := r.env
	dir, err := os.MkdirTemp(r.env.TempDir, tempPrefix)
	if err != nil {
		return env, func() {}
	}
	env.TempDir = dir
	return env, func() { os.RemoveAll(dir) }
}
