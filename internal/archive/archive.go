// Package archive is Athar's permanent SQLite store (spec §4, §5.3).
// It never deletes archived sessions or messages on its own.
package archive

import (
	"errors"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

var errTODO = errors.New("archive: not implemented")

// DB is an open archive.
type DB struct{}

// Open opens (or creates) the archive at path, runs migrations (taking a
// backup first when the schema changes) and PRAGMA quick_check.
// It never creates a new empty archive over an existing file that fails to open.
func Open(path string) (*DB, error) { return nil, errTODO }

// Close closes the archive.
func (d *DB) Close() error { return nil }

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

// SourceState returns what was stored for (machine, path); ok=false if never seen.
func (d *DB) SourceState(machine, path string) (fingerprint string, adapterVersion int, ok bool, err error) {
	return "", 0, false, errTODO
}

// ApplyReport summarizes one ApplySource call.
type ApplyReport struct {
	Inserted, Updated int
	Shrunk            int // sessions where the source had fewer messages; archived copy kept (§4.4)
}

// ApplySource stores the sessions read from one source in ONE transaction,
// following the never-delete and dedup rules of spec §4.4–§4.5:
//   - one session per (agent, native_id); live beats imports; fuller coverage fills gaps
//   - Cwd, once stored non-empty, is never overwritten by a later read of the same source
//   - messages: if the new list has the stored list as prefix (by role+text hash) it replaces it;
//     otherwise stored messages are kept and unseen new ones are appended (Shrunk++)
//   - the session is (re)attached to this source and source_missing_at is cleared
//   - ProjectPath/ProjectName are computed once, at first insert (spec §3.4)
func (d *DB) ApplySource(rec SourceRecord, sessions []model.Session) (ApplyReport, error) {
	return ApplyReport{}, errTODO
}

// RecordSourceError stores a failure for a source (shown on the Sources screen).
func (d *DB) RecordSourceError(machine, adapter, path string, err error) error { return errTODO }

// FinishSync marks sessions missing whose sources on this machine were not seen
// in the sync that just ended (seenPaths), and are not contained in any other live source.
// It returns the number of sessions newly marked missing.
func (d *DB) FinishSync(machine string, seenPaths map[string]bool, at time.Time) (int, error) {
	return 0, errTODO
}

// ---- query side ----

// Filter narrows the list. Empty Query = timeline (newest first).
type Filter struct {
	Query string
	Agent string    // "" = all
	Since time.Time // zero = any time
}

// Row is one line of the result list.
type Row struct {
	SessionID    int64
	Agent        string
	NativeID     string
	Title        string
	ProjectName  string
	ProjectPath  string
	Cwd          string
	Root         string
	Machine      string
	Coverage     model.Coverage
	Origin       model.Origin
	StartedAt    time.Time
	UpdatedAt    time.Time
	MessageCount int
	Missing      bool // source deleted by the agent (archive-only)

	Snippet      string // around the best hit, from ORIGINAL text; "" for timeline
	HitSeq       int    // seq of the best-matching message, -1 if none
	MatchedTerms int    // how many query terms this session matches
	TotalTerms   int
}

// Search returns rows for the filter (spec §5.3: per-session AND, partial matches after full ones).
func (d *DB) Search(f Filter, limit, offset int) ([]Row, error) { return nil, errTODO }

// Messages returns all messages of a session in order (seq ascending).
func (d *DB) Messages(sessionID int64) ([]model.Message, error) { return nil, errTODO }

// HitSeqs returns the seqs of messages in the session that match any query term (for the reading page).
func (d *DB) HitSeqs(sessionID int64, query string) ([]int, error) { return nil, errTODO }

// AgentCount is used for the Tab filter and the Sources screen.
type AgentCount struct {
	Agent    string
	Sessions int
}

// Agents lists agents that have sessions, most sessions first.
func (d *DB) Agents() ([]AgentCount, error) { return nil, errTODO }

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

// Sources lists sources on this machine.
func (d *DB) Sources(machine string) ([]SourceStatus, error) { return nil, errTODO }

// Meta reads a value from the meta table ("" if absent).
func (d *DB) Meta(key string) (string, error) { return "", errTODO }

// SetMeta writes a value to the meta table.
func (d *DB) SetMeta(key, value string) error { return errTODO }

// Backup writes dir/archive-YYYY-MM-DD.db with VACUUM INTO and keeps the newest 2.
func (d *DB) Backup(dir string, now time.Time) (string, error) { return "", errTODO }

// Path returns the archive file path.
func (d *DB) Path() string { return "" }
