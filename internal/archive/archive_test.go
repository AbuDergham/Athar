package archive

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

func TestOpenCreatesAndReopens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "athar-data", "archive.db") // parent does not exist yet
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open new: %v", err)
	}
	if got := d.Path(); got != path {
		t.Errorf("Path() = %q, want %q", got, path)
	}
	if v, err := d.Meta("schema_version"); err != nil || v != "1" {
		t.Errorf("schema_version = %q, %v; want 1", v, err)
	}
	mustApply(t, d, src("a.jsonl"), live("s1", "first", "hello", "hi"))
	if err := d.SetMeta("last_sync_at", "2026-09-23T03:10:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d.Close()
	if v, _ := d.Meta("last_sync_at"); v != "2026-09-23T03:10:00Z" {
		t.Errorf("meta lost on reopen: %q", v)
	}
	if v, _ := d.Meta("absent"); v != "" {
		t.Errorf("absent meta = %q", v)
	}
	if rows := mustSearch(t, d, Filter{}); len(rows) != 1 {
		t.Errorf("reopened archive has %d sessions, want 1", len(rows))
	}
	var mode string
	if err := d.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal", mode, err)
	}
}

func TestOpenDamagedNeverRecreates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.db")
	junk := bytes.Repeat([]byte("this is not a database "), 400)
	if err := os.WriteFile(path, junk, 0o644); err != nil {
		t.Fatal(err)
	}
	bdir := filepath.Join(dir, "backup")
	if err := os.MkdirAll(bdir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"archive-2026-09-01.db", "archive-2026-09-15.db", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(bdir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	d, err := Open(path)
	if err == nil {
		d.Close()
		t.Fatal("Open of a junk file succeeded")
	}
	var dmg *ErrDamaged
	if !errors.As(err, &dmg) {
		t.Fatalf("error %T %v is not *ErrDamaged", err, err)
	}
	if dmg.Path != path || dmg.Err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("ErrDamaged = %+v (%v)", dmg, err)
	}
	want := []string{filepath.Join(bdir, "archive-2026-09-15.db"), filepath.Join(bdir, "archive-2026-09-01.db")}
	if strings.Join(dmg.Backups, "|") != strings.Join(want, "|") {
		t.Errorf("Backups = %v, want %v (newest first)", dmg.Backups, want)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, junk) {
		t.Errorf("damaged archive was modified (err %v)", err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "archive.db,backup" {
		t.Errorf("files next to the damaged archive: %v", names)
	}
}

func TestOpenDetectsDamagedTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Meta("last_check_at"); v == "" {
		t.Error("a new archive has no last_check_at")
	}
	var ss []model.Session
	for i := range 200 {
		ss = append(ss, live(fmt.Sprintf("s%03d", i), "title", "some text to fill pages"))
	}
	mustApply(t, d, src("a"), ss...)
	var root, pageSize int64
	if err := d.db.QueryRow(`SELECT rootpage FROM sqlite_schema WHERE name = 'sessions'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := d.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	d.Close() // checkpoints the WAL into the file

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xA5}, 64), (root-1)*pageSize); err != nil {
		t.Fatal(err)
	}
	f.Close()
	before, _ := os.ReadFile(path)

	if d, err := Open(path); err == nil {
		d.Close()
		t.Fatal("Open accepted an archive whose sessions table is damaged")
	} else if dmg := (*ErrDamaged)(nil); !errors.As(err, &dmg) {
		t.Fatalf("error %v is not *ErrDamaged", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("the damaged archive was modified")
	}
}

func TestOpenRunsFullCheckWhenDue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustApply(t, d, src("a"), live("s1", "t", "m"))
	old := time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if err := d.SetMeta("last_check_at", old); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if v, _ := d.Meta("last_check_at"); v == old {
		t.Error("last_check_at not refreshed by a due full check")
	}
}

func TestOpenRefusesForeignAndNewerDatabases(t *testing.T) {
	dir := t.TempDir()

	// A newer Athar wrote this archive.
	newer := filepath.Join(dir, "newer.db")
	d, err := Open(newer)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetMeta("schema_version", "99"); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if d, err := Open(newer); err == nil {
		d.Close()
		t.Error("Open accepted an archive with a newer schema")
	} else if !strings.Contains(err.Error(), "newer") {
		t.Errorf("error %q does not mention the newer schema", err)
	}

	// Some other program's SQLite database.
	foreign := filepath.Join(dir, "foreign.db")
	d, err = Open(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`DROP TABLE meta; CREATE TABLE other(x)`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if d, err := Open(foreign); err == nil {
		d.Close()
		t.Error("Open accepted a database that is not an Athar archive")
	}
}

func TestSourceStateAndErrors(t *testing.T) {
	d := openTemp(t)
	if _, _, ok, err := d.SourceState("M1", "a.jsonl"); err != nil || ok {
		t.Fatalf("SourceState of an unknown source: ok=%v err=%v", ok, err)
	}

	// A failure before any successful read: no state, error shown.
	if err := d.RecordSourceError("M1", "claude", "a.jsonl", errors.New("torn copy")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := d.SourceState("M1", "a.jsonl"); ok {
		t.Error("a source that never read OK has state")
	}
	st := sourceStatus(t, d, "M1", "a.jsonl")
	if st.LastError != "torn copy" || !st.LastOK.IsZero() {
		t.Errorf("status after error = %+v", st)
	}

	rec := src("a.jsonl")
	rec.Skipped, rec.Warnings = 2, []string{"2 lines could not be parsed"}
	mustApply(t, d, rec, live("s1", "t", "a", "b"), live("s2", "t", "c"))
	fp, ver, ok, err := d.SourceState("M1", "a.jsonl")
	if err != nil || !ok || fp != rec.Fingerprint || ver != 1 {
		t.Errorf("SourceState = %q %d %v %v", fp, ver, ok, err)
	}
	st = sourceStatus(t, d, "M1", "a.jsonl")
	if st.LastError != "" || st.LastOK.IsZero() || st.Sessions != 2 || st.Skipped != 2 ||
		len(st.Warnings) != 1 || st.Adapter != "claude" {
		t.Errorf("status after apply = %+v", st)
	}
	if time.Since(st.LastOK) > time.Minute {
		t.Errorf("LastOK = %v", st.LastOK)
	}

	// A later failure keeps the last good fingerprint (the changed file is retried).
	if err := d.RecordSourceError("M1", "claude", "a.jsonl", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if fp2, _, ok, _ := d.SourceState("M1", "a.jsonl"); !ok || fp2 != fp {
		t.Errorf("fingerprint after error = %q ok=%v", fp2, ok)
	}
	if st := sourceStatus(t, d, "M1", "a.jsonl"); st.LastError != "boom" || st.Sessions != 2 {
		t.Errorf("status after second error = %+v", st)
	}

	// Other machines' sources are not listed.
	other := src("b.jsonl")
	other.Machine = "M2"
	mustApply(t, d, other, live("s3", "t", "x"))
	list, err := d.Sources("M1")
	if err != nil || len(list) != 1 {
		t.Errorf("Sources(M1) = %+v, %v", list, err)
	}
}

func sourceStatus(t *testing.T, d *DB, machine, path string) SourceStatus {
	t.Helper()
	list, err := d.Sources(machine)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Path == path {
			return s
		}
	}
	t.Fatalf("source %s not listed in %+v", path, list)
	return SourceStatus{}
}

func TestPanicInWriteReleasesTheLock(t *testing.T) {
	d := openTemp(t)
	func() {
		defer func() { recover() }()
		d.inTx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('half', 'written')`); err != nil {
				t.Fatal(err)
			}
			panic("adapter data broke the apply")
		})
	}()
	done := make(chan error, 1)
	go func() {
		_, err := d.ApplySource(src("a"), []model.Session{live("s1", "t", "m")})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a write after a panicking write is blocked")
	}
	if v, _ := d.Meta("half"); v != "" {
		t.Error("the panicking transaction was committed")
	}
}

func TestAgents(t *testing.T) {
	d := openTemp(t)
	a, b := live("s1", "x", "m"), live("s2", "x", "m")
	c := live("s3", "x", "m")
	c.Agent = "codex"
	mustApply(t, d, src("a"), a, b, c)
	got, err := d.Agents()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (AgentCount{"claude", 2}) || got[1] != (AgentCount{"codex", 1}) {
		t.Errorf("Agents() = %+v", got)
	}
}
