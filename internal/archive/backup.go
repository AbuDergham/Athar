package archive

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// keepBackups is how many backups Backup keeps (spec §4.8).
const keepBackups = 2

// backupDir is where Open looks for backups of the archive at path
// (athar-data\backup next to athar-data\archive.db).
func backupDir(path string) string { return filepath.Join(filepath.Dir(path), "backup") }

// Backup writes dir/archive-YYYY-MM-DD.db with VACUUM INTO and keeps the newest 2.
// The date is now's calendar date; a second backup on the same day replaces
// the first. It also records meta last_backup_at (RFC 3339, UTC).
func (d *DB) Backup(dir string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "archive-"+now.Format("2006-01-02")+".db")
	tmp := path + ".tmp"
	// VACUUM INTO refuses an existing output file; a leftover of an
	// interrupted backup is only ever a partial copy.
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if _, err := d.db.Exec(`VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if all := listBackups(dir); len(all) > keepBackups {
		for _, old := range all[keepBackups:] {
			os.Remove(old)
		}
	}
	if err := d.SetMeta("last_backup_at", now.UTC().Format(time.RFC3339)); err != nil {
		return path, err
	}
	return path, nil
}

// listBackups returns the backups in dir (archive-YYYY-MM-DD.db), newest first.
func listBackups(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && isBackupName(name) {
			out = append(out, filepath.Join(dir, name))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// isBackupName matches archive-YYYY-MM-DD.db.
func isBackupName(name string) bool {
	date, ok := strings.CutPrefix(name, "archive-")
	if !ok {
		return false
	}
	date, ok = strings.CutSuffix(date, ".db")
	if !ok {
		return false
	}
	_, err := time.Parse("2006-01-02", date)
	return err == nil
}
