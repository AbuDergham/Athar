package zed

import (
	"bytes"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
	"github.com/klauspost/compress/zstd"

	_ "modernc.org/sqlite"
)

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }

// row is one synthetic row of Zed's threads table. nil pointers are NULL.
type row struct {
	id, summary, updated, dataType string
	data                           []byte
	parent, paths, order, created  *string
}

func str(s string) *string { return &s }

// sidebarRow is one synthetic row of sidebar_threads in db\<channel>\db.sqlite.
type sidebarRow struct {
	sessionID, agentID, title, override *string
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// compress zstd-compresses data with the streaming encoder, which (like Zed's
// zstd::encode_all) writes frames without a content size.
func compress(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func openDB(t *testing.T, path string, wal bool) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	mode := "DELETE"
	if wal {
		mode = "WAL"
	}
	if _, err := db.Exec("PRAGMA journal_mode=" + mode); err != nil {
		t.Fatal(err)
	}
	if wal {
		if _, err := db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

const threadsSchema = `CREATE TABLE threads (
	id TEXT PRIMARY KEY,
	summary TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	data_type TEXT NOT NULL,
	data BLOB NOT NULL
, parent_id TEXT, folder_paths TEXT, folder_paths_order TEXT, created_at TEXT)`

const legacyThreadsSchema = `CREATE TABLE threads (
	id TEXT PRIMARY KEY,
	summary TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	data_type TEXT NOT NULL,
	data BLOB NOT NULL
)`

const sidebarSchema = `CREATE TABLE "sidebar_threads" (
	thread_id BLOB PRIMARY KEY,
	session_id TEXT,
	agent_id TEXT,
	title TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	created_at TEXT,
	folder_paths TEXT,
	folder_paths_order TEXT,
	archived INTEGER DEFAULT 0,
	main_worktree_paths TEXT,
	main_worktree_paths_order TEXT,
	remote_connection TEXT
, interacted_at TEXT, title_override TEXT) STRICT`

// writeThreads creates a threads.db at path (the returned DB stays open when
// keepOpen, like a running Zed holding un-checkpointed WAL content).
func writeThreads(t *testing.T, path string, legacy, wal, keepOpen bool, rows []row) *sql.DB {
	t.Helper()
	db := openDB(t, path, wal)
	schema := threadsSchema
	if legacy {
		schema = legacyThreadsSchema
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		var err error
		if legacy {
			_, err = db.Exec(`INSERT INTO threads (id, summary, updated_at, data_type, data) VALUES (?, ?, ?, ?, ?)`,
				r.id, r.summary, r.updated, r.dataType, r.data)
		} else {
			_, err = db.Exec(`INSERT INTO threads (id, summary, updated_at, data_type, data, parent_id, folder_paths, folder_paths_order, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				r.id, r.summary, r.updated, r.dataType, r.data, r.parent, r.paths, r.order, r.created)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if keepOpen {
		t.Cleanup(func() { db.Close() })
		return db
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return nil
}

func writeSidebar(t *testing.T, path string, withTable bool, rows []sidebarRow) {
	t.Helper()
	db := openDB(t, path, true)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE kv_store (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if !withTable {
		return
	}
	if _, err := db.Exec(sidebarSchema); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		title := ""
		if r.title != nil {
			title = *r.title
		}
		if _, err := db.Exec(`INSERT INTO sidebar_threads (thread_id, session_id, agent_id, title, updated_at, title_override) VALUES (?, ?, ?, ?, ?, ?)`,
			[]byte{byte(i), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, r.sessionID, r.agentID, title, "2026-08-11T15:21:12+00:00", r.override); err != nil {
			t.Fatal(err)
		}
	}
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func msg(role model.Role, kind model.Kind, text string) model.Message {
	return model.Message{Role: role, Kind: kind, Text: text}
}

const (
	idA = "a0000000-0000-4000-8000-00000000000a" // v3, zstd, agent title, two folders
	idB = "b0000000-0000-4000-8000-00000000000b" // legacy 0.2.0 as json text
	idC = "c0000000-0000-4000-8000-00000000000c" // empty title -> prompt
	idD = "d0000000-0000-4000-8000-00000000000d" // "New Thread" + sidebar override, big record
	idE = "e0000000-0000-4000-8000-00000000000e" // child thread (parent_id)
	idF = "f0000000-0000-4000-8000-00000000000f" // subagent_context set
	idG = "10000000-0000-4000-8000-000000000010" // corrupt zstd
	idH = "20000000-0000-4000-8000-000000000020" // unknown data_type
	idI = "30000000-0000-4000-8000-000000000030" // no messages (empty draft)
	idJ = "40000000-0000-4000-8000-000000000040" // json data_type, not JSON
)

// bigText is a single user message of about 2 MB.
var bigText = "start " + strings.Repeat("0123456789abcdef", 2<<16) + " end"

func mainRows(t *testing.T) []row {
	t.Helper()
	threadC := `{"title":"","messages":[{"User":{"id":"u","content":[{"Text":"   Refactor   the\n parser   module please "}]}},{"Agent":{"content":[{"Text":"Sure."}],"tool_results":{}}}],"updated_at":"2026-07-01T09:00:00+00:00","version":"0.3.0"}`
	threadD := `{"title":"New Thread","messages":[` +
		`{"User":{"id":"u","content":[{"Text":` + jsonString(bigText) + `}]}},` +
		`{"Agent":{"content":[{"ToolUse":{"id":"k1","name":"fetch","raw_input":"","input":{"url":"https://example.com/docs"},"is_input_complete":true}},` +
		`{"ToolUse":{"id":"k2","name":"screenshot","raw_input":"","input":{},"is_input_complete":true}},` +
		`{"ToolUse":{"id":"k3","name":"mcp_tool","raw_input":"{\"a\":1","input":null,"is_input_complete":false}}],` +
		`"tool_results":{"k1":{"tool_use_id":"k1","tool_name":"fetch","is_error":false,"content":{"Text":` + jsonString(strings.Repeat("x", 30000)) + `},"output":null},` +
		`"k2":{"tool_use_id":"k2","tool_name":"screenshot","is_error":false,"content":{"Image":{"source":"AAAA","size":null}},"output":null}}}}` +
		`],"updated_at":"2026-06-01T09:00:00+00:00","version":"0.3.0"}`
	threadE := `{"title":"child","messages":[{"User":{"id":"u","content":[{"Text":"subtask"}]}}],"updated_at":"2026-08-11T15:00:00+00:00","version":"0.3.0"}`
	threadF := `{"title":"sub","messages":[{"User":{"id":"u","content":[{"Text":"subtask"}]}}],"updated_at":"2026-08-11T15:00:00+00:00","subagent_context":{"parent_thread_id":"` + idA + `","depth":1},"version":"0.3.0"}`
	threadI := `{"title":"","messages":[],"draft_prompt":[{"Text":"unsent"}],"updated_at":"2026-08-11T15:00:00+00:00","version":"0.3.0"}`
	return []row{
		{id: idA, summary: "Fix the login flow", updated: "2026-08-11T18:21:12.123456789+03:00", dataType: "zstd",
			data: compress(t, fixture(t, "thread-v3.json")), paths: str("C:\\work\\alpha\nC:\\work\\beta"), order: str("1,0"),
			created: str("2026-08-11T15:00:05.5+00:00")},
		{id: idB, summary: "", updated: "2025-05-01T10:00:00Z", dataType: "json", data: fixture(t, "thread-legacy.json")},
		{id: idC, summary: "", updated: "2026-07-01T09:00:00+00:00", dataType: "zstd", data: compress(t, []byte(threadC)),
			paths: str(""), order: str(""), created: str("2026-07-01T08:59:00+00:00")},
		{id: idD, summary: "New Thread", updated: "2026-06-01T09:00:00+00:00", dataType: "zstd", data: compress(t, []byte(threadD)),
			paths: str("C:\\work\\gamma"), order: str("0"), created: str("not a time")},
		{id: idE, summary: "child", updated: "2026-08-11T15:00:00+00:00", dataType: "zstd", data: compress(t, []byte(threadE)),
			parent: str(idA)},
		{id: idF, summary: "sub", updated: "2026-08-11T15:00:00+00:00", dataType: "zstd", data: compress(t, []byte(threadF))},
		{id: idG, summary: "broken", updated: "2026-08-11T15:00:00+00:00", dataType: "zstd", data: []byte("this is not zstd")},
		{id: idH, summary: "future", updated: "2026-08-11T15:00:00+00:00", dataType: "brotli", data: []byte("{}")},
		{id: idI, summary: "", updated: "2026-08-11T15:00:00+00:00", dataType: "zstd", data: compress(t, []byte(threadI))},
		{id: idJ, summary: "bad json", updated: "2026-08-11T15:00:00+00:00", dataType: "json", data: []byte("{not json")},
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// zedRoot builds <root>\localappdata\Zed with threads.db and a sidebar DB.
func zedRoot(t *testing.T, rows []row) (root string, env adapters.Env) {
	t.Helper()
	root = t.TempDir()
	env = adapterstest.FixtureEnv(t, root, nil)
	zed := filepath.Join(env.LocalAppData, "Zed")
	writeThreads(t, filepath.Join(zed, "threads", "threads.db"), false, false, false, rows)
	writeSidebar(t, filepath.Join(zed, "db", "0-stable", "db.sqlite"), true, []sidebarRow{
		{sessionID: str(idD), title: str("New Thread"), override: str("  My   renamed thread ")},
		{sessionID: str(idA), title: str("Fix the login flow")},
		{sessionID: str("99999999-0000-4000-8000-000000000099"), agentID: str("claude-acp"), title: str("external"), override: str("ignored")},
		{sessionID: str(idC), title: str("x"), override: str("   ")},
	})
	writeSidebar(t, filepath.Join(zed, "db", "0-preview", "db.sqlite"), false, nil)
	writeSidebar(t, filepath.Join(zed, "db", "0-global", "db.sqlite"), false, nil)
	return root, env
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "zed" || a.Name() != "Zed" || a.Version() < 1 {
		t.Errorf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
	// Spec §6.3: Zed is read-only (the app is not installed; no per-thread resume).
	if !reflect.DeepEqual(a.Caps(), adapters.Caps{}) {
		t.Errorf("Caps = %+v, want none", a.Caps())
	}
}

func TestSources(t *testing.T) {
	_, env := zedRoot(t, mainRows(t))
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 {
		t.Fatalf("got %d sources, want 1: %+v", len(srcs), srcs)
	}
	s := srcs[0]
	zed := filepath.Join(env.LocalAppData, "Zed")
	threads := filepath.Join(zed, "threads", "threads.db")
	if s.Adapter != "zed" || s.Path != threads || s.Root != "" {
		t.Errorf("source = %+v", s)
	}
	if len(s.Files) == 0 || s.Files[0] != threads {
		t.Errorf("Files[0] = %v, want the threads db first", s.Files)
	}
	has := map[string]bool{}
	for _, f := range s.Files {
		has[f] = true
	}
	stable := filepath.Join(zed, "db", "0-stable", "db.sqlite")
	// The fixture db was closed, so SQLite removed its -wal; any sidecar that
	// exists must be listed.
	for _, want := range fsread.SQLiteSidecars(stable) {
		if !has[want] {
			t.Errorf("Files lacks %s: %v", want, s.Files)
		}
	}
	if has[filepath.Join(zed, "db", "0-global", "db.sqlite")] {
		t.Errorf("Files includes the global kv db: %v", s.Files)
	}

	// No Zed folder at all: nothing, silently.
	empty := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	if srcs, err := New().Sources(empty); err != nil || len(srcs) != 0 {
		t.Errorf("Sources(empty) = %v, %v", srcs, err)
	}
}

func TestRead(t *testing.T) {
	_, env := zedRoot(t, mainRows(t))
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3 (corrupt zstd, unknown data_type, bad JSON)", skipped)
	}
	var ids []string
	byID := map[string]model.Session{}
	for _, s := range sessions {
		ids = append(ids, s.NativeID)
		byID[s.NativeID] = s
	}
	sort.Strings(ids)
	want := []string{idA, idB, idC, idD}
	sort.Strings(want)
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("sessions = %v, want %v", ids, want)
	}
	for _, s := range sessions {
		if s.Agent != "zed" || s.Origin != model.OriginLive || s.Coverage != model.CoverageFull || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: agent/origin/coverage/root/machine = %q %q %q %q %q", s.NativeID, s.Agent, s.Origin, s.Coverage, s.Root, s.Machine)
		}
		if s.StartedAt.Location() != time.UTC || s.UpdatedAt.Location() != time.UTC {
			t.Errorf("%s: times not UTC", s.NativeID)
		}
		if s.UpdatedAt.Before(s.StartedAt) {
			t.Errorf("%s: UpdatedAt %v before StartedAt %v", s.NativeID, s.UpdatedAt, s.StartedAt)
		}
		for _, m := range s.Messages {
			if !m.At.IsZero() {
				t.Errorf("%s: message time %v, want zero (Zed stores none)", s.NativeID, m.At)
			}
			if m.Kind == model.KindToolOutput && len(m.Text) > model.MaxToolOutput {
				t.Errorf("%s: tool output of %d bytes", s.NativeID, len(m.Text))
			}
			if strings.Contains(m.Text, "reasoning") || strings.Contains(m.Text, "attached file body") || strings.Contains(m.Text, "opaque") {
				t.Errorf("%s: thinking or mention/context body leaked: %q", s.NativeID, m.Text)
			}
		}
	}

	a := byID[idA]
	if a.Title != "Fix the login flow" || a.TitleSource != model.TitleAgent {
		t.Errorf("A title = %q (%s)", a.Title, a.TitleSource)
	}
	if a.Cwd != `C:\work\beta` {
		t.Errorf("A cwd = %q, want the first folder in the user's order", a.Cwd)
	}
	if !a.StartedAt.Equal(utc("2026-08-11T15:00:05.5Z")) || !a.UpdatedAt.Equal(utc("2026-08-11T15:21:12.123456789Z")) {
		t.Errorf("A times = %v .. %v", a.StartedAt, a.UpdatedAt)
	}
	wantA := []model.Message{
		msg(model.RoleUser, model.KindText, "Please fix the login bug in @login.go [image]"),
		msg(model.RoleAssistant, model.KindText, "I will look at the file."),
		msg(model.RoleAssistant, model.KindToolCall, "read_file: alpha/login.go"),
		msg(model.RoleTool, model.KindToolOutput, "package main"),
		msg(model.RoleAssistant, model.KindToolCall, "terminal: go test ./..."),
		msg(model.RoleTool, model.KindToolOutput, "ok  \talpha\t0.1s"),
		msg(model.RoleTool, model.KindToolOutput, "orphan result"),
		msg(model.RoleAssistant, model.KindText, "The login bug is fixed."),
		msg(model.RoleUser, model.KindText, "Thanks! Now review @older chat and the مشروع"),
	}
	checkMessages(t, "A", a.Messages, wantA)

	b := byID[idB]
	if b.Title != "Legacy plan" || b.TitleSource != model.TitleAgent {
		t.Errorf("B title = %q (%s)", b.Title, b.TitleSource)
	}
	if b.Cwd != `D:\legacy\proj` {
		t.Errorf("B cwd = %q, want the snapshot worktree", b.Cwd)
	}
	if !b.StartedAt.Equal(utc("2025-05-01T09:00:00Z")) || !b.UpdatedAt.Equal(utc("2025-05-01T10:00:00Z")) {
		t.Errorf("B times = %v .. %v", b.StartedAt, b.UpdatedAt)
	}
	checkMessages(t, "B", b.Messages, []model.Message{
		msg(model.RoleUser, model.KindText, "Write a deploy script"),
		msg(model.RoleAssistant, model.KindText, "Here is the script."),
		msg(model.RoleAssistant, model.KindToolCall, "edit_file: deploy.ps1"),
		msg(model.RoleTool, model.KindToolOutput, "Edited deploy.ps1"),
		msg(model.RoleAssistant, model.KindText, "Done."),
	})

	c := byID[idC]
	if c.Title != "Refactor the parser module please" || c.TitleSource != model.TitlePrompt {
		t.Errorf("C title = %q (%s)", c.Title, c.TitleSource)
	}
	if c.Cwd != "" {
		t.Errorf("C cwd = %q, want empty (no folder, no snapshot)", c.Cwd)
	}
	if !c.StartedAt.Equal(utc("2026-07-01T08:59:00Z")) {
		t.Errorf("C started = %v", c.StartedAt)
	}

	d := byID[idD]
	if d.Title != "My renamed thread" || d.TitleSource != model.TitleUser {
		t.Errorf("D title = %q (%s), want the sidebar title_override", d.Title, d.TitleSource)
	}
	if d.Cwd != `C:\work\gamma` {
		t.Errorf("D cwd = %q", d.Cwd)
	}
	if !d.StartedAt.Equal(d.UpdatedAt) || !d.UpdatedAt.Equal(utc("2026-06-01T09:00:00Z")) {
		t.Errorf("D times = %v .. %v (bad created_at falls back to updated_at)", d.StartedAt, d.UpdatedAt)
	}
	if len(d.Messages) != 6 {
		t.Fatalf("D has %d messages, want 6: %+v", len(d.Messages), kinds(d.Messages))
	}
	if d.Messages[0].Role != model.RoleUser || d.Messages[0].Text != bigText {
		t.Errorf("D big user message not kept whole (len %d)", len(d.Messages[0].Text))
	}
	checkMessages(t, "D tools", d.Messages[1:], []model.Message{
		msg(model.RoleAssistant, model.KindToolCall, "fetch: https://example.com/docs"),
		msg(model.RoleTool, model.KindToolOutput, strings.Repeat("x", model.MaxToolOutput)),
		msg(model.RoleAssistant, model.KindToolCall, "screenshot"),
		msg(model.RoleTool, model.KindToolOutput, "[image]"),
		msg(model.RoleAssistant, model.KindToolCall, `mcp_tool: {"a":1`),
	})
}

func TestReadLegacyTable(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	path := filepath.Join(env.LocalAppData, "Zed", "threads", "threads.db")
	writeThreads(t, path, true, false, false, []row{
		{id: "old-1", summary: "", updated: "2025-02-01T08:30:00Z", dataType: "json", data: fixture(t, "thread-old.json")},
	})
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 0 || len(sessions) != 1 {
		t.Fatalf("got %d sessions, %d skipped", len(sessions), skipped)
	}
	s := sessions[0]
	if s.Title != "Very old thread" || s.TitleSource != model.TitleAgent || s.Cwd != "/home/example/old" {
		t.Errorf("session = %q (%s) cwd %q", s.Title, s.TitleSource, s.Cwd)
	}
	if !s.StartedAt.Equal(utc("2025-02-01T08:00:00Z")) || !s.UpdatedAt.Equal(utc("2025-02-01T08:30:00Z")) {
		t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
	}
	checkMessages(t, "old", s.Messages, []model.Message{
		msg(model.RoleUser, model.KindText, "Explain the build"),
		msg(model.RoleAssistant, model.KindText, "It uses make."),
		msg(model.RoleAssistant, model.KindToolCall, "grep: TODO"),
		msg(model.RoleTool, model.KindToolOutput, "main.go:3: TODO"),
	})
}

func TestReadErrors(t *testing.T) {
	// A database without a threads table is not Zed's: the source fails.
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	path := filepath.Join(env.LocalAppData, "Zed", "threads", "threads.db")
	db := openDB(t, path, false)
	if _, err := db.Exec(`CREATE TABLE other (x)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Sources = %v, %v", srcs, err)
	}
	if _, err := New().Read(t.Context(), env, srcs[0]); err == nil {
		t.Error("Read of a db without threads table: want an error")
	}

	// A missing file is an error too (the sync records it).
	srcs[0].Path = filepath.Join(t.TempDir(), "gone.db")
	srcs[0].Files = []string{srcs[0].Path}
	if _, err := New().Read(t.Context(), env, srcs[0]); err == nil {
		t.Error("Read of a missing db: want an error")
	}
}

// TestReadOnly: while "Zed" holds both databases open in WAL mode with
// un-checkpointed rows, a read sees those rows and leaves the Zed folder
// exactly as it was (no new files, same sizes and mtimes).
func TestReadOnly(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	zed := filepath.Join(env.LocalAppData, "Zed")
	w := writeThreads(t, filepath.Join(zed, "threads", "threads.db"), false, true, true, nil)
	threadC := `{"title":"WAL thread","messages":[{"User":{"id":"u","content":[{"Text":"only in the wal"}]}}],"updated_at":"2026-07-01T09:00:00+00:00","version":"0.3.0"}`
	if _, err := w.Exec(`INSERT INTO threads (id, summary, updated_at, data_type, data) VALUES (?, ?, ?, ?, ?)`,
		idC, "WAL thread", "2026-07-01T09:00:00+00:00", "zstd", compress(t, []byte(threadC))); err != nil {
		t.Fatal(err)
	}
	sb := openDB(t, filepath.Join(zed, "db", "0-stable", "db.sqlite"), true)
	defer sb.Close()
	if _, err := sb.Exec(sidebarSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Exec(`INSERT INTO sidebar_threads (thread_id, session_id, title, updated_at, title_override) VALUES (x'00', ?, 'x', 'y', 'Renamed in WAL')`, idC); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(zed, "threads", "threads.db-wal")); err != nil {
		t.Fatalf("fixture has no -wal: %v", err)
	}

	before := snapshot(t, zed)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	after := snapshot(t, zed)
	if skipped != 0 || len(sessions) != 1 || sessions[0].Title != "Renamed in WAL" || len(sessions[0].Messages) != 1 {
		t.Errorf("sessions = %+v, skipped %d", sessions, skipped)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("Zed folder changed:\nbefore %v\nafter  %v", before, after)
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Stat(p) // Stat, not DirEntry.Info: the listing can be stale on Windows
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[p] = "dir"
			return nil
		}
		out[p] = info.ModTime().UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(info.Size(), 10)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func kinds(ms []model.Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, string(m.Role)+"/"+string(m.Kind))
	}
	return out
}

func checkMessages(t *testing.T, name string, got, want []model.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d messages %v, want %d %v", name, len(got), kinds(got), len(want), kinds(want))
	}
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			g, w := got[i].Text, want[i].Text
			if len(g) > 80 {
				g = g[:80] + "…"
			}
			if len(w) > 80 {
				w = w[:80] + "…"
			}
			t.Errorf("%s: message %d = %s/%s %q, want %s/%s %q", name, i, got[i].Role, got[i].Kind, g, want[i].Role, want[i].Kind, w)
		}
	}
}
