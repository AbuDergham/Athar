// Package imports reads the session indexes of two session browsers the owner
// used before Athar: AgentsView (~\.agentsview\sessions.db) and Pi Session
// Manager (~\.pi\agent\sessions\sessions.db), plus copies of those databases
// kept under <Documents>\AI-History-Backup\ (spec §4.5).
//
// Both tools keep their own copy of every conversation they indexed, so they
// are the only local copy of sessions whose agent files were deleted since
// (Claude Code and Gemini CLI delete old transcripts). Every imported session
// is filed under the LIVE agent it came from (Session.Agent is "claude",
// "codex", ...; never "agentsview"/"psm") with the agent's own native id, so an
// imported row and its live twin collapse into one archive session keyed by
// (agent, native_id). The archive never lets an import replace a live session.
//
// Mapping (details in agentsview.go, psm.go and agentmap.go):
//   - Agent: from the row's source file path using the live adapters' path
//     rules (e.g. \.claude-local\projects\ -> claude with Root), falling back to
//     a label table for the tool's own agent names (vscode-copilot -> copilot,
//     antigravity-cli -> agy, claude-code -> claude, ...).
//   - NativeID: the tool's id without its "<agent>:" prefix (and, for Pi
//     Session Manager, without its ":<path hash>" suffix).
//   - Subagent rows (relationship_type 'subagent' or any parent session) are
//     skipped. Thinking text is never imported.
//   - Coverage is full when the row has messages, titles when it has only a
//     title; rows with neither are skipped.
//
// Databases are read only through fsread.CopySQLite + fsread.OpenSQLiteCopy.
// The importers have no Caps: resume/open actions use the live agent's Caps.
package imports

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// Adapter ids of the two importers (used only for their sources).
const (
	AgentsViewID = "agentsview"
	PSMID        = "psm"
)

const (
	// backupDir is the folder under Documents whose *.db files are searched.
	backupDir = "AI-History-Backup"
	// maxBackupDepth bounds the folder depth searched below backupDir.
	maxBackupDepth = 6
	// extraBackup marks sources found under backupDir.
	extraBackup = "backup"
)

type kind int

const (
	kindAgentsView kind = iota + 1
	kindPSM
)

type importer struct{ kind kind }

// NewAgentsView returns the AgentsView index importer (id "agentsview").
func NewAgentsView() adapters.Adapter { return importer{kind: kindAgentsView} }

// NewPSM returns the Pi Session Manager index importer (id "psm").
func NewPSM() adapters.Adapter { return importer{kind: kindPSM} }

func (i importer) ID() string {
	if i.kind == kindPSM {
		return PSMID
	}
	return AgentsViewID
}

func (i importer) Name() string {
	if i.kind == kindPSM {
		return "Pi Session Manager index"
	}
	return "AgentsView index"
}

func (importer) Version() int { return 1 }

// Caps is empty: imported sessions carry the live agent's id, and actions use
// that agent's Caps.
func (importer) Caps() adapters.Caps { return adapters.Caps{} }

// Sources lists the tool's own database and the matching backups. Backups
// whose file name names the other tool are left to it; backups with a neutral
// name are listed by both importers and Read keeps only the one whose schema
// matches. Missing locations are skipped silently.
func (i importer) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	seen := map[string]bool{}
	add := func(db string, backup bool) {
		key := strings.ToLower(filepath.Clean(db))
		if db == "" || seen[key] || !isFile(db) {
			return
		}
		seen[key] = true
		src := adapters.Source{Adapter: i.ID(), Path: db, Files: fsread.SQLiteSidecars(db)}
		if backup {
			src.Extra = map[string]string{extraBackup: "1"}
		}
		out = append(out, src)
	}
	for _, db := range i.primaryDBs(env) {
		add(db, false)
	}
	for _, db := range backupDBs(env) {
		if k := nameHint(db); k == 0 || k == i.kind {
			add(db, true)
		}
	}
	return out, nil
}

// primaryDBs lists where the tool keeps its live database.
func (i importer) primaryDBs(env adapters.Env) []string {
	getenv := func(k string) string {
		if env.Getenv == nil {
			return ""
		}
		return strings.TrimSpace(env.Getenv(k))
	}
	var dirs []string
	if i.kind == kindPSM {
		// PI_CODING_AGENT_DIR replaces %USERPROFILE%\.pi\agent.
		if v := getenv("PI_CODING_AGENT_DIR"); v != "" {
			dirs = append(dirs, filepath.Join(v, "sessions"))
		}
		if env.Home != "" {
			dirs = append(dirs, filepath.Join(env.Home, ".pi", "agent", "sessions"))
		}
	} else {
		for _, k := range []string{"AGENTSVIEW_DATA_DIR", "AGENT_VIEWER_DATA_DIR"} {
			if v := getenv(k); v != "" {
				dirs = append(dirs, v)
			}
		}
		if env.Home != "" {
			dirs = append(dirs, filepath.Join(env.Home, ".agentsview"))
		}
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, filepath.Join(d, "sessions.db"))
	}
	return out
}

// backupDBs lists every *.db file under <Documents>\AI-History-Backup, sorted.
func backupDBs(env adapters.Env) []string {
	if env.Documents == "" {
		return nil
	}
	root := filepath.Join(env.Documents, backupDir)
	if !isDir(root) {
		return nil
	}
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && p != root {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if rel, rerr := filepath.Rel(root, p); rerr == nil && rel != "." &&
				strings.Count(rel, string(filepath.Separator))+1 > maxBackupDepth {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && strings.EqualFold(filepath.Ext(p), ".db") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// nameHint guesses which tool a backup file belongs to from its name; 0 = unknown.
func nameHint(p string) kind {
	n := strings.ToLower(filepath.Base(p))
	switch {
	case strings.Contains(n, "agentsview"), strings.Contains(n, "agents-view"), strings.Contains(n, "agents_view"):
		return kindAgentsView
	case strings.Contains(n, "pi-session"), strings.Contains(n, "pi_session"), strings.Contains(n, "pisession"), strings.Contains(n, "psm"):
		return kindPSM
	}
	return 0
}

// Read copies the database and reads its sessions.
func (i importer) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	copyPath, cleanup, err := fsread.CopySQLite(src.Path, env.TempDir)
	defer cleanup()
	if err != nil {
		return adapters.ReadResult{}, err
	}
	db, err := fsread.OpenSQLiteCopy(copyPath)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	defer db.Close()

	sch, err := readSchema(ctx, db)
	if err != nil {
		return adapters.ReadResult{}, fmt.Errorf("read schema: %w", err)
	}
	var res adapters.ReadResult
	switch {
	case i.kind == kindAgentsView && sch.isAgentsView():
		res, err = readAgentsView(ctx, db, sch)
	case i.kind == kindPSM && sch.isPSM():
		res, err = readPSM(ctx, db, sch)
	default:
		// A neutrally named backup that belongs to the other importer, or an
		// unrelated database: nothing to import. Only a primary location is
		// worth a warning.
		if src.Extra[extraBackup] == "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s is not a %s database; nothing imported", src.Path, i.Name()))
		}
		return res, nil
	}
	if err != nil {
		return adapters.ReadResult{}, err
	}
	res.Sessions = dedupe(res.Sessions)
	return res, nil
}

// dedupe keeps one session per (agent, root, native id): the one with the
// most messages (then the latest update). Indexes can list one conversation
// twice (e.g. the same file seen under two paths).
func dedupe(in []model.Session) []model.Session {
	idx := map[string]int{}
	out := in[:0]
	for _, s := range in {
		k := s.Agent + "\x00" + s.Root + "\x00" + s.NativeID
		if j, ok := idx[k]; ok {
			o := out[j]
			if len(s.Messages) > len(o.Messages) ||
				(len(s.Messages) == len(o.Messages) && s.UpdatedAt.After(o.UpdatedAt)) {
				out[j] = s
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, s)
	}
	return out
}

// schema lists the columns of the tables the importers look at.
type schema map[string]map[string]bool

func readSchema(ctx context.Context, db *sql.DB) (schema, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sch := schema{}
	for _, t := range tables {
		switch t {
		case "sessions", "messages", "tool_calls", "message_entries", "scan_state":
		default:
			continue
		}
		cols := map[string]bool{}
		crow, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, t)
		if err != nil {
			return nil, err
		}
		for crow.Next() {
			var c string
			if err := crow.Scan(&c); err != nil {
				crow.Close()
				return nil, err
			}
			cols[strings.ToLower(c)] = true
		}
		crow.Close()
		if err := crow.Err(); err != nil {
			return nil, err
		}
		sch[t] = cols
	}
	return sch, nil
}

func (s schema) has(table string, cols ...string) bool {
	t, ok := s[table]
	if !ok {
		return false
	}
	for _, c := range cols {
		if !t[c] {
			return false
		}
	}
	return true
}

// col returns the column name for a SELECT list, or NULL when it is missing.
func (s schema) col(table, alias, c string) string {
	if s.has(table, c) {
		if alias != "" {
			return alias + "." + c
		}
		return c
	}
	return "NULL"
}

// isAgentsView: sessions(id, agent, file_path) + messages(session_id, ordinal, role, content).
func (s schema) isAgentsView() bool {
	return s.has("sessions", "id", "agent", "file_path") &&
		s.has("messages", "session_id", "ordinal", "role", "content")
}

// isPSM: sessions(id, path) + message_entries(session_path, role, content).
func (s schema) isPSM() bool {
	return s.has("sessions", "id", "path") &&
		s.has("message_entries", "session_path", "role", "content")
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
