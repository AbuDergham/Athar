package fsread

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type fileInfo struct {
	size  int64
	mtime time.Time
}

// snapshot records name, size and mtime of every entry in dir (recursively).
// It uses os.Stat, not the directory listing: on Windows the listing's size and
// mtime of a file held open by a writer are stale until some handle on it is
// closed, so a read-only open+close can make the listing "change".
func snapshot(t *testing.T, dir string) map[string]fileInfo {
	t.Helper()
	out := map[string]fileInfo{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = fileInfo{info.Size(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSnapshot(t *testing.T, before, after map[string]fileInfo) {
	t.Helper()
	for name, b := range before {
		a, ok := after[name]
		if !ok {
			t.Errorf("source file %s disappeared", name)
			continue
		}
		if a.size != b.size || !a.mtime.Equal(b.mtime) {
			t.Errorf("source file %s changed: %+v -> %+v", name, b, a)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("new file %s appeared in the source folder", name)
		}
	}
}

// fixtureDB creates a database in a new folder and keeps it open (like an
// agent would). journal is "wal" or "delete".
func fixtureDB(t *testing.T, journal string, rows int) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent", "state.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	stmts := []string{
		"PRAGMA journal_mode=" + journal,
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE msgs(id INTEGER PRIMARY KEY, body TEXT)",
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		if _, err := tx.Exec("INSERT INTO msgs(body) VALUES (?)", strings.Repeat("مرحبا hello ", 20)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func countRows(t *testing.T, copyPath string) int {
	t.Helper()
	db, err := OpenSQLiteCopy(copyPath)
	if err != nil {
		t.Fatalf("OpenSQLiteCopy: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM msgs").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func assertSelfContained(t *testing.T, copyPath string) {
	t.Helper()
	for _, s := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(copyPath + s); err == nil {
			t.Errorf("copy left a %s file next to it", s)
		}
	}
}

func TestCopySQLiteWALWithUncheckpointedRows(t *testing.T) {
	src, _ := fixtureDB(t, "wal", 500)
	if info, err := os.Stat(src + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("fixture has no un-checkpointed WAL (err=%v)", err)
	}
	tempDir := t.TempDir()
	before := snapshot(t, filepath.Dir(src))

	copyPath, cleanup, err := CopySQLite(src, tempDir)
	if err != nil {
		t.Fatalf("CopySQLite: %v", err)
	}
	if !strings.HasPrefix(copyPath, tempDir+string(filepath.Separator)) {
		t.Errorf("copy %s is not under tempDir %s", copyPath, tempDir)
	}
	assertSelfContained(t, copyPath)
	if n := countRows(t, copyPath); n != 500 {
		t.Errorf("copy has %d rows, want 500 (WAL not replayed?)", n)
	}
	assertSelfContained(t, copyPath) // OpenSQLiteCopy must not create sidecars either
	sameSnapshot(t, before, snapshot(t, filepath.Dir(src)))

	cleanup()
	if _, err := os.Stat(filepath.Dir(copyPath)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cleanup left the copy folder behind (err=%v)", err)
	}
	cleanup() // idempotent
}

// A writer holding the WAL write lock (an exclusive byte-range lock in -shm on
// Windows) must not make the copy fail; uncommitted rows are not visible.
func TestCopySQLiteWhileAgentWrites(t *testing.T) {
	for _, journal := range []string{"wal", "delete"} {
		t.Run(journal, func(t *testing.T) {
			src, _ := fixtureDB(t, journal, 200)
			writer, err := sql.Open("sqlite", src)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			ctx := context.Background()
			conn, err := writer.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(ctx, "INSERT INTO msgs(body) VALUES ('uncommitted')"); err != nil {
				t.Fatal(err)
			}
			defer conn.ExecContext(ctx, "ROLLBACK")

			before := snapshot(t, filepath.Dir(src))
			copyPath, cleanup, err := CopySQLite(src, t.TempDir())
			if err != nil {
				t.Fatalf("CopySQLite during a write transaction: %v", err)
			}
			defer cleanup()
			sameSnapshot(t, before, snapshot(t, filepath.Dir(src)))
			if n := countRows(t, copyPath); n != 200 {
				t.Errorf("copy has %d rows, want the 200 committed ones", n)
			}
			assertSelfContained(t, copyPath)
		})
	}
}

func TestCopySQLiteClosedRollbackDB(t *testing.T) {
	src, db := fixtureDB(t, "delete", 50)
	db.Close()
	before := snapshot(t, filepath.Dir(src))
	copyPath, cleanup, err := CopySQLite(src, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if n := countRows(t, copyPath); n != 50 {
		t.Errorf("copy has %d rows, want 50", n)
	}
	sameSnapshot(t, before, snapshot(t, filepath.Dir(src)))
}

// Temp folders with spaces, URI-special characters and Arabic must work.
func TestCopySQLiteSpecialTempPath(t *testing.T) {
	src, _ := fixtureDB(t, "wal", 10)
	tempDir := filepath.Join(t.TempDir(), "dir with space #1 %20 &x مجلد")
	copyPath, cleanup, err := CopySQLite(src, tempDir)
	if err != nil {
		t.Fatalf("CopySQLite: %v", err)
	}
	defer cleanup()
	if n := countRows(t, copyPath); n != 10 {
		t.Errorf("copy has %d rows, want 10", n)
	}
}

func TestCopySQLiteFailures(t *testing.T) {
	t.Run("missing db", func(t *testing.T) {
		tempDir := t.TempDir()
		_, cleanup, err := CopySQLite(filepath.Join(t.TempDir(), "missing.db"), tempDir)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v, want fs.ErrNotExist", err)
		}
		cleanup()
		assertEmptyDir(t, tempDir)
	})
	t.Run("not a database", func(t *testing.T) {
		src := writeTemp(t, "garbage.db", []byte(strings.Repeat("not sqlite ", 1000)))
		tempDir := t.TempDir()
		before := snapshot(t, filepath.Dir(src))
		_, cleanup, err := CopySQLite(src, tempDir)
		if err == nil {
			t.Fatal("want an error for a non-database file")
		}
		cleanup()
		assertEmptyDir(t, tempDir)
		sameSnapshot(t, before, snapshot(t, filepath.Dir(src)))
	})
	t.Run("temp dir inside the agent folder is refused", func(t *testing.T) {
		src, _ := fixtureDB(t, "wal", 1)
		before := snapshot(t, filepath.Dir(src))
		_, cleanup, err := CopySQLite(src, filepath.Join(filepath.Dir(src), "tmp"))
		if err == nil {
			t.Error("want an error when tempDir is inside the database folder")
		}
		cleanup()
		sameSnapshot(t, before, snapshot(t, filepath.Dir(src)))
	})
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("leftover in temp dir: %s", e.Name())
	}
}

func TestSQLiteURI(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Temp\sq-1\state.db`, "file:///C:/Temp/sq-1/state.db"},
		{`C:\a b\#?%\م.db`, "file:///C:/a%20b/%23%3F%25/%D9%85.db"},
	}
	if filepath.Separator != '\\' {
		cases = []struct{ in, want string }{
			{"/tmp/sq-1/state.db", "file:///tmp/sq-1/state.db"},
			{"/tmp/a b/#?%/م.db", "file:///tmp/a%20b/%23%3F%25/%D9%85.db"},
		}
	}
	for _, c := range cases {
		if got := sqliteURI(c.in, "mode=ro"); got != c.want+"?mode=ro" {
			t.Errorf("sqliteURI(%q) = %q, want %q", c.in, got, c.want+"?mode=ro")
		}
	}
}

// An agent committing continuously while copies are taken: every copy must be
// usable and show a committed state (between the rows present before the first
// copy and those present after the last one).
func TestCopySQLiteUnderConcurrentWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}
	for _, journal := range []string{"wal", "delete"} {
		t.Run(journal, func(t *testing.T) {
			src, _ := fixtureDB(t, journal, 100)
			writer, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			writer.SetMaxOpenConns(1)
			if journal == "wal" {
				// checkpoint often so the main file changes during copies too
				if _, err := writer.Exec("PRAGMA wal_autocheckpoint=20"); err != nil {
					t.Fatal(err)
				}
			}

			stop := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				for {
					select {
					case <-stop:
						done <- nil
						return
					default:
					}
					if _, err := writer.Exec("INSERT INTO msgs(body) VALUES (?)", strings.Repeat("x", 3000)); err != nil {
						done <- err
						return
					}
				}
			}()

			last := 100
			for i := 0; i < 5; i++ {
				copyPath, cleanup, err := CopySQLite(src, t.TempDir())
				if err != nil {
					close(stop)
					<-done
					t.Fatalf("copy %d: %v", i, err)
				}
				n := countRows(t, copyPath)
				cleanup()
				if n < last {
					t.Errorf("copy %d has %d rows, fewer than an earlier copy (%d)", i, n, last)
				}
				last = n
			}
			close(stop)
			if err := <-done; err != nil {
				t.Fatalf("writer: %v", err)
			}
			var final int
			if err := writer.QueryRow("SELECT count(*) FROM msgs").Scan(&final); err != nil {
				t.Fatal(err)
			}
			if last > final {
				t.Errorf("a copy saw %d rows, more than ever committed (%d)", last, final)
			}
			t.Logf("%s: last copy %d rows, final %d rows", journal, last, final)
		})
	}
}
