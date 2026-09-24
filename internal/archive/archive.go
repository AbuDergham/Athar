// Package archive is Athar's permanent SQLite store (spec §4, §5.3).
// It never deletes archived sessions or messages on its own.
//
// Layout: see schemaV1 (schema.go). Writes (ApplySource, FinishSync,
// RecordSourceError, SetMeta) are serialized inside the process; searches run
// concurrently with them thanks to WAL mode.
package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AbuDergham/Athar/internal/model"

	"modernc.org/sqlite" // registers the "sqlite" database/sql driver
	sqlite3 "modernc.org/sqlite/lib"
)

// corrupt reports whether an error of the open-time check means the file
// itself is damaged (a quick_check finding, SQLITE_CORRUPT, SQLITE_NOTADB,
// an I/O error) rather than that it could not be opened right now
// (SQLITE_CANTOPEN, SQLITE_BUSY, SQLITE_LOCKED, SQLITE_PERM, SQLITE_AUTH).
func corrupt(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return true // quick_check reported problems
	}
	switch se.Code() & 0xff {
	case sqlite3.SQLITE_CANTOPEN, sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED, sqlite3.SQLITE_PERM, sqlite3.SQLITE_AUTH:
		return false
	}
	return true
}

// DB is an open archive. It is safe for concurrent use.
type DB struct {
	db   *sql.DB
	path string
	home string // %USERPROFILE%, for the "~" project name

	writeMu sync.Mutex // one writer at a time (SQLite allows one anyway)
	proj    *projects

	cacheMu sync.Mutex
	gen     uint64                // bumped by every write that can change search results
	cache   *searchCache          // ranking of the last query, reused for its next pages
	metas   map[int64]sessionMeta // agent and updated_at of every session (nil = not loaded)
}

// ErrDamaged is returned by Open when an existing archive cannot be opened
// or fails its integrity check (spec §2.8, §4.8). The file is left untouched.
type ErrDamaged struct {
	Path    string
	Err     error
	Backups []string // backup files next to it (backup\archive-*.db), newest first
}

// Error names the archive and the SQLite error.
func (e *ErrDamaged) Error() string {
	return fmt.Sprintf("the archive %s is damaged: %v", e.Path, e.Err)
}

// Unwrap returns the SQLite error.
func (e *ErrDamaged) Unwrap() error { return e.Err }

// Open opens (or creates) the archive at path, runs migrations (taking a
// backup first when the schema changes) and PRAGMA quick_check.
// It never creates a new empty archive over an existing file that fails to open.
// The parent folder is created when missing.
//
// The check: every open runs quick_check on each ordinary table (every
// b-tree, FTS shadow tables included; about 0.1 s per 50 MB of archive). The
// complete PRAGMA quick_check, whose FTS5 integrity pass reads and decodes the
// whole index (about 1.4 s per 50 MB), runs when the last one is older than
// fullCheckEvery (meta last_check_at), so startup stays fast as the archive grows.
func Open(path string) (*DB, error) {
	info, err := os.Stat(path)
	exists := err == nil
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return nil, err
	case exists && info.IsDir():
		return nil, fmt.Errorf("archive %s is a folder", path)
	case !exists:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}

	sdb, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(maxConns)
	home, _ := os.UserHomeDir()
	d := &DB{db: sdb, path: path, home: home, proj: newProjects()}
	damaged := func(err error) error {
		sdb.Close()
		if !corrupt(err) { // locked by another program, no permission: not damaged
			return fmt.Errorf("cannot open the archive %s (is another program using it?): %w", path, err)
		}
		return &ErrDamaged{Path: path, Err: err, Backups: listBackups(backupDir(path))}
	}
	if exists && info.Size() > 0 {
		if err := d.check(true); err != nil {
			return nil, damaged(err)
		}
	}
	if _, err := sdb.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("archive %s: %w", path, err)
	}
	if err := d.migrate(); err != nil {
		sdb.Close()
		return nil, err
	}
	last, _ := d.Meta("last_check_at")
	if t, err := time.Parse(time.RFC3339, last); err != nil || time.Since(t) >= fullCheckEvery {
		if exists && info.Size() > 0 {
			if err := d.check(false); err != nil {
				return nil, damaged(err)
			}
		}
		// Best effort: another Athar may be applying a big source right now
		// (SQLITE_BUSY after the busy timeout); the check then runs again next time.
		_ = d.SetMeta("last_check_at", time.Now().UTC().Format(time.RFC3339))
	}
	return d, nil
}

// fullCheckEvery is how often Open runs the complete PRAGMA quick_check.
const fullCheckEvery = 7 * 24 * time.Hour

// maxConns bounds the connection pool (each connection has its own page cache).
const maxConns = 4

// dsn builds the connection string: every connection gets a busy timeout,
// foreign keys, synchronous=NORMAL (safe with WAL) and a 32 MB page cache
// (searches read the full-text index widely; the default is 2 MB);
// transactions start with BEGIN IMMEDIATE so a writer never fails on a lock
// upgrade. Memory-mapped I/O is not used: the archive may live on a
// removable drive, where a mapped read error would crash instead of failing.
func dsn(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p,
		RawQuery: "_busy_timeout=5000&_foreign_keys=1&_synchronous=NORMAL&_txlock=immediate&_pragma=cache_size(-32768)"}
	return u.String()
}

// check runs PRAGMA quick_check: on each ordinary table when perTable, else
// on the whole database. Any answer other than "ok" is an error.
func (d *DB) check(perTable bool) error {
	if !perTable {
		return d.quickCheck(`PRAGMA quick_check`)
	}
	var tables []string
	err := d.each(`SELECT name FROM sqlite_schema WHERE type = 'table' AND sql NOT LIKE 'CREATE VIRTUAL%' ORDER BY name`, nil,
		func(rows *sql.Rows) error {
			var name string
			err := rows.Scan(&name)
			tables = append(tables, name)
			return err
		})
	if err != nil {
		return err
	}
	for _, t := range tables {
		if err := d.quickCheck(`PRAGMA quick_check("` + strings.ReplaceAll(t, `"`, `""`) + `")`); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) quickCheck(pragma string) error {
	var problems []string
	err := d.each(pragma, nil, func(rows *sql.Rows) error {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		if s != "ok" && len(problems) < 5 {
			problems = append(problems, s)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("quick_check: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Close closes the archive.
func (d *DB) Close() error { return d.db.Close() }

// Path returns the archive file path.
func (d *DB) Path() string { return d.path }

// inTx runs fn in one write transaction, serialized with the other writers,
// and invalidates cached search results.
func (d *DB) inTx(fn func(tx *sql.Tx) error) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	tx, err := d.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done { // fn failed or panicked: never leave the write lock held
			tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	done = true
	if err := tx.Commit(); err != nil {
		return err
	}
	d.invalidate()
	return nil
}

// ---- sync side ----

// SourceRecord identifies a source being applied.
type SourceRecord struct {
	Machine        string
	Adapter        string
	Path           string
	Fingerprint    string
	AdapterVersion int
	Skipped        int
	Warnings       []string
}

// SourceState returns what was stored for (machine, path); ok=false if never
// read successfully. A later failed read keeps the last good fingerprint.
func (d *DB) SourceState(machine, path string) (fingerprint string, adapterVersion int, ok bool, err error) {
	var lastOK sql.NullInt64
	var lastErr string
	err = d.db.QueryRow(`SELECT fingerprint, adapter_version, last_ok_at, last_error FROM sources WHERE machine = ? AND path = ?`,
		machine, path).Scan(&fingerprint, &adapterVersion, &lastOK, &lastErr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	// A source whose last read failed is read again even when unchanged
	// since the last good read (a transient failure during --rescan).
	if err != nil || !lastOK.Valid || lastErr != "" {
		return "", 0, false, err
	}
	return fingerprint, adapterVersion, true, nil
}

// RecordSourceError stores a failure for a source (shown on the Sources screen).
// The stored fingerprint is kept, so the source is read again next time.
// A nil err clears the error.
func (d *DB) RecordSourceError(machine, adapter, path string, err error) error {
	msg, at := "", sql.NullInt64{}
	if err != nil {
		msg, at = err.Error(), sql.NullInt64{Int64: time.Now().UnixNano(), Valid: true}
	}
	return d.inTx(func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO sources(machine, adapter, path, last_error, last_error_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(machine, path) DO UPDATE SET adapter = excluded.adapter,
				last_error = excluded.last_error, last_error_at = excluded.last_error_at, missing_at = NULL`,
			machine, adapter, path, msg, at)
		return e
	})
}

// SourcePaths lists the known, present sources of an adapter on a machine.
// A sync whose adapter could not list its sources marks these as seen, so
// FinishSync does not take a listing failure for deleted sessions.
func (d *DB) SourcePaths(machine, adapter string) ([]string, error) {
	rows, err := d.db.Query(`SELECT path FROM sources WHERE machine = ? AND adapter = ? AND missing_at IS NULL ORDER BY path`,
		machine, adapter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- query side (lists) ----

// Messages returns all messages of a session in order (seq ascending).
// Seqs are 0-based and contiguous, so Row.HitSeq indexes this slice.
func (d *DB) Messages(sessionID int64) ([]model.Message, error) {
	lo, hi := msgRange(sessionID)
	rows, err := d.db.Query(`SELECT role, kind, at, text FROM messages WHERE id BETWEEN ? AND ? ORDER BY id`, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Message
	for rows.Next() {
		var m model.Message
		var at sql.NullInt64
		if err := rows.Scan(&m.Role, &m.Kind, &at, &m.Text); err != nil {
			return nil, err
		}
		m.At = fromNanos(at.Int64)
		out = append(out, m)
	}
	return out, rows.Err()
}

// AgentCount is used for the Tab filter and the Sources screen.
type AgentCount struct {
	Agent    string
	Sessions int
}

// Agents lists agents that have sessions, most sessions first.
func (d *DB) Agents() ([]AgentCount, error) {
	rows, err := d.db.Query(`SELECT agent, count(*) AS n FROM sessions GROUP BY agent ORDER BY n DESC, agent`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentCount
	for rows.Next() {
		var a AgentCount
		if err := rows.Scan(&a.Agent, &a.Sessions); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SourceStatus is one line of the Sources screen.
type SourceStatus struct {
	Adapter   string
	Path      string
	Sessions  int
	LastOK    time.Time
	LastError string
	Skipped   int
	Warnings  []string
}

// Sources lists sources on this machine that the last sync still saw
// (sources that disappeared are kept in the archive but not listed).
// Sessions counts the sessions each source currently contains.
func (d *DB) Sources(machine string) ([]SourceStatus, error) {
	rows, err := d.db.Query(`SELECT s.adapter, s.path, s.last_ok_at, s.last_error, s.skipped, s.warnings,
			(SELECT count(*) FROM session_sources l WHERE l.source_id = s.id)
		FROM sources s WHERE s.machine = ? AND s.missing_at IS NULL ORDER BY s.adapter, s.path`, machine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceStatus
	for rows.Next() {
		var s SourceStatus
		var lastOK sql.NullInt64
		var warnings string
		if err := rows.Scan(&s.Adapter, &s.Path, &lastOK, &s.LastError, &s.Skipped, &warnings, &s.Sessions); err != nil {
			return nil, err
		}
		s.LastOK = fromNanos(lastOK.Int64)
		s.Warnings = decodeWarnings(warnings)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---- meta ----

// Meta reads a value from the meta table ("" if absent).
func (d *DB) Meta(key string) (string, error) {
	var v string
	err := d.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta writes a value to the meta table.
func (d *DB) SetMeta(key, value string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.Exec(`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---- helpers ----

// toNanos stores a time as Unix nanoseconds; the zero time is 0.
func toNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// fromNanos is the inverse of toNanos (UTC).
func fromNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// nullNanos is toNanos with NULL for the zero time.
func nullNanos(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixNano(), Valid: true}
}

func encodeWarnings(w []string) string {
	if len(w) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(w)
	return string(b)
}

func decodeWarnings(s string) []string {
	var w []string
	if json.Unmarshal([]byte(s), &w) != nil || len(w) == 0 {
		return nil
	}
	return w
}
