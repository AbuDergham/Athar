package opencode

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// ReadDB reads every top-level session of an OpenCode-family SQLite database
// (OpenCode's opencode*.db, Kilo 7's kilo.db) and returns them with the given
// agent id, plus the number of rows skipped because their JSON did not parse.
//
// copyPath must be a private copy made by fsread.CopySQLite; it is opened with
// fsread.OpenSQLiteCopy, which refuses any other path, so an agent's database
// is never opened in place. The caller owns the copy and its cleanup.
//
// Child (subagent) sessions and sessions without any readable message are
// left out. An error means the whole database could not be read, e.g. an
// unsupported schema (OpenCode 2.0 preview's session_v2 tables).
func ReadDB(copyPath, agent string) ([]model.Session, int, error) {
	return readCopy(context.Background(), copyPath, agent)
}

// readDBFile copies the agent's database at path into tempDir with
// fsread.CopySQLite, reads the copy and removes it.
func readDBFile(ctx context.Context, path, agent, tempDir string) ([]model.Session, int, error) {
	cp, cleanup, err := fsread.CopySQLite(path, tempDir)
	defer cleanup()
	if err != nil {
		return nil, 0, err
	}
	sessions, skipped, err := readCopy(ctx, cp, agent)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	return sessions, skipped, nil
}

// readCopy opens a CopySQLite copy read-only and reads it.
func readCopy(ctx context.Context, copyPath, agent string) ([]model.Session, int, error) {
	db, err := fsread.OpenSQLiteCopy(copyPath)
	if err != nil {
		return nil, 0, err
	}
	defer db.Close() // before the caller's cleanup removes the copy
	return readDB(ctx, db, agent)
}

// schema is what readDB found out about a database's tables.
type schema struct {
	sessionParent bool // session.parent_id exists
	messageTime   bool // message.time_created exists
	partTime      bool // part.time_created exists
}

// detectSchema checks that the session/message/part tables exist with the
// columns Athar reads. OpenCode's schema changes often, so nothing is assumed.
func detectSchema(ctx context.Context, db *sql.DB) (schema, error) {
	tables := map[string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return schema{}, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return schema{}, err
		}
		tables[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return schema{}, err
	}
	var missing []string
	for _, t := range []string{"session", "message", "part"} {
		if !tables[t] {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		if tables["session_v2"] {
			return schema{}, fmt.Errorf("unsupported OpenCode database schema (session_v2 without the session/message/part tables)")
		}
		return schema{}, fmt.Errorf("not an OpenCode database: missing table(s) %s", strings.Join(missing, ", "))
	}

	need := map[string][]string{
		"session": {"id", "directory", "title", "time_created", "time_updated"},
		"message": {"id", "session_id", "data"},
		"part":    {"id", "message_id", "data"},
	}
	var sc schema
	for _, t := range []string{"session", "message", "part"} {
		cols, err := columns(ctx, db, t)
		if err != nil {
			return schema{}, err
		}
		for _, c := range need[t] {
			if !cols[c] {
				return schema{}, fmt.Errorf("unsupported OpenCode database schema: %s.%s is missing", t, c)
			}
		}
		switch t {
		case "session":
			sc.sessionParent = cols["parent_id"]
		case "message":
			sc.messageTime = cols["time_created"]
		case "part":
			sc.partTime = cols["time_created"]
		}
	}
	return sc, nil
}

func columns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// readDB reads sessions, then messages, then parts, each in one ordered pass.
func readDB(ctx context.Context, db *sql.DB, agent string) ([]model.Session, int, error) {
	sc, err := detectSchema(ctx, db)
	if err != nil {
		return nil, 0, err
	}
	pick := func(ok bool, col string) string {
		if ok {
			return col
		}
		return "NULL"
	}

	byID := map[string]*session{}
	var order []*session
	err = each(ctx, db, `SELECT id, `+pick(sc.sessionParent, "parent_id")+`, directory, title,
		CAST(time_created AS INTEGER), CAST(time_updated AS INTEGER)
		FROM session ORDER BY time_created, id`, func(rows *sql.Rows) error {
		var id, parent, dir, title sql.NullString
		var created, updated sql.NullInt64
		if err := rows.Scan(&id, &parent, &dir, &title, &created, &updated); err != nil {
			return err
		}
		if id.String == "" || parent.String != "" {
			return nil // a subagent's session: its messages are ignored below
		}
		s := &session{id: id.String, directory: dir.String, title: title.String, created: created.Int64, updated: updated.Int64}
		byID[id.String] = s
		order = append(order, s)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	skipped := 0
	msgs := map[string]*message{}
	err = each(ctx, db, `SELECT id, session_id, `+pick(sc.messageTime, "CAST(time_created AS INTEGER)")+`, data
		FROM message ORDER BY session_id, `+pick(sc.messageTime, "time_created")+`, id`, func(rows *sql.Rows) error {
		var id, sessionID sql.NullString
		var created sql.NullInt64
		var data []byte
		if err := rows.Scan(&id, &sessionID, &created, &data); err != nil {
			return err
		}
		s := byID[sessionID.String]
		if s == nil {
			return nil
		}
		m, err := parseMessage(data, created.Int64)
		if err != nil {
			skipped++
			return nil
		}
		s.messages = append(s.messages, m)
		msgs[id.String] = m
		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	err = each(ctx, db, `SELECT message_id, `+pick(sc.partTime, "CAST(time_created AS INTEGER)")+`, data
		FROM part ORDER BY message_id, id`, func(rows *sql.Rows) error {
		var messageID sql.NullString
		var created sql.NullInt64
		var data []byte
		if err := rows.Scan(&messageID, &created, &data); err != nil {
			return err
		}
		m := msgs[messageID.String]
		if m == nil {
			return nil // a part of a child session or of an unreadable message
		}
		if err := m.addPart(data, created.Int64); err != nil {
			skipped++
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	var out []model.Session
	for _, s := range order {
		if ms, ok := build(agent, s); ok {
			out = append(out, ms)
		}
	}
	return out, skipped, nil
}

// each runs query and calls fn for every row, stopping early when ctx is done.
func each(ctx context.Context, db *sql.DB, query string, fn func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
