package archive

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestBackupKeepsNewestTwo(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s1", "Backed up", "hello", "hi"))
	dir := filepath.Join(filepath.Dir(d.Path()), "backup")

	day := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)
	var made []string
	for i := range 3 {
		p, err := d.Backup(dir, day.AddDate(0, 0, 7*i))
		if err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
		made = append(made, p)
	}
	if filepath.Base(made[2]) != "archive-2026-09-15.db" {
		t.Errorf("backup name %s", made[2])
	}
	// Same day again: replaced, not duplicated.
	if _, err := d.Backup(dir, day.AddDate(0, 0, 14).Add(time.Hour)); err != nil {
		t.Fatalf("same-day backup: %v", err)
	}
	var names []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"archive-2026-09-08.db", "archive-2026-09-15.db"}) {
		t.Errorf("backups kept: %v", names)
	}
	if v, _ := d.Meta("last_backup_at"); v == "" {
		t.Error("last_backup_at not recorded")
	}

	// A backup is a working archive.
	b, err := Open(made[2])
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer b.Close()
	rows, err := b.Search(Filter{Query: "backed"}, 10, 0)
	if err != nil || len(rows) != 1 {
		t.Errorf("backup search = %v, %v", ids(rows), err)
	}
}
