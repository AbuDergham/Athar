package zcode

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// ctxCheckEvery is how many rows are scanned between context checks.
const ctxCheckEvery = 512

// sessionRow is one row of the session table.
type sessionRow struct {
	id, projectID, parentID, directory, title string
	taskType, titleSource                     string
	created, updated                          int64 // Unix ms
}

// messageRow is one row of the message table with its parts, in order.
type messageRow struct {
	id      string
	created int64
	data    []byte
	parts   []partRow
}

type partRow struct {
	created int64
	data    []byte
}

// promptRow is one row of input_history.
type promptRow struct {
	sessionID, projectID, text, kind string
	created                          int64
}

// readStore copies the database at path into tempDir, reads every top-level
// session and returns them. An error means the whole database is unreadable
// (copy failed, or the tables Athar needs are missing).
func readStore(ctx context.Context, path, tempDir string) (adapters.ReadResult, error) {
	cp, cleanup, err := fsread.CopySQLite(path, tempDir)
	defer cleanup()
	if err != nil {
		return adapters.ReadResult{}, err
	}
	db, err := fsread.OpenSQLiteCopy(cp)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	defer db.Close() // runs before cleanup
	res, err := readDB(ctx, db)
	if err != nil {
		return adapters.ReadResult{}, fmt.Errorf("%s: %w", path, err)
	}
	return res, nil
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

// colOr returns col when the table has it, else the SQL literal def.
func colOr(cols map[string]bool, col, def string) string {
	if cols[col] {
		return "coalesce(" + col + ", " + def + ")"
	}
	return def
}

// requireColumns fails when table lacks any of the named columns.
func requireColumns(table string, cols map[string]bool, names ...string) error {
	var missing []string
	for _, n := range names {
		if !cols[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("unsupported ZCode schema: table %s lacks %s", table, strings.Join(missing, ", "))
	}
	return nil
}

func readDB(ctx context.Context, db *sql.DB) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	sCols, err := columns(ctx, db, "session")
	if err != nil {
		return res, err
	}
	mCols, err := columns(ctx, db, "message")
	if err != nil {
		return res, err
	}
	pCols, err := columns(ctx, db, "part")
	if err != nil {
		return res, err
	}
	hCols, err := columns(ctx, db, "input_history")
	if err != nil {
		return res, err
	}
	if err := requireColumns("session", sCols, "id", "directory", "title", "time_created", "time_updated"); err != nil {
		return res, err
	}
	if err := requireColumns("message", mCols, "id", "session_id", "data"); err != nil {
		return res, err
	}
	if err := requireColumns("part", pCols, "id", "message_id", "data"); err != nil {
		return res, err
	}

	sessions, err := readSessions(ctx, db, sCols)
	if err != nil {
		return res, err
	}
	msgs, err := readMessages(ctx, db, mCols, pCols)
	if err != nil {
		return res, err
	}
	var prompts []promptRow
	if hCols["session_id"] && hCols["text"] && hCols["time_created"] {
		if prompts, err = readPrompts(ctx, db, hCols); err != nil {
			return res, err
		}
	}

	// Prompts grouped by session; rows without a session cannot be attributed.
	promptsBy := map[string][]promptRow{}
	var promptOrder []string
	for _, p := range prompts {
		if p.sessionID == "" {
			res.Skipped++
			continue
		}
		if p.kind != "" && p.kind != "prompt" {
			continue // other input kinds (none seen yet) are not prompts
		}
		if _, seen := promptsBy[p.sessionID]; !seen {
			promptOrder = append(promptOrder, p.sessionID)
		}
		promptsBy[p.sessionID] = append(promptsBy[p.sessionID], p)
	}

	known := map[string]bool{}
	projectDir := map[string]string{} // project_id -> directory of its earliest top-level session
	for _, s := range sessions {
		known[s.id] = true
		if s.projectID != "" && !isChild(s) && projectDir[s.projectID] == "" {
			projectDir[s.projectID] = s.directory
		}
	}

	for i, s := range sessions {
		if i%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return res, err
			}
		}
		if isChild(s) {
			continue
		}
		out, skipped := convertSession(s, msgs[s.id])
		res.Skipped += skipped
		if len(out.Messages) == 0 {
			// No readable message rows: fall back to the typed prompts.
			out.Messages = promptMessages(promptsBy[s.id])
			if len(out.Messages) == 0 {
				continue
			}
			out.Coverage = model.CoveragePrompts
		}
		finishSession(&out)
		res.Sessions = append(res.Sessions, out)
	}

	// Prompts of sessions whose row is gone: prompt-log sessions.
	for _, id := range promptOrder {
		if known[id] {
			continue
		}
		ps := promptsBy[id]
		msgs := promptMessages(ps)
		if len(msgs) == 0 {
			continue
		}
		out := model.Session{
			Agent:       ID,
			NativeID:    id,
			Cwd:         cleanDir(projectDir[ps[0].projectID]),
			TitleSource: model.TitleNone,
			Coverage:    model.CoveragePrompts,
			Origin:      model.OriginPromptLog,
			Messages:    msgs,
		}
		finishSession(&out)
		res.Sessions = append(res.Sessions, out)
	}
	return res, nil
}

// childTaskTypes are the task types of sessions ZCode runs on behalf of another one.
var childTaskTypes = map[string]bool{
	"workflow_child":        true,
	"subagent_child":        true,
	"nested_workflow_child": true,
}

// isChild reports whether s is a subagent/workflow session, which Athar skips
// (spec: the parent keeps the tool call and its result). A fork has a
// parent_id but is the user's own conversation, so it is kept. Rows written
// before task_type existed default to "interactive", so any other session
// with a parent_id counts as a child.
func isChild(s sessionRow) bool {
	switch {
	case childTaskTypes[s.taskType]:
		return true
	case s.taskType == "fork":
		return false
	default:
		return s.parentID != ""
	}
}

func readSessions(ctx context.Context, db *sql.DB, cols map[string]bool) ([]sessionRow, error) {
	q := `SELECT id, ` + colOr(cols, "project_id", "''") + `, ` + colOr(cols, "parent_id", "''") +
		`, coalesce(directory, ''), coalesce(title, ''), ` + colOr(cols, "task_type", "''") + `, ` +
		colOr(cols, "title_source", "''") + `, coalesce(time_created, 0), coalesce(time_updated, 0)
		FROM session ORDER BY time_created, id`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionRow
	for rows.Next() {
		var s sessionRow
		if err := rows.Scan(&s.id, &s.projectID, &s.parentID, &s.directory, &s.title, &s.taskType,
			&s.titleSource, &s.created, &s.updated); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// readMessages returns every message with its parts, grouped by session and
// ordered by time (then id); parts are ordered by time (then id).
func readMessages(ctx context.Context, db *sql.DB, mCols, pCols map[string]bool) (map[string][]*messageRow, error) {
	mTime := colOr(mCols, "time_created", "0")
	rows, err := db.QueryContext(ctx, `SELECT id, session_id, `+mTime+`, coalesce(data, '') FROM message ORDER BY session_id, `+mTime+`, id`)
	if err != nil {
		return nil, err
	}
	bySession := map[string][]*messageRow{}
	byID := map[string]*messageRow{}
	n := 0
	for rows.Next() {
		if n++; n%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				rows.Close()
				return nil, err
			}
		}
		var sid string
		m := &messageRow{}
		if err := rows.Scan(&m.id, &sid, &m.created, &m.data); err != nil {
			rows.Close()
			return nil, err
		}
		bySession[sid] = append(bySession[sid], m)
		byID[m.id] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	pTime := colOr(pCols, "time_created", "0")
	prow, err := db.QueryContext(ctx, `SELECT message_id, `+pTime+`, coalesce(data, '') FROM part ORDER BY message_id, `+pTime+`, id`)
	if err != nil {
		return nil, err
	}
	defer prow.Close()
	for prow.Next() {
		if n++; n%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		var mid string
		var p partRow
		if err := prow.Scan(&mid, &p.created, &p.data); err != nil {
			return nil, err
		}
		if m := byID[mid]; m != nil {
			m.parts = append(m.parts, p)
		}
	}
	return bySession, prow.Err()
}

func readPrompts(ctx context.Context, db *sql.DB, cols map[string]bool) ([]promptRow, error) {
	order := "time_created"
	if cols["id"] {
		order += ", id"
	}
	q := `SELECT coalesce(session_id, ''), ` + colOr(cols, "project_id", "''") + `, coalesce(text, ''), ` +
		colOr(cols, "kind", "''") + `, coalesce(time_created, 0) FROM input_history ORDER BY ` + order
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []promptRow
	for rows.Next() {
		var p promptRow
		if err := rows.Scan(&p.sessionID, &p.projectID, &p.text, &p.kind, &p.created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// promptMessages turns typed prompts into user messages, dropping blank ones.
func promptMessages(ps []promptRow) []model.Message {
	var out []model.Message
	for _, p := range ps {
		text := strings.TrimSpace(p.text)
		if text == "" {
			continue
		}
		out = append(out, model.Message{Role: model.RoleUser, Kind: model.KindText, At: msTime(float64(p.created)), Text: text})
	}
	return out
}

// finishSession fills the prompt title and completes StartedAt/UpdatedAt from
// the messages: StartedAt is the session's creation (else its first dated
// message), UpdatedAt the later of the stored update time and the last message.
func finishSession(s *model.Session) {
	if s.TitleSource == model.TitleNone || strings.TrimSpace(s.Title) == "" {
		s.Title, s.TitleSource = "", model.TitleNone
		for _, m := range s.Messages {
			if m.Role == model.RoleUser && m.Kind == model.KindText {
				if t := promptTitle(m.Text); t != "" {
					s.Title, s.TitleSource = t, model.TitlePrompt
					break
				}
			}
		}
	}
	var firstMsg time.Time
	for _, m := range s.Messages {
		if m.At.IsZero() {
			continue
		}
		if firstMsg.IsZero() || m.At.Before(firstMsg) {
			firstMsg = m.At
		}
		if m.At.After(s.UpdatedAt) {
			s.UpdatedAt = m.At
		}
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = firstMsg
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = s.StartedAt
	}
}

// cleanDir returns a stored directory as a native path ("" stays "").
func cleanDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return filepath.FromSlash(dir)
}
