package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/archive"
)

func TestBackupDue(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		last string
		want bool
	}{
		{"", true},
		{"garbage", true},
		{now.Add(-8 * 24 * time.Hour).Format(time.RFC3339), true},
		{now.Add(-7 * 24 * time.Hour).Format(time.RFC3339), true},
		{now.Add(-6 * 24 * time.Hour).Format(time.RFC3339), false},
		{now.Add(-time.Hour).Format(time.RFC3339), false},
		{now.Add(48 * time.Hour).Format(time.RFC3339), true}, // clock was wrong: back up again
	}
	for _, c := range cases {
		if got := backupDue(c.last, now); got != c.want {
			t.Errorf("backupDue(%q) = %v, want %v", c.last, got, c.want)
		}
	}
}

// damagedFixture writes a damaged archive.db and one valid backup whose meta
// "marker" is "from-backup". (No -wal: archive.Open itself lets SQLite remove
// an invalid WAL; sidecars are covered by TestRestoreBackupMovesSidecars.)
type damagedFixture struct {
	dir, path, backup string
	damaged           []byte
}

func newDamagedFixture(t *testing.T) *damagedFixture {
	t.Helper()
	f := &damagedFixture{dir: t.TempDir()}
	f.path = filepath.Join(f.dir, archiveName)

	good, err := archive.Open(filepath.Join(t.TempDir(), "good.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := good.SetMeta("marker", "from-backup"); err != nil {
		t.Fatal(err)
	}
	f.backup, err = good.Backup(filepath.Join(f.dir, "backup"), time.Date(2026, 9, 20, 10, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	good.Close()

	f.damaged = bytes.Repeat([]byte("this is not a SQLite database "), 200)
	if err := os.WriteFile(f.path, f.damaged, 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// assertUntouched checks the damaged archive is exactly where and as it was.
func (f *damagedFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if got, err := os.ReadFile(f.path); err != nil || !bytes.Equal(got, f.damaged) {
		t.Errorf("%s changed (err %v)", f.path, err)
	}
	if m, _ := filepath.Glob(filepath.Join(f.dir, "archive.damaged-*")); len(m) != 0 {
		t.Errorf("unexpected renamed files: %q", m)
	}
}

func TestOpenArchiveRestoresBackup(t *testing.T) {
	f := newDamagedFixture(t)
	sys, out := testSystem(t, "y\n")
	sys.console = true
	sys.now = func() time.Time { return time.Date(2026, 9, 23, 21, 30, 5, 0, time.Local) }

	db, err := openArchive(f.path, sys, true)
	if err != nil {
		t.Fatalf("openArchive: %v\n%s", err, out)
	}
	defer db.Close()
	if v, _ := db.Meta("marker"); v != "from-backup" {
		t.Errorf("restored archive meta marker = %q, want from-backup", v)
	}
	for _, want := range []string{f.path, "damaged", f.backup, "[y/N]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prompt does not mention %q:\n%s", want, out)
		}
	}

	// The damaged archive is kept under a new name.
	kept := filepath.Join(f.dir, "archive.damaged-20260923-213005.db")
	if got, err := os.ReadFile(kept); err != nil || !bytes.Equal(got, f.damaged) {
		t.Errorf("damaged archive not kept as %s (err %v)", kept, err)
	}
	// The backup itself is untouched (it was copied, not moved).
	if _, err := os.Stat(f.backup); err != nil {
		t.Errorf("backup %s: %v", f.backup, err)
	}
}

func TestOpenArchiveDamagedDeclined(t *testing.T) {
	f := newDamagedFixture(t)
	sys, out := testSystem(t, "n\n")
	sys.console = true

	_, err := openArchive(f.path, sys, true)
	if err == nil {
		t.Fatal("openArchive: want an error when the restore is declined")
	}
	if !strings.Contains(err.Error(), f.path) {
		t.Errorf("error %q does not name the archive", err)
	}
	if !strings.Contains(out.String(), "[y/N]") {
		t.Errorf("no restore question:\n%s", out)
	}
	f.assertUntouched(t)
}

func TestOpenArchiveDamagedHeadless(t *testing.T) {
	f := newDamagedFixture(t)
	sys, _ := testSystem(t, "")
	sys.stdin = failReader{t}

	_, err := openArchive(f.path, sys, false)
	var dmg *archive.ErrDamaged
	if !errors.As(err, &dmg) {
		t.Fatalf("openArchive headless: err = %v, want ErrDamaged", err)
	}
	for _, want := range []string{f.path, f.backup, "to restore it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	f.assertUntouched(t)
}

func TestOpenArchiveDamagedNoBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, archiveName)
	if err := os.WriteFile(path, bytes.Repeat([]byte("garbage "), 500), 0o644); err != nil {
		t.Fatal(err)
	}
	sys, _ := testSystem(t, "")
	sys.console = true
	sys.stdin = failReader{t} // nothing to offer: no question

	_, err := openArchive(path, sys, true)
	if err == nil || !strings.Contains(err.Error(), "no backup") {
		t.Fatalf("openArchive without backups: err = %v, want one saying there is no backup", err)
	}
}

func TestUniqueDamagedName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, archiveName)
	at := time.Date(2026, 9, 23, 21, 30, 5, 0, time.Local)
	first := damagedName(path, at)
	if filepath.Base(first) != "archive.damaged-20260923-213005.db" {
		t.Fatalf("damagedName = %s", first)
	}
	if err := os.WriteFile(first+"-wal", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if second := damagedName(path, at); second == first {
		t.Fatalf("damagedName reused %s although its -wal exists", first)
	}
}

func TestRestoreBackupMovesSidecars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, archiveName)
	backup := filepath.Join(dir, "backup.db")
	files := map[string]string{
		path:          "damaged main file",
		path + "-wal": "stale WAL that must not be replayed into the backup",
		path + "-shm": "stale shared memory",
		backup:        "backup content",
	}
	for p, c := range files {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kept := damagedName(path, time.Date(2026, 9, 23, 1, 2, 3, 0, time.Local))
	if err := restoreBackup(path, kept, backup); err != nil {
		t.Fatalf("restoreBackup: %v", err)
	}
	want := map[string]string{
		kept:          "damaged main file",
		kept + "-wal": "stale WAL that must not be replayed into the backup",
		kept + "-shm": "stale shared memory",
		path:          "backup content",
		backup:        "backup content",
	}
	for p, c := range want {
		if got, err := os.ReadFile(p); err != nil || string(got) != c {
			t.Errorf("%s = %q (err %v), want %q", p, got, err, c)
		}
	}
	for _, s := range []string{"-wal", "-shm", ".restoring"} {
		if _, err := os.Stat(path + s); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s%s still exists next to the restored archive", path, s)
		}
	}
}

func TestRestoreBackupFailurePutsFilesBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, archiveName)
	for p, c := range map[string]string{path: "damaged", path + "-wal": "wal"} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kept := damagedName(path, time.Now())
	if err := restoreBackup(path, kept, filepath.Join(dir, "missing-backup.db")); err == nil {
		t.Fatal("restoreBackup from a missing backup: want an error")
	}
	for p, c := range map[string]string{path: "damaged", path + "-wal": "wal"} {
		if got, err := os.ReadFile(p); err != nil || string(got) != c {
			t.Errorf("%s = %q (err %v), want %q put back", p, got, err, c)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "archive.damaged-*")); len(m) != 0 {
		t.Errorf("left behind: %q", m)
	}
}

func TestOpenArchiveOffersNextBackup(t *testing.T) {
	f := newDamagedFixture(t)
	newest := filepath.Join(f.dir, "backup", "archive-2026-09-22.db")
	if err := os.WriteFile(newest, bytes.Repeat([]byte("damaged backup "), 300), 0o644); err != nil {
		t.Fatal(err)
	}
	sys, out := testSystem(t, "y\ny\n")
	sys.console = true
	at := time.Date(2026, 9, 23, 21, 30, 5, 0, time.Local)
	sys.now = func() time.Time { return at }

	db, err := openArchive(f.path, sys, true)
	if err != nil {
		t.Fatalf("openArchive: %v\n%s", err, out)
	}
	defer db.Close()
	if v, _ := db.Meta("marker"); v != "from-backup" {
		t.Errorf("meta marker = %q, want the older, valid backup", v)
	}
	for _, want := range []string{"Newest backup: " + newest, "damaged too", "Next backup: " + f.backup} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
	// Both damaged files are kept: the original archive and the bad copy.
	if m, _ := filepath.Glob(filepath.Join(f.dir, "archive.damaged-*.db")); len(m) != 2 {
		t.Errorf("kept damaged files %q, want 2", m)
	}
}
