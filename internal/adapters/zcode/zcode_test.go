package zcode

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"

	_ "modernc.org/sqlite" // fixture databases are built with the same driver
)

// createDB builds a WAL-mode database at path from SQL scripts in testdata and
// keeps the connection open until the test ends, so recent rows stay in the
// -wal file (a reader that ignored the WAL would miss them).
func createDB(t *testing.T, path string, scripts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, s := range scripts {
		b, err := os.ReadFile(filepath.Join("testdata", s))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("fixture db has no -wal: %v", err)
	}
}

// fixture returns an Env whose home holds .zcode\cli\db\db.sqlite and,
// when withTasks, .zcode\v2\tasks-index.sqlite.
func fixture(t *testing.T, withTasks bool) (adapters.Env, string) {
	t.Helper()
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	zdir := filepath.Join(env.Home, ".zcode")
	createDB(t, filepath.Join(zdir, "cli", "db", "db.sqlite"), "schema.sql", "fixture.sql")
	if withTasks {
		createDB(t, filepath.Join(zdir, "v2", "tasks-index.sqlite"), "tasks_schema.sql", "tasks.sql")
	}
	return env, zdir
}

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

func byID(t *testing.T, sessions []model.Session) map[string]model.Session {
	t.Helper()
	out := map[string]model.Session{}
	for _, s := range sessions {
		if _, dup := out[s.NativeID]; dup {
			t.Fatalf("duplicate session %s", s.NativeID)
		}
		out[s.NativeID] = s
	}
	return out
}

func TestSources(t *testing.T) {
	env, zdir := fixture(t, true)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 {
		t.Fatalf("got %d sources, want 1", len(srcs))
	}
	db := filepath.Join(zdir, "cli", "db", "db.sqlite")
	tasks := filepath.Join(zdir, "v2", "tasks-index.sqlite")
	src := srcs[0]
	if src.Adapter != ID || src.Path != db {
		t.Errorf("source = %+v", src)
	}
	want := map[string]bool{db: true, db + "-wal": true, tasks: true, tasks + "-wal": true}
	for _, f := range src.Files {
		delete(want, f)
	}
	if len(want) != 0 {
		t.Errorf("Files %v miss %v", src.Files, want)
	}

	// No ZCode folder at all: no sources, no error.
	empty := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	if srcs, err := New().Sources(empty); err != nil || len(srcs) != 0 {
		t.Errorf("empty home: %v, %v", srcs, err)
	}
}

func TestRead(t *testing.T) {
	env, _ := fixture(t, false)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	// bad part JSON, bad message JSON, input history row without a session id
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
	got := byID(t, sessions)
	var ids []string
	for id := range got {
		ids = append(ids, id)
	}
	for _, id := range []string{"sess_alpha", "sess_beta", "sess_fork", "sess_prompts", "sess_gone", "sess_gone2"} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing session %s (have %v)", id, ids)
		}
	}
	for _, id := range []string{"sess_child", "sess_oldchild", "sess_empty"} {
		if _, ok := got[id]; ok {
			t.Errorf("session %s must be skipped", id)
		}
	}
	if len(got) != 6 {
		t.Errorf("got %d sessions %v, want 6", len(got), ids)
	}

	a := got["sess_alpha"]
	if a.Agent != ID || a.Cwd != `C:\work\alpha` || a.Title != "Fix the build" || a.TitleSource != model.TitleAgent ||
		a.Coverage != model.CoverageFull || a.Origin != model.OriginLive || a.Root != "" || a.Machine != "" {
		t.Errorf("alpha header = %+v", a)
	}
	if !a.StartedAt.Equal(ms(1782000000000)) {
		t.Errorf("alpha StartedAt = %v", a.StartedAt)
	}
	if !a.UpdatedAt.Equal(ms(1782000900000)) {
		t.Errorf("alpha UpdatedAt = %v", a.UpdatedAt)
	}
	long := strings.Repeat("0", model.MaxToolOutput)
	want := []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, At: ms(1782000001001), Text: "hello there"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1782000002003), Text: "Running the build."},
		{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ms(1782000002004), Text: "Bash: go build ./..."},
		{Role: model.RoleTool, Kind: model.KindToolOutput, At: ms(1782000002500), Text: "build ok"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1782000002600), Text: "Build passed."},
		{Role: model.RoleUser, Kind: model.KindText, At: ms(1782000005001), Text: "راجع المشروع من فضلك\n\n[file: notes.txt]"},
		{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ms(1782000006001), Text: `Read: C:\work\alpha\missing.go`},
		{Role: model.RoleTool, Kind: model.KindToolOutput, At: ms(1782000006002), Text: "file not found"},
		{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ms(1782000006100), Text: "Bash: dir /s"},
		{Role: model.RoleTool, Kind: model.KindToolOutput, At: ms(1782000006100), Text: long},
		{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ms(1782000006200), Text: `TodoWrite: {"todos":[{"content":"step one","status":"pending"}]}`},
		{Role: model.RoleUser, Kind: model.KindText, At: ms(1782000009002), Text: "last question"},
	}
	if !reflect.DeepEqual(a.Messages, want) {
		t.Errorf("alpha messages:\n got %d:", len(a.Messages))
		for i, m := range a.Messages {
			w := model.Message{}
			if i < len(want) {
				w = want[i]
			}
			if !reflect.DeepEqual(m, w) {
				t.Errorf("  [%d] got  %+v\n       want %+v", i, m, w)
			}
		}
		for i := len(a.Messages); i < len(want); i++ {
			t.Errorf("  [%d] missing %+v", i, want[i])
		}
	}
	for _, m := range a.Messages {
		for _, bad := range []string{"private chain", "inlined file", "todo reminder", "Summary of the earlier", "orphan text"} {
			if strings.Contains(m.Text, bad) {
				t.Errorf("alpha keeps %q", bad)
			}
		}
	}

	b := got["sess_beta"]
	if b.Cwd != filepath.FromSlash("D:/proj/beta") {
		t.Errorf("beta Cwd = %q", b.Cwd)
	}
	if b.Title != "hi there" || b.TitleSource != model.TitlePrompt {
		t.Errorf("beta title = %q (%s)", b.Title, b.TitleSource)
	}
	if len(b.Messages) != 2 || !b.UpdatedAt.Equal(ms(1782100500000)) {
		t.Errorf("beta = %+v", b)
	}

	f := got["sess_fork"]
	if f.Title != "My fork" || f.TitleSource != model.TitleUser || len(f.Messages) != 1 || f.Messages[0].Text != "forked question" {
		t.Errorf("fork = %+v", f)
	}

	p := got["sess_prompts"]
	if p.Coverage != model.CoveragePrompts || p.Origin != model.OriginLive || p.Cwd != `C:\work\alpha` {
		t.Errorf("prompts header = %+v", p)
	}
	if p.Title != "first prompt only" || p.TitleSource != model.TitlePrompt {
		t.Errorf("prompts title = %q (%s)", p.Title, p.TitleSource)
	}
	wantP := []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, At: ms(1782300001000), Text: "first   prompt only"},
		{Role: model.RoleUser, Kind: model.KindText, At: ms(1782300002000), Text: "second prompt"},
	}
	if !reflect.DeepEqual(p.Messages, wantP) {
		t.Errorf("prompts messages = %+v", p.Messages)
	}
	if !p.StartedAt.Equal(ms(1782300000000)) || !p.UpdatedAt.Equal(ms(1782300002000)) {
		t.Errorf("prompts times = %v .. %v", p.StartedAt, p.UpdatedAt)
	}

	g := got["sess_gone"]
	if g.Coverage != model.CoveragePrompts || g.Origin != model.OriginPromptLog || g.Cwd != `C:\work\alpha` ||
		g.Title != "prompt of a deleted session" || g.TitleSource != model.TitlePrompt || len(g.Messages) != 1 ||
		!g.StartedAt.Equal(ms(1782500001000)) || !g.UpdatedAt.Equal(ms(1782500001000)) {
		t.Errorf("gone = %+v", g)
	}
	g2 := got["sess_gone2"]
	if g2.Cwd != "" || g2.Origin != model.OriginPromptLog || len(g2.Messages) != 1 {
		t.Errorf("gone2 = %+v", g2)
	}
}

func TestReadTaskIndexTitles(t *testing.T) {
	env, _ := fixture(t, true)
	sessions, _ := adapterstest.ReadAll(t, New(), env)
	got := byID(t, sessions)
	if b := got["sess_beta"]; b.Title != "Renamed by user" || b.TitleSource != model.TitleUser {
		t.Errorf("beta title = %q (%s)", b.Title, b.TitleSource)
	}
	if a := got["sess_alpha"]; a.Title != "Fix the build" || a.TitleSource != model.TitleAgent {
		t.Errorf("alpha title = %q (%s)", a.Title, a.TitleSource)
	}
	if f := got["sess_fork"]; f.Title != "My fork" {
		t.Errorf("fork title = %q (deleted task rows must be ignored)", f.Title)
	}
}

// TestReadOnly checks that reading leaves ZCode's folder exactly as it was.
func TestReadOnly(t *testing.T) {
	env, zdir := fixture(t, true)
	before := snapshot(t, zdir)
	adapterstest.ReadAll(t, New(), env)
	after := snapshot(t, zdir)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("ZCode folder changed:\nbefore %v\nafter  %v", before, after)
	}
}

// snapshot records every entry under root, with (size, mtime) for files. Folder
// mtimes are left out: NTFS updates them lazily after the fixture is built.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[p] = "dir"
			return nil
		}
		out[p] = fmt.Sprintf("size=%d mtime=%s", info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadUnsupportedSchema(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	path := filepath.Join(env.Home, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE unrelated (x text)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Sources: %v, %v", srcs, err)
	}
	if _, err := New().Read(context.Background(), env, srcs[0]); err == nil {
		t.Error("Read of a database without session/message/part tables must fail")
	}
}

func TestReadCancelled(t *testing.T) {
	env, _ := fixture(t, false)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Read(ctx, env, srcs[0]); err == nil {
		t.Error("Read with a cancelled context must fail")
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if c.Resume != nil {
		t.Errorf("Resume = %v, want nil (no terminal resume)", c.Resume)
	}
	if c.App == nil || c.App.Name != "ZCode" || len(c.App.Candidates) == 0 ||
		!strings.Contains(c.App.Candidates[0], `%LOCALAPPDATA%\Programs\ZCode\ZCode.exe`) {
		t.Errorf("App = %+v", c.App)
	}
}

func TestToolSummary(t *testing.T) {
	cases := []struct {
		tool, input, want string
	}{
		{"Bash", `{"command":"ls -la","description":"List"}`, "Bash: ls -la"},
		{"Edit", `{"file_path":"C:\\a\\b.go","old_string":"x"}`, `Edit: C:\a\b.go`},
		{"Grep", `{"pattern":"TODO","path":"src"}`, "Grep: TODO"},
		{"WebFetch", `{"url":"https://example.com"}`, "WebFetch: https://example.com"},
		{"", `{}`, "tool"},
		{"Skill", `null`, "Skill"},
		{"Agent", `{"description":"find files","prompt":"look"}`, "Agent: find files"},
		{"Custom", `{"a":1}`, `Custom: {"a":1}`},
	}
	for _, c := range cases {
		if got := toolSummary(c.tool, []byte(c.input)); got != c.want {
			t.Errorf("toolSummary(%q, %s) = %q, want %q", c.tool, c.input, got, c.want)
		}
	}
}

// TestRealData prints statistics about this machine's ZCode data (ATHAR_REALDATA=1).
func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
