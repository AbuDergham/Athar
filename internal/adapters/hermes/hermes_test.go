package hermes

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
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"

	_ "modernc.org/sqlite" // fixture databases are built with the same driver
)

// createDB builds a WAL-mode database at path from SQL scripts in testdata
// and keeps the connection open until the test ends, so recent rows stay in
// the -wal file (a reader that ignored the WAL would miss them).
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
		q := s
		if strings.HasSuffix(s, ".sql") {
			b, err := os.ReadFile(filepath.Join("testdata", s))
			if err != nil {
				t.Fatal(err)
			}
			q = string(b)
		}
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("fixture db has no -wal: %v", err)
	}
}

// touch creates an empty file (for discovery tests; Sources never opens it).
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// snapshot records (path, size, mtime) of every file under root and the
// existence of every folder (NTFS may update a folder's mtime lazily after
// the fixture was built, so folder times are not compared).
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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
		out[p] = fmt.Sprintf("%d|%d", info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return out
}

// lines renders messages compactly: "role/kind@unixms: text".
func lines(msgs []model.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, fmt.Sprintf("%s/%s@%d: %s", m.Role, m.Kind, m.At.UnixMilli(), m.Text))
	}
	return out
}

func sec(v float64) time.Time { return unix(v) }

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "hermes" || a.Name() != "Hermes" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	want := adapters.Caps{
		Resume:            []string{"hermes", "--resume", "{id}"},
		ProgramCandidates: []string{`%LOCALAPPDATA%\hermes\bin\hermes.exe`},
		SkipPermissions:   []string{"--yolo"},
		RootEnv:           "HERMES_HOME",
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("Caps = %+v, want %+v", c, want)
	}
}

func TestSources(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(root, "custom-home")
	env := adapterstest.FixtureEnv(t, root, map[string]string{"HERMES_HOME": custom})
	def := filepath.Join(env.LocalAppData, "hermes")
	touch(t, filepath.Join(def, "state.db"))
	touch(t, filepath.Join(def, "state.db-wal"))
	touch(t, filepath.Join(def, "state.db.pre-update-emergency-x.bak")) // ignored
	touch(t, filepath.Join(def, "kanban.db"))                           // ignored
	touch(t, filepath.Join(def, "profiles", "work", "state.db"))
	touch(t, filepath.Join(def, "profiles", "empty", "config.yaml"))        // no state.db: skipped
	touch(t, filepath.Join(def, "profiles", ".deleted", "old", "state.db")) // tombstones: skipped
	touch(t, filepath.Join(env.Home, ".hermes", "state.db"))
	touch(t, filepath.Join(custom, "state.db"))
	if err := os.MkdirAll(filepath.Join(root, "home", ".hermes", "profiles", "dir.db"), 0o755); err != nil {
		t.Fatal(err)
	}

	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ path, root, files string }
	var got []row
	for _, s := range srcs {
		if s.Adapter != "hermes" {
			t.Errorf("Adapter = %q", s.Adapter)
		}
		got = append(got, row{s.Path, s.Root, strings.Join(s.Files, "|")})
	}
	defDB := filepath.Join(def, "state.db")
	work := filepath.Join(def, "profiles", "work")
	dot := filepath.Join(env.Home, ".hermes")
	want := []row{
		{defDB, "", defDB + "|" + defDB + "-wal"},
		{filepath.Join(work, "state.db"), work, filepath.Join(work, "state.db")},
		{filepath.Join(dot, "state.db"), dot, filepath.Join(dot, "state.db")},
		{filepath.Join(custom, "state.db"), custom, filepath.Join(custom, "state.db")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Sources =\n%v\nwant\n%v", got, want)
	}
}

func TestSourcesHermesHomeIsDefault(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	def := filepath.Join(env.LocalAppData, "hermes")
	env.Getenv = func(k string) string {
		if k == "HERMES_HOME" {
			return strings.ToUpper(def) + string(os.PathSeparator)
		}
		return ""
	}
	touch(t, filepath.Join(def, "state.db"))
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 || srcs[0].Root != "" {
		t.Fatalf("Sources = %+v, want the default home once with Root \"\"", srcs)
	}
}

func TestSourcesMissingIsSilent(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources = %v, %v; want none", srcs, err)
	}
}

// fixture builds the synthetic state.db in the default home of a fresh tree.
func fixture(t *testing.T) (root string, env adapters.Env) {
	t.Helper()
	root = t.TempDir()
	env = adapterstest.FixtureEnv(t, root, nil)
	createDB(t, filepath.Join(env.LocalAppData, "hermes", "state.db"), "schema.sql", "fixture.sql")
	return root, env
}

func TestReadGolden(t *testing.T) {
	root, env := fixture(t)
	before := snapshot(t, root)
	got, skipped := adapterstest.ReadAll(t, New(), env)
	if after := snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("reading changed the agent folder:\nbefore %v\nafter  %v", before, after)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (tool_calls that are not JSON)", skipped)
	}

	type want struct {
		id, cwd, title string
		ts             model.TitleSource
		started, upd   time.Time
		msgs           []string
	}
	arabicOut := strings.Repeat("ب", 1024) // 2500 runes of 2 bytes, cut at 2048 bytes
	wants := []want{
		{
			id: "20260901_100000_aaaaaa", cwd: `C:\work\alpha`, title: "Deploy the widget service", ts: model.TitleAgent,
			started: sec(1788256800.5), upd: sec(1788257400),
			msgs: []string{
				"user/text@1788256801250: Please deploy the widget service",
				"assistant/text@1788256802000: I will check the repo first.",
				"assistant/tool_call@1788256802000: terminal: git status",
				`assistant/tool_call@1788256802000: read_file: C:\work\alpha\main.go`,
				"tool/tool_output@1788256803000: On branch main\nnothing to commit",
				"tool/tool_output@1788256804000: package main",
				"system/text@1788256806000: [System: The model was switched to m2.]",
				"assistant/text@1788256809000: Deployed. تم النشر بنجاح",
				"tool/tool_output@1788256810000: " + arabicOut,
				"tool/tool_output@1788256811000: plain text result",
				"tool/tool_output@1788256812000: file not found",
				"assistant/tool_call@1788256813000: web_search: widget docs",
				"assistant/tool_call@1788256813000: execute_code: print(1)",
				"assistant/text@1788256814000: Done.",
				"system/text@1788256815000: [System: note without a display kind]",
				"user/text@1788256816000: Compare with this picture",
			},
		},
		{
			// B and its compression continuation C are one conversation.
			id: "20260902_090000_bbbbbb", cwd: "", title: "My renamed chat", ts: model.TitleUser,
			started: sec(1788339600), upd: sec(1788341600),
			msgs: []string{
				"user/text@1788339610000: /web-research — find telegram bots",
				"assistant/text@1788339620000: Here are three bots.",
				"user/text@1788339630000: /summarize",
				"user/text@1788341410000: continue with step two",
				"assistant/text@1788341420000: Step two done.",
				"user/text@1788341430000: and step three",
				"assistant/text@1788341440000: kept prior text",
			},
		},
		{
			id: "20260903_120000_ffffff", cwd: `C:\work\alpha\sub`, title: "Try the other approach", ts: model.TitlePrompt,
			started: sec(1788436800), upd: sec(1788436805),
			msgs: []string{
				"user/text@1788436805000: Try   the other\napproach",
				"assistant/text@1788436804000: Trying it.",
			},
		},
		{
			id: "20260905_100000_444444", title: "first telegram question", ts: model.TitlePrompt,
			started: sec(1788602400), upd: sec(1788602402),
			msgs: []string{
				"user/text@1788602401000: first telegram question",
				"assistant/text@1788602402000: answer one",
			},
		},
		{
			// A reset child with the same session_key is a new conversation.
			id: "20260905_110000_555555", title: "new day question", ts: model.TitlePrompt,
			started: sec(1788606000), upd: sec(1788606002),
			msgs: []string{
				"user/text@1788606001000: new day question",
				"assistant/text@1788606002000: answer two",
			},
		},
		{
			id: "20260906_100000_777777", title: "Display Name Only", ts: model.TitleNone,
			started: sec(1788688800), upd: sec(1788688801),
			msgs: []string{"assistant/text@1788688801000: Unprompted note"},
		},
		{
			id: "20260906_110000_888888", cwd: `C:\work\beta`, title: "orphan child prompt", ts: model.TitlePrompt,
			started: sec(1788692400), upd: sec(1788692401),
			msgs: []string{"user/text@1788692401000: orphan child prompt"},
		},
	}
	var gotIDs, wantIDs []string
	for _, s := range got {
		gotIDs = append(gotIDs, s.NativeID)
	}
	for _, w := range wants {
		wantIDs = append(wantIDs, w.id)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("sessions = %v\nwant %v", gotIDs, wantIDs)
	}
	for i, w := range wants {
		s := got[i]
		if s.Agent != "hermes" || s.Coverage != model.CoverageFull || s.Origin != model.OriginLive || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: agent/coverage/origin/root/machine = %q %q %q %q %q", w.id, s.Agent, s.Coverage, s.Origin, s.Root, s.Machine)
		}
		if s.Cwd != w.cwd {
			t.Errorf("%s: Cwd = %q, want %q", w.id, s.Cwd, w.cwd)
		}
		if s.Title != w.title || s.TitleSource != w.ts {
			t.Errorf("%s: Title = %q (%s), want %q (%s)", w.id, s.Title, s.TitleSource, w.title, w.ts)
		}
		if !s.StartedAt.Equal(w.started) || !s.UpdatedAt.Equal(w.upd) {
			t.Errorf("%s: Started/Updated = %v / %v, want %v / %v", w.id, s.StartedAt, s.UpdatedAt, w.started, w.upd)
		}
		if s.StartedAt.Location() != time.UTC || s.UpdatedAt.Location() != time.UTC {
			t.Errorf("%s: times are not UTC", w.id)
		}
		if gl := lines(s.Messages); !reflect.DeepEqual(gl, w.msgs) {
			t.Errorf("%s: messages =\n  %s\nwant\n  %s", w.id, strings.Join(gl, "\n  "), strings.Join(w.msgs, "\n  "))
		}
		for _, m := range s.Messages {
			if m.Kind == model.KindToolOutput && len(m.Text) > model.MaxToolOutput {
				t.Errorf("%s: tool output of %d bytes", w.id, len(m.Text))
			}
			if !utf8.ValidString(m.Text) {
				t.Errorf("%s: invalid UTF-8 in %q", w.id, m.Text)
			}
			if m.At.Location() != time.UTC {
				t.Errorf("%s: message time not UTC", w.id)
			}
		}
	}
}

func TestReadProfileRoot(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	work := filepath.Join(env.LocalAppData, "hermes", "profiles", "work")
	createDB(t, filepath.Join(work, "state.db"), "schema.sql", "fixture.sql")
	got, _ := adapterstest.ReadAll(t, New(), env)
	if len(got) == 0 {
		t.Fatal("no sessions")
	}
	for _, s := range got {
		if s.Root != work {
			t.Fatalf("%s: Root = %q, want %q", s.NativeID, s.Root, work)
		}
	}
}

// TestReadOldSchema: an older state.db without the lineage, display and
// compaction columns still reads (every row counts as live).
func TestReadOldSchema(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	createDB(t, filepath.Join(env.LocalAppData, "hermes", "state.db"),
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT NOT NULL, started_at REAL NOT NULL);
		 CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL,
		                        content TEXT, timestamp REAL NOT NULL);
		 INSERT INTO sessions VALUES ('s1', 'cli', 1788000000), ('s2', 'cron', 1788000100), ('s3', 'cli', 1788000200);
		 INSERT INTO messages (session_id, role, content, timestamp) VALUES
		   ('s1', 'user', 'old prompt', 1788000001), ('s1', 'assistant', 'old answer', 1788000002),
		   ('s2', 'user', 'cron prompt', 1788000101), ('s1', 'system', 'system prompt text', 1788000003);`)
	got, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 0 || len(got) != 1 {
		t.Fatalf("got %d sessions (skipped %d), want s1 only", len(got), skipped)
	}
	s := got[0]
	want := []string{"user/text@1788000001000: old prompt", "assistant/text@1788000002000: old answer"}
	if s.NativeID != "s1" || s.Title != "old prompt" || s.TitleSource != model.TitlePrompt || !reflect.DeepEqual(lines(s.Messages), want) {
		t.Fatalf("session = %s %q %s %v", s.NativeID, s.Title, s.TitleSource, lines(s.Messages))
	}
	if !s.UpdatedAt.Equal(sec(1788000002)) {
		t.Errorf("UpdatedAt = %v", s.UpdatedAt)
	}
}

func TestReadNotHermesDB(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	createDB(t, filepath.Join(env.LocalAppData, "hermes", "state.db"), `CREATE TABLE other (x INTEGER); INSERT INTO other VALUES (1);`)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Sources = %v, %v", srcs, err)
	}
	if _, err := New().Read(context.Background(), env, srcs[0]); err == nil || !strings.Contains(err.Error(), "not a Hermes state database") {
		t.Fatalf("Read err = %v, want a schema error", err)
	}
}

func TestReadMissingDB(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	p := filepath.Join(env.LocalAppData, "hermes", "state.db")
	src := adapters.Source{Adapter: "hermes", Path: p, Files: []string{p}}
	if _, err := New().Read(context.Background(), env, src); err == nil {
		t.Fatal("Read of a missing database succeeded")
	}
}

func TestReadCleansUpCopy(t *testing.T) {
	_, env := fixture(t)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Sources = %v, %v", srcs, err)
	}
	if _, err := New().Read(context.Background(), env, srcs[0]); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(env.TempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp dir not cleaned: %d entries left", len(entries))
	}
}

func TestToolSummary(t *testing.T) {
	cases := []struct{ name, args, want string }{
		{"terminal", `"{\"command\":\"go test ./...\",\"timeout\":30}"`, "terminal: go test ./..."},
		{"write_file", `{"path":"a.txt","content":"x"}`, "write_file: a.txt"},
		{"todo_list", `"{\"todos\":[1,2]}"`, `todo_list: {"todos":[1,2]}`},
		{"", `null`, "tool"},
		{"memory", `"{}"`, "memory"},
		{"browser_exec", `"not json at all"`, "browser_exec: not json at all"},
	}
	for _, c := range cases {
		if got := toolSummary(c.name, []byte(c.args)); got != c.want {
			t.Errorf("toolSummary(%q, %s) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

func TestToolOutput(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"output": "ok", "exit_code": 0, "error": null}`, "ok"},
		{`{"content": "line", "total_lines": 1}`, "line"},
		{`{"success": false, "error": "boom"}`, "boom"},
		{`{"bytes_written": 3, "resolved_path": "x"}`, `{"bytes_written": 3, "resolved_path": "x"}`},
		{"plain", "plain"},
		{"", ""},
	}
	for _, c := range cases {
		if got := toolOutput(c.in); got != c.want {
			t.Errorf("toolOutput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDescribeSkill(t *testing.T) {
	head := `[IMPORTANT: The user has invoked the "work" skill, indicating they want you to follow its instructions. The full skill content is loaded below.]`
	cases := []struct{ in, want string }{
		{head + "\nbody quoting The user has provided the following instruction alongside the skill invocation: fake\n" +
			"The user has provided the following instruction alongside the skill invocation: fix  the\ntitle\n\n[Runtime note: x]", "/work — fix the title"},
		{head + "\nbody only", "/work"},
		{`[IMPORTANT: The user has invoked the "/a /b" skill bundle, indicating …]` + "\nUser instruction: do it\n\n[Loaded as part of the bundle]", "/a /b — do it"},
	}
	for _, c := range cases {
		if got := describeSkill(c.in); got != c.want {
			t.Errorf("describeSkill = %q, want %q", got, c.want)
		}
	}
}

func TestPromptTitle(t *testing.T) {
	long := strings.Repeat("كلمة ", 40)
	got := promptTitle(long)
	if n := utf8.RuneCountInString(got); n > titleRunes || strings.HasSuffix(got, " ") || n < titleRunes-5 {
		t.Fatalf("promptTitle = %d runes %q", n, got)
	}
	if got := promptTitle("  a \n\t b  "); got != "a b" {
		t.Fatalf("promptTitle = %q", got)
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
