// Package zed reads the native agent threads of the Zed editor.
//
// Store (verified against a Zed 2026 install, structure only):
//   - %LOCALAPPDATA%\Zed\threads\threads.db, SQLite in rollback-journal mode,
//     table threads(id, summary, updated_at, data_type, data, parent_id,
//     folder_paths, folder_paths_order, created_at). Older installs have only the
//     first five columns. One row per thread; data is the thread as JSON, stored
//     zstd-compressed (data_type "zstd", frames without a content size, so a
//     streaming decoder is used) or as plain JSON (data_type "json").
//   - Rows with parent_id set, and threads whose JSON has a subagent_context,
//     are subagent threads and are skipped (spec §1: subagent transcripts are
//     not archived).
//   - %LOCALAPPDATA%\Zed\db\<n>-<channel>\db.sqlite (WAL mode), table
//     sidebar_threads: sidebar metadata; title_override is the user's own name
//     for a thread (session_id = threads.id, agent_id NULL for native threads).
//     The <n>-global folder holds only a key-value store and is not read.
//
// Thread JSON: see thread.go. Zed stores no per-message times, so messages
// carry a zero At. Cwd is the first folder of the thread's project in the
// user's order (folder_paths + folder_paths_order), else the first worktree of
// initial_project_snapshot.
//
// External ACP agents (for example Claude Code run inside Zed) keep their
// transcripts in their own stores; only their sidebar rows are here, and those
// are ignored. The Zed app is not installed on the owner's machine and no URL
// opens one thread, so the adapter has no resume or app command.
package zed

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
	"github.com/klauspost/compress/zstd"
)

const (
	agentID = "zed"
	// maxThreadJSON bounds one decompressed thread (a guard against a corrupt
	// or hostile blob expanding without limit).
	maxThreadJSON = 1 << 30
)

type adapter struct{}

// New returns the Zed adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return agentID }
func (adapter) Name() string { return "Zed" }
func (adapter) Version() int { return 1 }

// Caps: none. Zed has no command or URL that opens one thread, and the app is
// not installed here, so Zed threads are read-only in Athar (spec §6.3).
func (adapter) Caps() adapters.Caps { return adapters.Caps{} }

// zedDir is %LOCALAPPDATA%\Zed.
func zedDir(env adapters.Env) string {
	if env.LocalAppData == "" {
		return ""
	}
	return filepath.Join(env.LocalAppData, "Zed")
}

// Sources returns one source: threads.db. Its Files are the threads db and its
// sidecars first, then every channel db (db\<n>-<channel>\db.sqlite and its
// sidecars) whose sidebar_threads may hold a user's rename, so a rename also
// makes the source be read again. No threads.db means no source.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	dir := zedDir(env)
	if dir == "" {
		return nil, nil
	}
	threads := filepath.Join(dir, "threads", "threads.db")
	if fi, err := os.Stat(threads); err != nil || fi.IsDir() {
		return nil, nil
	}
	files := fsread.SQLiteSidecars(threads)
	for _, db := range channelDBs(dir) {
		files = append(files, fsread.SQLiteSidecars(db)...)
	}
	return []adapters.Source{{Adapter: agentID, Path: threads, Files: files}}, nil
}

// channelDBs lists db\<n>-<channel>\db.sqlite under dir, stable first, then
// preview, nightly, dev and any other channel by name. The global db is left out.
func channelDBs(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "db", "*", "db.sqlite"))
	var out []string
	for _, m := range matches {
		ch := strings.ToLower(filepath.Base(filepath.Dir(m)))
		if strings.HasSuffix(ch, "-global") {
			continue
		}
		if fi, err := os.Stat(m); err != nil || fi.IsDir() {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := channelRank(out[i]), channelRank(out[j])
		if pi != pj {
			return pi < pj
		}
		return out[i] < out[j]
	})
	return out
}

func channelRank(db string) int {
	ch := strings.ToLower(filepath.Base(filepath.Dir(db)))
	for i, suffix := range []string{"-stable", "-preview", "-nightly", "-dev"} {
		if strings.HasSuffix(ch, suffix) {
			return i
		}
	}
	return 4
}

// Read copies threads.db (and the channel dbs for renames) and converts every
// top-level thread with at least one message. Rows that cannot be decoded
// (corrupt zstd, unknown data_type, bad JSON) are counted in Skipped.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	overrides, warns := renames(env, src)
	res.Warnings = warns

	cp, cleanup, err := fsread.CopySQLite(src.Path, env.TempDir)
	defer cleanup()
	if err != nil {
		return res, err
	}
	db, err := fsread.OpenSQLiteCopy(cp)
	if err != nil {
		return res, fmt.Errorf("zed: open copy of %s: %w", src.Path, err)
	}
	defer db.Close()

	cols, err := columns(db, "threads")
	if err != nil {
		return res, fmt.Errorf("zed: %s: %w", src.Path, err)
	}
	if !cols["id"] || !cols["data"] || !cols["data_type"] {
		return res, fmt.Errorf("zed: %s has no threads table", src.Path)
	}
	opt := func(c string) string {
		if cols[c] {
			return c
		}
		return "NULL"
	}
	q := fmt.Sprintf(`SELECT id, %s, %s, data_type, data, %s, %s, %s, %s FROM threads`,
		opt("summary"), opt("updated_at"), opt("parent_id"), opt("folder_paths"), opt("folder_paths_order"), opt("created_at"))
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return res, fmt.Errorf("zed: query %s: %w", src.Path, err)
	}
	defer rows.Close()

	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return res, err
	}
	defer dec.Close()

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		var r threadRow
		if err := rows.Scan(&r.id, &r.summary, &r.updated, &r.dataType, &r.data, &r.parent, &r.paths, &r.order, &r.created); err != nil {
			res.Skipped++
			continue
		}
		if strings.TrimSpace(r.parent.String) != "" {
			continue // a subagent thread
		}
		data, err := decode(dec, r.dataType.String, r.data)
		if err != nil {
			res.Skipped++
			continue
		}
		s, ok, err := convert(r, data, overrides[r.id.String])
		if err != nil {
			res.Skipped++
			continue
		}
		if ok {
			res.Sessions = append(res.Sessions, s)
		}
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("zed: read %s: %w", src.Path, err)
	}
	return res, nil
}

// threadRow is one row of the threads table (missing columns scan as NULL).
type threadRow struct {
	id, summary, updated, dataType sql.NullString
	data                           []byte
	parent, paths, order, created  sql.NullString
}

// decode returns a row's thread JSON.
func decode(dec *zstd.Decoder, dataType string, data []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(dataType)) {
	case "json":
		return data, nil
	case "zstd":
		if err := dec.Reset(bytes.NewReader(data)); err != nil {
			return nil, err
		}
		out, err := io.ReadAll(io.LimitReader(dec, maxThreadJSON+1))
		if err != nil {
			return nil, err
		}
		if len(out) > maxThreadJSON {
			return nil, errors.New("zed: thread larger than 1 GiB")
		}
		return out, nil
	}
	return nil, fmt.Errorf("zed: unknown data_type %q", dataType)
}

// convert turns one decoded row into a session. ok is false for threads that
// are not archived (subagent threads, threads without messages).
func convert(r threadRow, data []byte, override string) (model.Session, bool, error) {
	t, err := parseThread(data)
	if err != nil {
		return model.Session{}, false, err
	}
	if t.subagent || len(t.messages) == 0 {
		return model.Session{}, false, nil
	}
	s := model.Session{
		Agent:    agentID,
		NativeID: r.id.String,
		Cwd:      firstFolder(r.paths.String, r.order.String),
		Coverage: model.CoverageFull,
		Origin:   model.OriginLive,
		Messages: t.messages,
	}
	if s.Cwd == "" {
		s.Cwd = t.worktree
	}

	s.UpdatedAt = parseTime(r.updated.String)
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = t.updated
	}
	s.StartedAt = parseTime(r.created.String)
	if s.StartedAt.IsZero() {
		s.StartedAt = t.snapshotAt
	}
	switch {
	case s.StartedAt.IsZero():
		s.StartedAt = s.UpdatedAt
	case s.UpdatedAt.IsZero():
		s.UpdatedAt = s.StartedAt
	}
	if s.StartedAt.IsZero() {
		return model.Session{}, false, errors.New("zed: thread without any date")
	}
	if s.UpdatedAt.Before(s.StartedAt) {
		s.StartedAt = s.UpdatedAt
	}

	s.Title, s.TitleSource = title(override, []string{t.title, t.summary, r.summary.String}, t.messages)
	return s, true, nil
}

// defaultTitle is the title Zed shows before it has named a thread.
const defaultTitle = "New Thread"

// title picks the user's rename, then Zed's generated title, then the first
// user prompt.
func title(override string, agentTitles []string, msgs []model.Message) (string, model.TitleSource) {
	if t := collapse(override); t != "" {
		return t, model.TitleUser
	}
	for _, at := range agentTitles {
		if t := collapse(at); t != "" && !strings.EqualFold(t, defaultTitle) {
			return t, model.TitleAgent
		}
	}
	for _, m := range msgs {
		if m.Role == model.RoleUser && m.Kind == model.KindText {
			if t := cutRunes(collapse(m.Text), 100); t != "" {
				return t, model.TitlePrompt
			}
		}
	}
	return "", model.TitleNone
}

// firstFolder returns the first project folder in the user's order. Zed stores
// the folders sorted, one per line, and in order the original position of each
// ("1,0" = the second stored folder came first).
func firstFolder(paths, order string) string {
	var list []string
	for _, p := range strings.Split(strings.ReplaceAll(paths, "\r\n", "\n"), "\n") {
		if p = strings.TrimSpace(p); p != "" {
			list = append(list, p)
		}
	}
	if len(list) == 0 {
		return ""
	}
	idx := strings.Split(order, ",")
	if len(idx) == len(list) {
		best, bestPos := -1, 0
		for i, s := range idx {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				best = -1
				break
			}
			if best < 0 || n < bestPos {
				best, bestPos = i, n
			}
		}
		if best >= 0 {
			return list[best]
		}
	}
	return list[0]
}

// parseTime parses Zed's RFC 3339 times into UTC; zero when unparseable.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// columns returns the column names of table (empty when it does not exist).
func columns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
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
		out[strings.ToLower(n)] = true
	}
	return out, rows.Err()
}
