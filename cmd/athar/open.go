package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/archive"
)

// openArchive opens the archive at path. When it is damaged (spec §2.8,
// §4.8) and ask is set, it shows the SQLite error and offers the newest
// backup; on "y" the damaged file (with its -wal/-shm) is renamed to
// archive.damaged-<time>.db, the backup is copied in its place and opened.
// If that copy is damaged too, the next backup is offered. Without ask, or
// when the answer is no, the damaged archive is left untouched and an error
// explaining what to do is returned. An empty archive is never created over
// a damaged one.
func openArchive(path string, sys *system, ask bool) (*archive.DB, error) {
	db, err := archive.Open(path)
	var dmg *archive.ErrDamaged
	if !errors.As(err, &dmg) {
		return db, err
	}
	hint := "There is no backup to restore. The file was left untouched: " +
		"move it away to start a new archive, or run athar with --data-dir <path>."
	for i, backup := range dmg.Backups {
		if !ask {
			return nil, &damagedError{dmg: dmg, hint: "Newest backup: " + backup +
				"\nRun athar in a terminal window (without --sync) to restore it."}
		}
		label := "Newest backup"
		if i > 0 {
			fmt.Fprintln(sys.stderr, "That backup is damaged too.")
			label = "Next backup"
		}
		fmt.Fprintf(sys.stderr, "%v\n%s: %s\n", dmg, label, backup)
		kept := damagedName(path, sys.now())
		fmt.Fprintf(sys.stderr, "Restore it? The damaged archive is kept as %s. [y/N] ", filepath.Base(kept))
		if !yes(sys.readLine()) {
			return nil, &damagedError{dmg: dmg, hint: "The archive was left untouched."}
		}
		if err := restoreBackup(path, kept, backup); err != nil {
			return nil, fmt.Errorf("restoring %s: %w", backup, err)
		}
		fmt.Fprintf(sys.stderr, "Restored %s.\n", backup)
		db, err = archive.Open(path)
		if !errors.As(err, &dmg) {
			return db, err
		}
		hint = "Every backup is damaged too; the damaged files were kept next to the archive " +
			"(archive.damaged-*.db). Run athar with --data-dir <path> to start a new archive."
	}
	return nil, &damagedError{dmg: dmg, hint: hint}
}

// damagedError is an *archive.ErrDamaged with advice for the user.
type damagedError struct {
	dmg  *archive.ErrDamaged
	hint string
}

func (e *damagedError) Error() string { return e.dmg.Error() + "\n" + e.hint }
func (e *damagedError) Unwrap() error { return e.dmg }

// sqliteSidecars are the files SQLite keeps next to a database.
var sqliteSidecars = []string{"", "-wal", "-shm", "-journal"}

// damagedName is where the damaged archive at path is kept:
// archive.damaged-YYYYMMDD-HHMMSS.db next to it, with -2, -3… when taken.
func damagedName(path string, at time.Time) string {
	base := strings.TrimSuffix(path, filepath.Ext(path)) + ".damaged-" + at.Format("20060102-150405")
	for n := 1; ; n++ {
		name := base + ".db"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.db", base, n)
		}
		taken := false
		for _, s := range sqliteSidecars {
			if _, err := os.Lstat(name + s); !errors.Is(err, fs.ErrNotExist) {
				taken = true
			}
		}
		if !taken {
			return name
		}
	}
}

// restoreBackup renames the archive at path and its sidecars to kept (a stale
// -wal left next to the backup copy would be replayed into it), then copies
// backup to path. On failure it puts the damaged files back.
func restoreBackup(path, kept, backup string) error {
	var moved []string
	undo := func() {
		for _, s := range moved {
			os.Rename(kept+s, path+s)
		}
	}
	for _, s := range sqliteSidecars {
		if _, err := os.Lstat(path + s); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := os.Rename(path+s, kept+s); err != nil {
			undo()
			return err
		}
		moved = append(moved, s)
	}
	if err := copyFile(backup, path); err != nil {
		undo()
		return err
	}
	return nil
}

// copyFile copies src to dst through a temporary file renamed into place.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".restoring"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// backupEvery is how often the archive is backed up at startup (spec §4.8).
const backupEvery = 7 * 24 * time.Hour

// backupDue reports whether a backup is due, given meta last_backup_at.
// A time in the future (the clock was wrong) counts as due.
func backupDue(last string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, last)
	return err != nil || now.Sub(t) >= backupEvery || t.After(now)
}

// backupJob is a weekly backup running in the background.
type backupJob struct {
	done chan struct{}
	err  error
}

// startBackup starts the weekly backup (athar-data\backup\archive-YYYY-MM-DD.db,
// the newest 2 kept) when one is due. A new archive that has never
// finished a sync has nothing worth keeping and is not backed up.
func startBackup(db *archive.DB, dataDir string, now time.Time) *backupJob {
	j := &backupJob{done: make(chan struct{})}
	lastSync, _ := db.Meta("last_sync_at")
	last, err := db.Meta("last_backup_at")
	if lastSync == "" || err != nil || !backupDue(last, now) {
		close(j.done)
		return j
	}
	go func() {
		defer close(j.done)
		_, j.err = db.Backup(filepath.Join(dataDir, backupName), now)
	}()
	return j
}

// wait waits for the backup; if it takes longer than notice it says so, since
// the window would otherwise look frozen. It returns the backup's error.
func (j *backupJob) wait(w io.Writer, notice time.Duration) error {
	select {
	case <-j.done:
		return j.err
	case <-time.After(notice):
	}
	fmt.Fprintln(w, "Athar: finishing the weekly backup…")
	<-j.done
	return j.err
}
