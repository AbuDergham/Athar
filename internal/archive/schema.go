package archive

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// schemaVersion is the archive schema this build writes (meta.schema_version).
const schemaVersion = 1

// schemaV1 creates the whole archive (spec §4.2).
//
// Times are INTEGER Unix nanoseconds in UTC (NULL or 0 = unknown).
//
// Full-text search (spec §5) uses two contentless FTS5 tables with the
// trigram tokenizer over search.Normalize'd text, so the original text is
// stored once (in messages.text) and the index holds only trigrams:
//   - fts_msg:   rowid = messages.id (= session_id<<24 | seq), body =
//     normalized message text (tool outputs: their first 2 KB only).
//   - fts_title: rowid = sessions.id, body = normalized "title project_name".
//
// contentless_delete=1 lets rows be deleted by rowid (needed when a title
// changes or a lower-origin copy is replaced, see apply.go). case_sensitive 1
// makes the tokenizer compare exactly what Normalize produced (it already
// folds case), so an FTS match is exactly a substring match of the normalized
// text; the Go side (snippets, hit messages, HitSeqs) relies on that.
// Snippets are cut from the original text with search.Snippet, which is why
// no normalized copy of the text is kept.
const schemaV1 = `
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE sources (
	id              INTEGER PRIMARY KEY,
	machine         TEXT    NOT NULL,
	adapter         TEXT    NOT NULL,
	path            TEXT    NOT NULL,
	fingerprint     TEXT    NOT NULL DEFAULT '', -- of the last successful read
	adapter_version INTEGER NOT NULL DEFAULT 0,
	last_ok_at      INTEGER,
	last_error      TEXT    NOT NULL DEFAULT '',
	last_error_at   INTEGER,
	skipped         INTEGER NOT NULL DEFAULT 0,
	warnings        TEXT    NOT NULL DEFAULT '[]', -- JSON array of strings
	missing_at      INTEGER,                       -- not listed by the last sync
	UNIQUE (machine, path)
);

CREATE TABLE sessions (
	id                INTEGER PRIMARY KEY,
	agent             TEXT    NOT NULL,
	native_id         TEXT    NOT NULL,
	machine           TEXT    NOT NULL DEFAULT '', -- last seen live on
	cwd               TEXT    NOT NULL DEFAULT '',
	project_path      TEXT    NOT NULL DEFAULT '',
	project_name      TEXT    NOT NULL DEFAULT '',
	title             TEXT    NOT NULL DEFAULT '',
	title_source      TEXT    NOT NULL DEFAULT 'none',
	norm_title        TEXT    NOT NULL DEFAULT '', -- the fts_title body, for short terms
	started_at        INTEGER NOT NULL DEFAULT 0,
	updated_at        INTEGER NOT NULL DEFAULT 0,
	message_count     INTEGER NOT NULL DEFAULT 0,
	coverage          TEXT    NOT NULL DEFAULT 'meta',
	origin            TEXT    NOT NULL DEFAULT 'live',
	root              TEXT    NOT NULL DEFAULT '',
	source_id         INTEGER REFERENCES sources(id),
	source_missing_at INTEGER,
	UNIQUE (agent, native_id)
);
CREATE INDEX sessions_updated ON sessions(updated_at);
CREATE INDEX sessions_agent_updated ON sessions(agent, updated_at);

-- id = session_id << 24 | seq (see msgID): a session's messages are one
-- rowid range in seq order, and an fts_msg rowid tells its session and seq
-- without a lookup.
CREATE TABLE messages (
	id         INTEGER PRIMARY KEY,
	session_id INTEGER NOT NULL REFERENCES sessions(id),
	seq        INTEGER NOT NULL, -- 0-based position in the session
	role       TEXT    NOT NULL,
	kind       TEXT    NOT NULL,
	at         INTEGER,
	text       TEXT    NOT NULL,
	text_hash  BLOB    NOT NULL  -- sha256(role|kind|text)[:16]
);

-- Which sources (on which machine) contain which sessions, with the origin
-- of that copy: FinishSync marks a session missing when no source seen in
-- the sync still holds a copy at least as good as the archived one.
CREATE TABLE session_sources (
	session_id INTEGER NOT NULL REFERENCES sessions(id),
	source_id  INTEGER NOT NULL REFERENCES sources(id),
	origin     TEXT    NOT NULL,
	PRIMARY KEY (session_id, source_id)
) WITHOUT ROWID;
CREATE INDEX session_sources_source ON session_sources(source_id);

CREATE VIRTUAL TABLE fts_msg USING fts5(
	body, content='', contentless_delete=1, tokenize='trigram case_sensitive 1'
);
CREATE VIRTUAL TABLE fts_title USING fts5(
	body, content='', contentless_delete=1, tokenize='trigram case_sensitive 1'
);
`

// migrations[v] upgrades schema v to v+1. Open takes a backup before running any.
var migrations = map[int]string{}

// migrate brings the schema to schemaVersion, creating it in a database
// without any table (a new or empty file). It refuses databases that are not
// Athar archives and archives written by a newer Athar.
func (d *DB) migrate() error {
	var tables int
	if err := d.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type = 'table'`).Scan(&tables); err != nil {
		return err
	}
	if tables == 0 {
		return d.inTx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(schemaV1); err != nil {
				return fmt.Errorf("create schema: %w", err)
			}
			_, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('schema_version', ?)`, strconv.Itoa(schemaVersion))
			return err
		})
	}

	var hasMeta int
	if err := d.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'meta'`).Scan(&hasMeta); err != nil {
		return err
	}
	if hasMeta == 0 {
		return fmt.Errorf("%s is not an Athar archive", d.path)
	}
	v, err := d.Meta("schema_version")
	if err != nil {
		return err
	}
	have, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("%s is not an Athar archive (schema_version %q)", d.path, v)
	}
	switch {
	case have == schemaVersion:
		return nil
	case have > schemaVersion:
		return fmt.Errorf("%s was written by a newer Athar (archive schema %d, this build reads %d); update Athar", d.path, have, schemaVersion)
	}
	if _, err := d.Backup(backupDir(d.path), time.Now()); err != nil {
		return fmt.Errorf("backup before migrating the archive: %w", err)
	}
	return d.inTx(func(tx *sql.Tx) error {
		for ; have < schemaVersion; have++ {
			step, ok := migrations[have]
			if !ok {
				return fmt.Errorf("no migration from archive schema %d", have)
			}
			if _, err := tx.Exec(step); err != nil {
				return fmt.Errorf("migrate archive schema %d: %w", have, err)
			}
		}
		_, err := tx.Exec(`UPDATE meta SET value = ? WHERE key = 'schema_version'`, strconv.Itoa(schemaVersion))
		return err
	})
}

// seqBits is the number of low bits of messages.id that hold the seq.
const seqBits = 24

// maxSeq is the largest seq a message can have (a session keeps at most
// maxSeq+1 messages; ApplySource warns about the rest).
const maxSeq = 1<<seqBits - 1

// msgID is the messages.id (and fts_msg rowid) of a session's message.
func msgID(sessionID int64, seq int) int64 { return sessionID<<seqBits | int64(seq) }

// splitMsgID is the inverse of msgID.
func splitMsgID(id int64) (sessionID int64, seq int) { return id >> seqBits, int(id & maxSeq) }

// msgRange returns the first and last possible message id of a session.
func msgRange(sessionID int64) (lo, hi int64) { return msgID(sessionID, 0), msgID(sessionID, maxSeq) }
