package hermes

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// sessionRow is one row of the sessions table (absent columns read as zero).
type sessionRow struct {
	id, source, parent, modelConfig, endReason, sessionKey string
	cwd, title, titleSource, displayName                   string
	titleSourceNull                                        bool
	startedAt, endedAt, lastActivityAt                     float64
}

// messageRow is one row of the messages table (absent columns read as zero,
// except active, which reads as 1).
type messageRow struct {
	id                         int64
	sessionID, role, content   string
	toolCallID, toolCalls      string
	toolName, displayKind      string
	timestamp                  float64
	active, compacted, summary bool
}

// Read copies one state database, reads the copy and removes it.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	cp, cleanup, err := fsread.CopySQLite(src.Path, env.TempDir)
	defer cleanup()
	if err != nil {
		return adapters.ReadResult{}, err
	}
	db, err := fsread.OpenSQLiteCopy(cp)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	defer db.Close() // before cleanup removes the copy
	sessions, messages, err := load(ctx, db)
	if err != nil {
		return adapters.ReadResult{}, fmt.Errorf("%s: %w", src.Path, err)
	}
	return build(sessions, messages, src.Root), nil
}

// columns returns the column names of table (empty when it does not exist).
func columns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// selectList renders the wanted columns, with NULL for the missing ones.
func selectList(have map[string]bool, want []string) string {
	parts := make([]string, len(want))
	for i, c := range want {
		if have[c] {
			parts[i] = `"` + c + `"`
		} else {
			parts[i] = "NULL"
		}
	}
	return strings.Join(parts, ", ")
}

// requireColumns fails when table lacks any of the named columns.
func requireColumns(table string, have map[string]bool, names ...string) error {
	var missing []string
	for _, n := range names {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("not a Hermes state database: table %s lacks %s", table, strings.Join(missing, ", "))
	}
	return nil
}

var sessionCols = []string{"id", "source", "parent_session_id", "model_config", "end_reason",
	"session_key", "cwd", "title", "title_source", "display_name", "started_at", "ended_at", "last_activity_at"}

var messageCols = []string{"id", "session_id", "role", "content", "timestamp", "tool_call_id",
	"tool_calls", "tool_name", "active", "compacted", "_compressed_summary", "display_kind"}

// load reads every session and message row.
func load(ctx context.Context, db *sql.DB) ([]sessionRow, []messageRow, error) {
	sc, err := columns(ctx, db, "sessions")
	if err != nil {
		return nil, nil, err
	}
	if err := requireColumns("sessions", sc, "id"); err != nil {
		return nil, nil, err
	}
	mc, err := columns(ctx, db, "messages")
	if err != nil {
		return nil, nil, err
	}
	if err := requireColumns("messages", mc, "id", "session_id", "role", "content"); err != nil {
		return nil, nil, err
	}

	rows, err := db.QueryContext(ctx, `SELECT `+selectList(sc, sessionCols)+` FROM sessions`)
	if err != nil {
		return nil, nil, err
	}
	var sessions []sessionRow
	for rows.Next() {
		v := make([]any, len(sessionCols))
		ptrs := make([]any, len(v))
		for i := range v {
			ptrs[i] = &v[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return nil, nil, err
		}
		sessions = append(sessions, sessionRow{
			id: str(v[0]), source: str(v[1]), parent: str(v[2]), modelConfig: str(v[3]),
			endReason: str(v[4]), sessionKey: str(v[5]), cwd: str(v[6]), title: str(v[7]),
			titleSource: str(v[8]), titleSourceNull: v[8] == nil, displayName: str(v[9]),
			startedAt: num(v[10]), endedAt: num(v[11]), lastActivityAt: num(v[12]),
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	rows, err = db.QueryContext(ctx, `SELECT `+selectList(mc, messageCols)+` FROM messages ORDER BY "id"`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var messages []messageRow
	for rows.Next() {
		v := make([]any, len(messageCols))
		ptrs := make([]any, len(v))
		for i := range v {
			ptrs[i] = &v[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		messages = append(messages, messageRow{
			id: int64(num(v[0])), sessionID: str(v[1]), role: str(v[2]), content: str(v[3]),
			timestamp: num(v[4]), toolCallID: str(v[5]), toolCalls: str(v[6]), toolName: str(v[7]),
			active: v[8] == nil || num(v[8]) != 0, compacted: num(v[9]) != 0, summary: num(v[10]) != 0,
			displayKind: str(v[11]),
		})
	}
	return sessions, messages, rows.Err()
}

// str converts a scanned SQLite value to a string ("" for NULL).
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

// num converts a scanned SQLite value to a number (0 for NULL or text that is
// not a number).
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case string, []byte:
		var f float64
		if _, err := fmt.Sscan(str(x), &f); err == nil {
			return f
		}
	}
	return 0
}
