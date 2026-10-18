package trae

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

const (
	idLogin   = "6828a0000000000000000001"
	idEmpty   = "6828a0000000000000000000"
	idChat    = "2f1e6b3c-0000-4000-8000-000000000002"
	idTitle   = "6828b0000000000000000003"
	idBadMsgs = "6828b0000000000000000004"
	idHi      = "6828b0000000000000000005"
	idRemote  = "6828c0000000000000000006"
	idGlobal  = "6828d0000000000000000007"
)

var oldTime = time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

// buildFixture copies testdata/appdata into <root>/appdata and turns every
// items.json ({key: value}) into a VS Code-style state.vscdb with an ItemTable.
// A JSON string value is stored verbatim (so a torn value can be expressed);
// any other value is stored as its JSON text. The aaa111 database is left in
// WAL mode with its rows still in the -wal file (connection kept open).
func buildFixture(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	src := filepath.Join("testdata", "appdata")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(root, "appdata", rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if d.Name() == "items.json" {
			wal := strings.Contains(p, "aaa111")
			createVSCDB(t, filepath.Join(filepath.Dir(target), "state.vscdb"), b, wal)
			return nil
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		return os.Chtimes(target, oldTime, oldTime)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func createVSCDB(t *testing.T, path string, items []byte, wal bool) {
	t.Helper()
	var kv map[string]json.RawMessage
	if err := json.Unmarshal(items, &kv); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	stmts := []string{}
	if wal {
		stmts = append(stmts, "PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0")
	}
	stmts = append(stmts, "CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)")
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := string(kv[k])
		var s string
		if json.Unmarshal(kv[k], &s) == nil {
			v = s
		}
		if _, err := db.Exec("INSERT INTO ItemTable (key, value) VALUES (?, ?)", k, v); err != nil {
			t.Fatal(err)
		}
	}
	if wal {
		t.Cleanup(func() { db.Close() })
		if _, err := os.Stat(path + "-wal"); err != nil {
			t.Fatalf("fixture db has no -wal: %v", err)
		}
		return
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, oldTime, oldTime)
}

func fixtureEnv(t *testing.T) (adapters.Env, string) {
	t.Helper()
	root := buildFixture(t)
	return adapterstest.FixtureEnv(t, root, nil), root
}

func byID(t *testing.T, sessions []model.Session) map[string]model.Session {
	t.Helper()
	m := map[string]model.Session{}
	for _, s := range sessions {
		if _, dup := m[s.NativeID]; dup {
			t.Fatalf("duplicate session %s", s.NativeID)
		}
		m[s.NativeID] = s
	}
	return m
}

// lines renders messages compactly: "role/kind@unixms: text".
func lines(msgs []model.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, fmt.Sprintf("%s/%s@%d: %s", m.Role, m.Kind, m.At.UnixMilli(), m.Text))
	}
	return out
}

func equalLines(t *testing.T, name string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s messages:\n got:\n  %s\n want:\n  %s", name, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "trae" || a.Name() != "Trae" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestSources(t *testing.T) {
	env, _ := fixtureEnv(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ rel, host, files string }
	var got []row
	for _, s := range srcs {
		if s.Adapter != "trae" {
			t.Errorf("%s: Adapter = %q", s.Path, s.Adapter)
		}
		if s.Root != "" {
			t.Errorf("%s: Root = %q", s.Path, s.Root)
		}
		rel, _ := filepath.Rel(env.AppData, s.Path)
		var files []string
		for _, f := range s.Files {
			files = append(files, filepath.Base(f))
		}
		got = append(got, row{filepath.ToSlash(rel), s.Extra["host"], strings.Join(files, ",")})
	}
	sort.Slice(got, func(i, j int) bool { return got[i].rel < got[j].rel })
	want := []row{
		{"Trae CN/User/workspaceStorage/bbb222/state.vscdb", "Trae CN", "state.vscdb,workspace.json"},
		{"Trae/User/globalStorage/state.vscdb", "Trae", "state.vscdb"},
		{"Trae/User/workspaceStorage/aaa111/state.vscdb", "Trae", "state.vscdb,state.vscdb-wal,state.vscdb-shm,workspace.json"},
		{"Trae/User/workspaceStorage/ccc333/state.vscdb", "Trae", "state.vscdb,workspace.json"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("sources:\n got %v\nwant %v", got, want)
	}
}

func TestSourcesMissingAppDataIsSilent(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources = %v, %v; want none, nil", srcs, err)
	}
	env.AppData = ""
	if srcs, err := New().Sources(env); err != nil || len(srcs) != 0 {
		t.Fatalf("Sources with empty AppData = %v, %v", srcs, err)
	}
}

func TestReadAllGolden(t *testing.T) {
	env, _ := fixtureEnv(t)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	m := byID(t, sessions)

	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.NativeID)
		if s.Agent != "trae" || s.Origin != model.OriginLive || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: agent/origin/root/machine = %q %q %q %q", s.NativeID, s.Agent, s.Origin, s.Root, s.Machine)
		}
		for _, msg := range s.Messages {
			if strings.Contains(msg.Text, "HIDDEN-REASONING") {
				t.Errorf("%s: reasoning text was archived: %q", s.NativeID, msg.Text)
			}
			if strings.Contains(msg.Text, "input history") {
				t.Errorf("%s: input history was read", s.NativeID)
			}
			if msg.Kind == model.KindToolOutput && len(msg.Text) > model.MaxToolOutput {
				t.Errorf("%s: tool output of %d bytes", s.NativeID, len(msg.Text))
			}
		}
	}
	wantIDs := []string{idChat, idLogin, idTitle, idHi, idRemote, idGlobal}
	if strings.Join(ids, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("sessions = %v\nwant       %v", ids, wantIDs)
	}
	// 42 (not an object), the session whose messages are not a list, message 7.
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
	if _, ok := m[idEmpty]; ok {
		t.Error("empty untitled session was returned")
	}
	if _, ok := m[idBadMsgs]; ok {
		t.Error("malformed session was returned")
	}

	// The agent (builder) session: paged, so partial; the fuller of the two
	// copies wins; plan items become text, tool calls and tool outputs.
	s := m[idLogin]
	if s.Cwd != `D:\work\alpha` {
		t.Errorf("login Cwd = %q", s.Cwd)
	}
	if s.Title != "Add a login page to the demo app with validation" || s.TitleSource != model.TitlePrompt {
		t.Errorf("login title = %q (%s)", s.Title, s.TitleSource)
	}
	if s.Coverage != model.CoveragePartial {
		t.Errorf("login coverage = %s, want partial (hasMore)", s.Coverage)
	}
	if !s.StartedAt.Equal(ms(1747440000000)) || !s.UpdatedAt.Equal(ms(1747443700000)) {
		t.Errorf("login times = %v .. %v", s.StartedAt, s.UpdatedAt)
	}
	equalLines(t, "login", lines(s.Messages), []string{
		"user/text@1747440060000: Add a login page to the demo app\n  with validation",
		"assistant/text@1747440120000: Let me look at the existing routes first.",
		`assistant/tool_call@1747440120000: view_files: d:\work\alpha\src\routes.ts`,
		"tool/tool_output@1747440120000: export const routes = [];",
		"assistant/tool_call@1747440120000: run_command: npm run test",
		"tool/tool_output@1747440120000: 3 tests passed",
		"assistant/text@1747440120000: Tests pass; now wiring the form.",
		`assistant/tool_call@1747440120000: create_file: d:\work\alpha\src\login.ts`,
		"assistant/text@1747440120000: Added the login page with form validation.",
		"user/text@1747443000000: أضف التحقق من البريد الإلكتروني",
		"assistant/text@1747443700000: Email validation added.",
		"assistant/text@1747443700000: Plan: validate the email field.",
		`assistant/tool_call@1747443700000: edit_file_update: d:\work\alpha\src\login.ts`,
		"tool/tool_output@1747443700000: error: file locked",
	})

	// A chat-mode session: listed in the agent storage with only a title; its
	// messages come from the chat storage (and the migration copy).
	c := m[idChat]
	if c.Cwd != `D:\work\alpha` || c.Coverage != model.CoverageFull {
		t.Errorf("chat cwd/coverage = %q %s", c.Cwd, c.Coverage)
	}
	if c.Title != "Explain closures in JS" || c.TitleSource != model.TitlePrompt {
		t.Errorf("chat title = %q (%s)", c.Title, c.TitleSource)
	}
	if !c.StartedAt.Equal(ms(1747450000000)) || !c.UpdatedAt.Equal(ms(1747450200000)) {
		t.Errorf("chat times = %v .. %v", c.StartedAt, c.UpdatedAt)
	}
	equalLines(t, "chat", lines(c.Messages), []string{
		"user/text@1747450010000: Explain closures in JS",
		"assistant/text@1747450020000: A closure captures variables from its enclosing scope.",
	})

	// Title only: kept with coverage "titles".
	ti := m[idTitle]
	if ti.Coverage != model.CoverageTitles || len(ti.Messages) != 0 || ti.Title != "Refactor the parser module" || ti.TitleSource != model.TitlePrompt {
		t.Errorf("title-only = %+v", ti)
	}
	if ti.Cwd != `C:\Users\dev\proj` {
		t.Errorf("title-only Cwd = %q (the .code-workspace folder)", ti.Cwd)
	}
	if !ti.StartedAt.Equal(ms(1747455000000)) || !ti.UpdatedAt.Equal(ms(1747455600000)) {
		t.Errorf("title-only times = %v .. %v", ti.StartedAt, ti.UpdatedAt)
	}

	// No title, no createdAt/updatedAt, a numeric-string timestamp.
	hi := m[idHi]
	if hi.Title != "Hi there" || hi.TitleSource != model.TitlePrompt || hi.Coverage != model.CoverageFull {
		t.Errorf("hi = %q %s %s", hi.Title, hi.TitleSource, hi.Coverage)
	}
	if !hi.StartedAt.Equal(ms(1747460000000)) || !hi.UpdatedAt.Equal(ms(1747460001000)) {
		t.Errorf("hi times = %v .. %v", hi.StartedAt, hi.UpdatedAt)
	}
	equalLines(t, "hi", lines(hi.Messages), []string{
		"user/text@1747460000000: Hi there",
		"assistant/text@1747460001000: Hello! How can I help?",
	})

	// A remote workspace (not a file:// URI) has no local folder.
	if r := m[idRemote]; r.Cwd != "" || len(r.Messages) != 1 {
		t.Errorf("remote = cwd %q, %d messages", r.Cwd, len(r.Messages))
	}
	// globalStorage: no workspace, no Cwd.
	if g := m[idGlobal]; g.Cwd != "" || len(g.Messages) != 2 || g.Coverage != model.CoverageFull {
		t.Errorf("global = cwd %q, %d messages, %s", g.Cwd, len(g.Messages), g.Coverage)
	}
}

func TestReadWarnsOnUnparsableValue(t *testing.T) {
	env, _ := fixtureEnv(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		if !strings.Contains(s.Path, "ccc333") {
			continue
		}
		res, err := New().Read(context.Background(), env, s)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Sessions) != 1 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "memento/icube-ai-agent-storage") {
			t.Fatalf("ccc333: %d sessions, warnings %q", len(res.Sessions), res.Warnings)
		}
		return
	}
	t.Fatal("ccc333 source not found")
}

// Reading must not change anything under the agent's folders.
func TestReadIsReadOnly(t *testing.T) {
	env, root := fixtureEnv(t)
	before := snapshot(t, filepath.Join(root, "appdata"))
	adapterstest.ReadAll(t, New(), env)
	after := snapshot(t, filepath.Join(root, "appdata"))
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Errorf("appdata changed:\nbefore %v\nafter  %v", before, after)
	}
	// The private copies are removed after each Read.
	left, _ := os.ReadDir(env.TempDir)
	if len(left) != 0 {
		t.Errorf("temp dir not cleaned: %d entries", len(left))
	}
}

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
		} else {
			out[p] = fmt.Sprintf("%d|%d", info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return out
}

func TestReadLargeMessage(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("لوحة ", 400_000) // ~4 MB of Arabic text in one message
	val, _ := json.Marshal(map[string]any{"list": []any{map[string]any{
		"sessionId": "big1", "createdAt": 1747490000000, "updatedAt": 1747490000000,
		"messages": []any{map[string]any{"role": "user", "content": big, "timestamp": 1747490000000}},
	}}})
	items, _ := json.Marshal(map[string]json.RawMessage{"memento/icube-ai-agent-storage": val})
	db := filepath.Join(root, "appdata", "Trae", "User", "workspaceStorage", "big", "state.vscdb")
	os.MkdirAll(filepath.Dir(db), 0o755)
	createVSCDB(t, db, items, false)
	env := adapterstest.FixtureEnv(t, root, nil)
	sessions, _ := adapterstest.ReadAll(t, New(), env)
	if len(sessions) != 1 || len(sessions[0].Messages) != 1 || sessions[0].Messages[0].Text != big {
		t.Fatalf("large message not read intact (%d sessions)", len(sessions))
	}
	if n := len([]rune(sessions[0].Title)); n != 100 {
		t.Errorf("title runes = %d, want 100", n)
	}
}

func TestReadDBWithoutItemTable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "appdata", "Trae", "User", "workspaceStorage", "odd", "state.vscdb")
	os.MkdirAll(filepath.Dir(path), 0o755)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE other (x)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	env := adapterstest.FixtureEnv(t, root, nil)
	srcs, _ := New().Sources(env)
	if len(srcs) != 1 {
		t.Fatalf("sources = %d", len(srcs))
	}
	res, err := New().Read(context.Background(), env, srcs[0])
	if err != nil || len(res.Sessions) != 0 || len(res.Warnings) != 1 {
		t.Fatalf("Read = %d sessions, warnings %q, err %v", len(res.Sessions), res.Warnings, err)
	}
}

func TestReadMissingDB(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	p := filepath.Join(env.AppData, "Trae", "User", "workspaceStorage", "gone", "state.vscdb")
	_, err := New().Read(context.Background(), env, adapters.Source{Adapter: "trae", Path: p, Files: []string{p}})
	if err == nil {
		t.Fatal("Read of a missing database succeeded")
	}
}

func TestWorkspaceFolder(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"folder":"file:///d%3A/work/alpha"}`, `D:\work\alpha`},
		{`{"folder":"file:///c%3A/Users/dev/My%20Project/"}`, `C:\Users\dev\My Project`},
		{`{"folder":"file:///D:/x/%D8%A8%D8%B1%D9%86%D8%A7%D9%85%D8%AC"}`, `D:\x\برنامج`},
		{`{"folder":"file://server/share/dir"}`, `\\server\share\dir`},
		{`{"folder":"file:///d%3A"}`, `D:\`},
		{`{"workspace":"file:///c%3A/Users/dev/proj/proj.code-workspace"}`, `C:\Users\dev\proj`},
		{`{"configuration":"file:///c%3A/w/a.code-workspace"}`, `C:\w`},
		{`{"folder":"vscode-remote://ssh-remote%2Bbox/home/dev/x"}`, ``},
		{`{"folder":42}`, ``},
		{`not json`, ``},
	}
	for _, c := range cases {
		if got := workspaceFolder([]byte(c.in)); got != c.want {
			t.Errorf("workspaceFolder(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToolCallText(t *testing.T) {
	cases := []struct{ name, params, want string }{
		{"run_command", `{"command":"go","args":["test","./..."]}`, "run_command: go test ./..."},
		{"view_files", `{"files":[{"file_path":"a.go"},{"file_path":"b.go"}]}`, "view_files: a.go, b.go"},
		{"delete_file", `{"file_paths":["x","y"]}`, "delete_file: x, y"},
		{"create_file", `{"filePath":"only-camel.ts"}`, "create_file: only-camel.ts"},
		{"view_folder", `{"dir_path":"d:\\w","max_depth":2}`, `view_folder: d:\w`},
		{"search_by_regex", `{"query":"foo.*","search_directory":"src"}`, "search_by_regex: foo.*"},
		{"open_preview", `{"preview_url":"http://localhost:3000"}`, "open_preview: http://localhost:3000"},
		{"mystery", `{"alpha":1}`, `mystery: {"alpha":1}`},
		{"mystery", `{}`, "mystery"},
		{"mystery", ``, "mystery"},
	}
	for _, c := range cases {
		if got := toolCallText(c.name, json.RawMessage(c.params)); got != c.want {
			t.Errorf("toolCallText(%s, %s) = %q, want %q", c.name, c.params, got, c.want)
		}
	}
}

func TestToolOutputTruncated(t *testing.T) {
	long := strings.Repeat("é", model.MaxToolOutput) // 2 bytes each
	data, _ := json.Marshal(map[string]any{"stdout": long})
	out := toolOutput(planResult{Data: data})
	if len(out) > model.MaxToolOutput || !strings.HasPrefix(long, out) || out == "" {
		t.Fatalf("tool output = %d bytes", len(out))
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if c.Resume != nil || c.App == nil || c.App.Name != "Trae" || len(c.App.Args) != 1 || c.App.Args[0] != "{cwd}" {
		t.Fatalf("caps = %+v", c)
	}
	if len(c.App.Candidates) == 0 || !strings.HasSuffix(c.App.Candidates[0], `\Trae.exe`) {
		t.Fatalf("candidates = %v", c.App.Candidates)
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
