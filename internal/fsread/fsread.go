// Package fsread reads agent files without disturbing the agents:
// files are opened with FILE_SHARE_READ|WRITE|DELETE and closed right away,
// and SQLite databases are only ever read from a private temp copy.
package fsread

import (
	"errors"
	"io"
	"time"
)

var errTODO = errors.New("fsread: not implemented")

// Open opens a file for reading with full sharing (read, write, delete) so the
// owning agent can keep appending to, renaming or deleting it. Callers must Close quickly.
func Open(path string) (io.ReadCloser, error) { return nil, errTODO }

// ReadFile reads a whole file through Open.
func ReadFile(path string) ([]byte, error) { return nil, errTODO }

// Lines calls fn for every complete line of a JSONL file (lines of ANY length;
// never bufio.Scanner's 64 KiB limit). A final line without a trailing newline
// is NOT passed to fn (it may be half-written). fn returning an error stops the read.
func Lines(path string, fn func(line []byte) error) error { return errTODO }

// ZstdLines is Lines for a zstd-compressed JSONL file (streaming decoder).
func ZstdLines(path string, fn func(line []byte) error) error { return errTODO }

// Stat returns size and modification time without opening the file for writing.
func Stat(path string) (size int64, mtime time.Time, err error) { return 0, time.Time{}, errTODO }

// Fingerprint returns a stable string built from (path, size, mtime) of every
// existing file in paths, in the given order. Missing files are skipped.
func Fingerprint(paths []string) (string, error) { return "", errTODO }

// CopySQLite copies db and its -wal and -shm files (those that exist) into a new
// folder under tempDir, verifies the copy with PRAGMA quick_check (retrying the
// copy up to 3 times), and returns the path of the copied db plus a cleanup func.
// The copy is opened by callers with OpenSQLiteCopy. The original is never opened by SQLite.
func CopySQLite(db, tempDir string) (copyPath string, cleanup func(), err error) {
	return "", func() {}, errTODO
}

// SQLiteSidecars returns db plus the -wal / -shm paths that currently exist.
func SQLiteSidecars(db string) []string { return []string{db} }
