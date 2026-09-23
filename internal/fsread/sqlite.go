package fsread

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

const (
	// copyAttempts is the first copy plus up to 3 retries.
	copyAttempts = 4
	// copyPrefix names the private folders CopySQLite creates under tempDir.
	copyPrefix = "sq-"
	// sqlitePendingByte is SQLite's PENDING_BYTE (1 GiB). The 512 bytes from
	// here (PENDING, RESERVED and the SHARED range) are what SQLite locks with
	// LockFileEx; they sit on the "lock-byte page", which never holds data.
	sqlitePendingByte = 0x40000000
	sqliteLockSize    = 512
	copyChunk         = 1 << 20
)

// CopySQLite copies db and its -wal and -shm files (those that exist) into a new
// folder under tempDir, verifies the copy with PRAGMA quick_check (retrying the
// copy up to 3 times), and returns the path of the copied db plus a cleanup func.
// The copy is opened by callers with OpenSQLiteCopy. The original is never opened by SQLite.
//
// Details:
//   - Files are read through the share-everything Open; a hot -journal is copied
//     too, so an in-flight rollback-mode transaction is rolled back on the copy.
//     A -shm that cannot be read (a writer's lock) is skipped: SQLite rebuilds
//     it from the -wal anyway.
//   - The copy is opened read-write (it is private) so SQLite replays the -wal,
//     then it is converted to a self-contained rollback-journal database: no
//     sidecar files remain and OpenSQLiteCopy can use immutable=1 safely.
//   - A copy whose source db changed while it was being copied is retried; if
//     every attempt changed, the last copy that passed quick_check is returned.
//   - tempDir "" means os.TempDir(); a tempDir inside db's folder is refused.
//
// The returned cleanup is never nil; close any *sql.DB on the copy before calling it.
func CopySQLite(db, tempDir string) (copyPath string, cleanup func(), err error) {
	noop := func() {}
	if err := checkTempDir(db, tempDir); err != nil {
		return "", noop, err
	}
	if _, err := os.Stat(db); err != nil {
		return "", noop, err
	}
	if tempDir != "" {
		if err := os.MkdirAll(tempDir, 0o700); err != nil {
			return "", noop, err
		}
	}

	var lastErr error
	var fallbackDir, fallbackPath string
	for attempt := 0; attempt < copyAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
		}
		dir, path, stable, err := copyAndVerify(db, tempDir)
		if err != nil {
			lastErr = err
			if _, serr := os.Stat(db); errors.Is(serr, fs.ErrNotExist) {
				break // the agent deleted it meanwhile; retrying cannot help
			}
			continue
		}
		if fallbackDir != "" {
			os.RemoveAll(fallbackDir)
		}
		if stable {
			return path, removeAllFunc(dir), nil
		}
		fallbackDir, fallbackPath = dir, path
		lastErr = errors.New("database changed while it was being copied")
	}
	if fallbackDir != "" {
		return fallbackPath, removeAllFunc(fallbackDir), nil
	}
	return "", noop, fmt.Errorf("fsread: copy SQLite %s: %w", db, lastErr)
}

// OpenSQLiteCopy opens a database made by CopySQLite read-only with
// mode=ro&immutable=1 (no locks, no -wal/-shm files). It refuses paths that are
// not inside a CopySQLite folder, so an agent database is never opened in place.
func OpenSQLiteCopy(copyPath string) (*sql.DB, error) {
	if !strings.HasPrefix(filepath.Base(filepath.Dir(copyPath)), copyPrefix) {
		return nil, fmt.Errorf("fsread: %s is not a CopySQLite copy; agent databases are never opened in place", copyPath)
	}
	if _, err := os.Stat(copyPath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURI(copyPath, "mode=ro&immutable=1"))
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func removeAllFunc(dir string) func() {
	return func() { os.RemoveAll(dir) }
}

// copyAndVerify makes one copy attempt in a fresh folder. stable reports that
// the source db's size and mtime did not change while its files were copied.
// On error the folder is removed.
func copyAndVerify(db, tempDir string) (dir, copyPath string, stable bool, err error) {
	folder, err := os.MkdirTemp(tempDir, copyPrefix)
	if err != nil {
		return "", "", false, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(folder)
		}
	}()
	copyPath = filepath.Join(folder, filepath.Base(db))

	before, err := Fingerprint([]string{db})
	if err != nil {
		return "", "", false, err
	}
	// Order matters: the db first, then its journal / WAL, which only grow
	// with content newer than (or needed to repair) the db copy.
	if err = copyDBFile(copyPath, db, sqlitePendingByte); err != nil {
		return "", "", false, err
	}
	for _, suffix := range []string{"-journal", "-wal"} {
		err = copyDBFile(copyPath+suffix, db+suffix, -1)
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
			continue
		}
		if err != nil {
			return "", "", false, err
		}
	}
	// Best effort: SQLite discards and rebuilds a -shm it opens first, and a
	// writer's exclusive lock can make it unreadable on Windows.
	if e := copyDBFile(copyPath+"-shm", db+"-shm", -1); e != nil {
		os.Remove(copyPath + "-shm")
	}
	after, err := Fingerprint([]string{db})
	if err != nil {
		return "", "", false, err
	}
	if err = verifyCopy(copyPath); err != nil {
		return "", "", false, err
	}
	// After a clean close in journal_mode=DELETE, SQLite has removed its -wal
	// and -shm and rolled back any hot journal. What can remain is a copied
	// journal SQLite ignored as not hot (an agent's unsynced, in-flight
	// transaction); drop it so the copy is self-contained.
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if err = os.Remove(copyPath + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", "", false, err
		}
		err = nil
	}
	return folder, copyPath, before == after, nil
}

// verifyCopy opens the private copy read-write (so SQLite replays the -wal or
// rolls back a hot -journal), runs PRAGMA quick_check, and converts the copy to
// journal_mode=DELETE so it no longer needs sidecar files.
func verifyCopy(copyPath string) (err error) {
	db, err := sql.Open("sqlite", sqliteURI(copyPath, "mode=rw"))
	if err != nil {
		return err
	}
	defer func() {
		if cerr := db.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()
	db.SetMaxOpenConns(1)

	rows, err := db.Query("PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("quick_check: %w", err)
	}
	var results []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return fmt.Errorf("quick_check: %w", err)
		}
		if len(results) < 5 {
			results = append(results, s)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("quick_check: %w", err)
	}
	rows.Close()
	if len(results) != 1 || results[0] != "ok" {
		return fmt.Errorf("quick_check failed: %s", strings.Join(results, "; "))
	}

	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		return fmt.Errorf("fold WAL into the copy: %w", err)
	}
	if !strings.EqualFold(mode, "delete") {
		return fmt.Errorf("fold WAL into the copy: journal_mode is still %q", mode)
	}
	return nil
}

// copyDBFile copies src to a new file dst, reading src through the
// share-everything Open. When lockStart >= 0, the sqliteLockSize bytes at
// lockStart are read on their own and zero-filled if another handle holds a
// Windows byte-range lock on them (SQLite's lock bytes never hold data).
func copyDBFile(dst, src string, lockStart int64) (err error) {
	in, err := openFile(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()

	lockEnd := lockStart + sqliteLockSize
	inLock := func(off int64) bool { return lockStart >= 0 && off >= lockStart && off < lockEnd }
	buf := make([]byte, copyChunk)
	var off int64
	for {
		n := int64(len(buf))
		switch {
		case lockStart >= 0 && off < lockStart && off+n > lockStart:
			n = lockStart - off // stop right before the lock bytes
		case inLock(off):
			n = lockEnd - off // read the lock bytes on their own
		}
		m, rerr := in.ReadAt(buf[:n], off)
		if rerr != nil && rerr != io.EOF && inLock(off) && isLockViolation(rerr) {
			info, serr := in.Stat()
			if serr != nil {
				return serr
			}
			end := min(lockEnd, info.Size())
			if end <= off {
				return nil
			}
			zero := buf[:end-off]
			clear(zero)
			if _, werr := out.Write(zero); werr != nil {
				return werr
			}
			off = end
			continue
		}
		if m > 0 {
			if _, werr := out.Write(buf[:m]); werr != nil {
				return werr
			}
			off += int64(m)
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// checkTempDir refuses a temp folder inside the database's own folder: making
// the copy must never create anything in an agent's folders.
func checkTempDir(db, tempDir string) error {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	dbDir, err := filepath.Abs(filepath.Dir(db))
	if err != nil {
		return err
	}
	td, err := filepath.Abs(tempDir)
	if err != nil {
		return err
	}
	if dbDir == filepath.VolumeName(dbDir)+string(filepath.Separator) {
		return nil // a database at a drive root: everything on the drive is "inside"
	}
	rel, err := filepath.Rel(dbDir, td)
	if err != nil {
		return nil // different volumes
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("fsread: temp dir %s is inside the database folder %s", tempDir, dbDir)
	}
	return nil
}

// sqliteURI builds a SQLite URI filename (file:///C:/dir/x.db?query) with the
// path percent-escaped, so spaces, '#', '?', '%' and non-ASCII names work.
func sqliteURI(path, query string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = strings.TrimPrefix(path, `\\?\`)
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: query}
	return u.String()
}
