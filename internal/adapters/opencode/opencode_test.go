package opencode

import (
	"context"
	"database/sql"
	"errors"
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
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"

	_ "modernc.org/sqlite" // fixture databases are built with the same driver
)

// oldTime is the mtime given to every copied fixture file, so no fixture
// counts as "being written right now".
var oldTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

// copyTree copies the committed testdata/home tree to dst/home.
func copyTree(t *testing.T, dst string) {
	t.Helper()
	src := filepath.Join("testdata", "home")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, "home", rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		return os.Chtimes(target, oldTime, oldTime)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Directory mtimes too (they are part of legacy fingerprints).
	filepath.WalkDir(filepath.Join(dst, "home"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chtimes(p, oldTime, oldTime)
		}
		return nil
	})
}

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

// fixture returns a root with home/.local/share/opencode holding the legacy
// JSON tree, opencode.db and opencode-beta.db.
func fixture(t *testing.T) (root, data string) {
	t.Helper()
	root = t.TempDir()
	copyTree(t, root)
	data = filepath.Join(root, "home", ".local", "share", "opencode")
	createDB(t, filepath.Join(data, "opencode.db"), "schema.sql", "fixture.sql")
	createDB(t, filepath.Join(data, "opencode-beta.db"), "schema.sql", "beta.sql")
	return root, data
}

// snapshot records (path, size, mtime) of every file and folder under root.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fmt.Sprintf("%v|%d|%d", d.IsDir(), info.Size(), info.ModTime().UnixNano())
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

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "opencode" || a.Name() != "OpenCode" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
	c := a.Caps()
	// Resume stays disabled until tested against a copy of a real DB (spec §13.3).
	if c.Resume != nil || c.Fork != nil || c.SkipPermissions != nil || c.App != nil {
		t.Fatalf("Caps = %+v, want no resume/app until verified", c)
	}
}

func TestSources(t *testing.T) {
	root, data := fixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]adapters.Source{}
	for _, s := range srcs {
		if s.Adapter != "opencode" {
			t.Errorf("source %s has adapter %q", s.Path, s.Adapter)
		}
		byPath[s.Path] = s
	}
	storage := filepath.Join(data, "storage")
	want := []string{
		filepath.Join(data, "opencode-beta.db"),
		filepath.Join(data, "opencode.db"),
		filepath.Join(storage, "session", "0123abcd", "ses_legacy2.json"),
		filepath.Join(storage, "session", "global", "ses_legacy1.json"),
	}
	if len(srcs) != len(want) {
		t.Fatalf("got %d sources, want %d: %v", len(srcs), len(want), srcs)
	}
	for _, w := range want {
		if _, ok := byPath[w]; !ok {
			t.Errorf("missing source %s", w)
		}
	}
	db := byPath[filepath.Join(data, "opencode.db")]
	wantFiles := []string{db.Path, db.Path + "-wal", db.Path + "-shm"}
	if !reflect.DeepEqual(db.Files, wantFiles) {
		t.Errorf("db Files = %v, want %v", db.Files, wantFiles)
	}
	leg := byPath[filepath.Join(storage, "session", "global", "ses_legacy1.json")]
	wantLeg := []string{
		leg.Path,
		filepath.Join(storage, "message", "ses_legacy1"),
		filepath.Join(storage, "message", "ses_legacy1", "msg_l1.json"),
		filepath.Join(storage, "part", "msg_l1"),
		filepath.Join(storage, "message", "ses_legacy1", "msg_l2.json"),
		filepath.Join(storage, "part", "msg_l2"),
	}
	if !reflect.DeepEqual(leg.Files, wantLeg) {
		t.Errorf("legacy Files =\n%v\nwant\n%v", leg.Files, wantLeg)
	}
	for _, s := range srcs {
		if s.Root != "" {
			t.Errorf("source %s Root = %q, want default", s.Path, s.Root)
		}
	}
}

func TestSourcesMissingRootIsSilent(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on an empty home = %v, %v; want none", srcs, err)
	}
}

func TestSourcesXDGAndOpencodeDB(t *testing.T) {
	root, _ := fixture(t)
	xdg := filepath.Join(root, "xdg")
	createDB(t, filepath.Join(xdg, "opencode", "opencode.db"), "schema.sql", "beta.sql")
	custom := filepath.Join(root, "elsewhere", "custom.db")
	createDB(t, custom, "schema.sql", "beta.sql")
	env := adapterstest.FixtureEnv(t, root, map[string]string{
		"XDG_DATA_HOME": xdg,
		"OPENCODE_DB":   custom,
	})
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range srcs {
		if got[s.Path] {
			t.Errorf("duplicate source %s", s.Path)
		}
		got[s.Path] = true
	}
	for _, w := range []string{filepath.Join(xdg, "opencode", "opencode.db"), custom} {
		if !got[w] {
			t.Errorf("missing source %s in %v", w, srcs)
		}
	}
	// The default root is still read (Athar's environment may differ from OpenCode's).
	if !got[filepath.Join(root, "home", ".local", "share", "opencode", "opencode.db")] {
		t.Errorf("default opencode.db missing")
	}
}

func TestReadAllGolden(t *testing.T) {
	root, _ := fixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	before := snapshot(t, root)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if after := snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("reading changed the fixture tree:\nbefore %v\nafter  %v", before, after)
	}

	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.NativeID)
	}
	wantIDs := []string{"ses_alpha", "ses_beta", "ses_big", "ses_gamma", "ses_legacy1"}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("sessions = %v, want %v (child and empty sessions skipped)", ids, wantIDs)
	}
	// Broken JSON: one part and one message in opencode.db, one legacy part file.
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
	by := map[string]model.Session{}
	for _, s := range sessions {
		by[s.NativeID] = s
		if s.Agent != "opencode" || s.Coverage != model.CoverageFull || s.Origin != model.OriginLive || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: agent/coverage/origin/root/machine = %q %q %q %q %q", s.NativeID, s.Agent, s.Coverage, s.Origin, s.Root, s.Machine)
		}
		for _, m := range s.Messages {
			if strings.Contains(m.Text, "PRIVATE-REASONING") {
				t.Errorf("%s: reasoning text archived", s.NativeID)
			}
			if strings.Contains(m.Text, "Called the Read tool") {
				t.Errorf("%s: synthetic part archived", s.NativeID)
			}
			if strings.Contains(m.Text, "COMPACTION-SUMMARY") {
				t.Errorf("%s: compaction summary archived", s.NativeID)
			}
			if m.Kind == model.KindToolOutput && len(m.Text) > model.MaxToolOutput {
				t.Errorf("%s: tool output of %d bytes", s.NativeID, len(m.Text))
			}
		}
	}

	t.Run("alpha", func(t *testing.T) {
		s := by["ses_alpha"]
		if s.Cwd != filepath.FromSlash("C:/work/alpha") {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if s.Title != "Refactor the parser module" || s.TitleSource != model.TitleAgent {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1783850400000)) || !s.UpdatedAt.Equal(ms(1783850460000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		if s.StartedAt.Location() != time.UTC || s.UpdatedAt.Location() != time.UTC {
			t.Errorf("times not UTC")
		}
		out := strings.Repeat("ok", 1500)[:model.MaxToolOutput]
		want := []string{
			"user/text@1783850400000: Please refactor the parser in parser.go\n\n[image]",
			"assistant/text@1783850402000: I will run the tests first.",
			"assistant/tool_call@1783850403000: bash: go test ./...",
			"tool/tool_output@1783850404000: " + out,
			"assistant/text@1783850405000: All tests pass.",
			"user/text@1783850450000: Now add a README",
			"assistant/tool_call@1783850452000: write: C:/work/alpha/README.md",
			"tool/tool_output@1783850453000: Wrote file successfully.",
			"assistant/text@1783850459000: Done.",
		}
		if got := lines(s.Messages); !reflect.DeepEqual(got, want) {
			t.Errorf("messages =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})

	t.Run("beta", func(t *testing.T) {
		s := by["ses_beta"]
		if s.Cwd != filepath.FromSlash("D:/Projects/beta") {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		// The default "New session - <ISO time>" title is replaced by the first prompt.
		if s.Title != "كيف أنشر المشروع على الخادم؟" || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		want := []string{
			"user/text@1783929600000: كيف أنشر   المشروع\nعلى الخادم؟",
			"assistant/tool_call@1783929602000: webfetch: https://example.com/docs",
			"tool/tool_output@1783929603000: timeout",
			"assistant/text@1783929605000: استخدم الأمر deploy",
		}
		if got := lines(s.Messages); !reflect.DeepEqual(got, want) {
			t.Errorf("messages =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if !s.StartedAt.Equal(ms(1783929600000)) || !s.UpdatedAt.Equal(ms(1783929660000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
	})

	t.Run("big", func(t *testing.T) {
		s := by["ses_big"]
		if len(s.Messages) != 1 || len(s.Messages[0].Text) != 2<<20 || s.Messages[0].Role != model.RoleUser {
			t.Fatalf("big session: %d messages, first %d bytes", len(s.Messages), len(s.Messages[0].Text))
		}
		if s.Title != "Big paste" || s.TitleSource != model.TitleAgent {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
	})

	t.Run("gamma from channel db", func(t *testing.T) {
		s := by["ses_gamma"]
		want := []string{
			"user/text@1785585600000: hello from the beta channel",
			"assistant/text@1785585601000: hi",
		}
		if got := lines(s.Messages); !reflect.DeepEqual(got, want) {
			t.Errorf("messages = %q", got)
		}
		if s.Cwd != filepath.FromSlash("C:/work/gamma") {
			t.Errorf("Cwd = %q", s.Cwd)
		}
	})

	t.Run("legacy json", func(t *testing.T) {
		s := by["ses_legacy1"]
		if s.Cwd != `C:\work\legacy` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if s.Title != "Legacy chat about Go" || s.TitleSource != model.TitleAgent {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1770746400000)) || !s.UpdatedAt.Equal(ms(1770746500000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		want := []string{
			"user/text@1770746400000: What does defer do in Go?",
			"assistant/text@1770746402000: defer runs a call when the surrounding function returns.",
			`assistant/tool_call@1770746403000: read: C:\work\legacy\main.go`,
			"tool/tool_output@1770746404000: package main",
		}
		if got := lines(s.Messages); !reflect.DeepEqual(got, want) {
			t.Errorf("messages =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
}

// copyAndReadDB copies path with fsread.CopySQLite (as the Kilo adapter does) and
// runs ReadDB on the copy.
func copyAndReadDB(t *testing.T, path, agent string) ([]model.Session, int, error) {
	t.Helper()
	cp, cleanup, err := fsread.CopySQLite(path, t.TempDir())
	t.Cleanup(cleanup)
	if err != nil {
		t.Fatalf("CopySQLite: %v", err)
	}
	return ReadDB(cp, agent)
}

func TestReadDBKiloSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "kilo.db")
	createDB(t, path, "kilo_schema.sql")
	before := snapshot(t, filepath.Join(dir, "data"))
	sessions, skipped, err := copyAndReadDB(t, path, "kilo")
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, filepath.Join(dir, "data")); !reflect.DeepEqual(before, after) {
		t.Errorf("reading changed the database folder")
	}
	if skipped != 0 || len(sessions) != 1 {
		t.Fatalf("got %d sessions, %d skipped", len(sessions), skipped)
	}
	s := sessions[0]
	if s.Agent != "kilo" || s.NativeID != "ses_kilo1" || s.Title != "Kilo task title" || s.Cwd != filepath.FromSlash("C:/work/kilo") {
		t.Errorf("session = %q %q %q %q", s.Agent, s.NativeID, s.Title, s.Cwd)
	}
	want := []string{
		"user/text@1784019600000: explain the build script",
		"assistant/text@1784019601000: It compiles the project.",
	}
	if got := lines(s.Messages); !reflect.DeepEqual(got, want) {
		t.Errorf("messages = %q", got)
	}
}

// ReadDB only opens CopySQLite copies: an agent's database is never opened in place.
func TestReadDBRefusesOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kilo.db")
	createDB(t, path, "kilo_schema.sql")
	if _, _, err := ReadDB(path, "kilo"); err == nil {
		t.Fatal("ReadDB opened a database that is not a CopySQLite copy")
	}
}

// The adapter's Read copies into env.TempDir and removes the copy afterwards.
func TestReadCleansUpCopy(t *testing.T) {
	root, data := fixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	path := filepath.Join(data, "opencode.db")
	res, err := New().Read(context.Background(), env, adapters.Source{Adapter: "opencode", Path: path, Files: fsread.SQLiteSidecars(path)})
	if err != nil || len(res.Sessions) == 0 {
		t.Fatalf("Read = %d sessions, %v", len(res.Sessions), err)
	}
	if ents, _ := os.ReadDir(env.TempDir); len(ents) != 0 {
		t.Errorf("temp copies left behind: %v", ents)
	}
}

func TestReadDBUnsupportedSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session_v2 (id TEXT PRIMARY KEY, data TEXT); CREATE TABLE session_message (id TEXT PRIMARY KEY, data TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, _, err = copyAndReadDB(t, path, "opencode")
	if err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("err = %v, want an unsupported-schema error", err)
	}
}

func TestReadMissingDB(t *testing.T) {
	root, data := fixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	path := filepath.Join(data, "nope.db")
	if _, err := New().Read(context.Background(), env, adapters.Source{Adapter: "opencode", Path: path, Files: []string{path}}); err == nil {
		t.Fatal("want an error for a missing database")
	}
}

// legacySource returns the Source that Sources lists for a legacy session file.
func legacySource(t *testing.T, env adapters.Env, path string) adapters.Source {
	t.Helper()
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		if s.Path == path {
			return s
		}
	}
	t.Fatalf("no source for %s", path)
	return adapters.Source{}
}

// Whole-file JSON modified in the last 5 s may still be being written (spec
// §4.3): the legacy source is retried next sync (adapters.ErrRetryLater),
// whether the fresh file parses or not.
func TestLegacyRecentFileIsRetried(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("part", "msg_l2", "prt_l2e.json"),        // torn
		filepath.Join("part", "msg_l2", "prt_l2c.json"),        // valid
		filepath.Join("message", "ses_legacy1", "msg_l1.json"), // valid message
		filepath.Join("session", "global", "ses_legacy1.json"), // the session file
	} {
		t.Run(filepath.Base(rel), func(t *testing.T) {
			root, data := fixture(t)
			env := adapterstest.FixtureEnv(t, root, nil)
			storage := filepath.Join(data, "storage")
			now := time.Now()
			if err := os.Chtimes(filepath.Join(storage, rel), now, now); err != nil {
				t.Fatal(err)
			}
			src := legacySource(t, env, filepath.Join(storage, "session", "global", "ses_legacy1.json"))
			_, err := New().Read(context.Background(), env, src)
			if !errors.Is(err, adapters.ErrRetryLater) {
				t.Fatalf("err = %v, want ErrRetryLater", err)
			}
		})
	}
}

// An old legacy file that does not parse is a skipped record, not an error.
func TestLegacyOldTornFileIsSkipped(t *testing.T) {
	root, data := fixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	src := legacySource(t, env, filepath.Join(data, "storage", "session", "global", "ses_legacy1.json"))
	res, err := New().Read(context.Background(), env, src)
	if err != nil || res.Skipped != 1 || len(res.Sessions) != 1 {
		t.Fatalf("Read = %d sessions, %d skipped, %v", len(res.Sessions), res.Skipped, err)
	}
}

func TestLegacyChildSessionSkipped(t *testing.T) {
	root, data := fixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	path := filepath.Join(data, "storage", "session", "0123abcd", "ses_legacy2.json")
	res, err := New().Read(context.Background(), env, adapters.Source{Adapter: "opencode", Path: path, Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 0 || res.Skipped != 0 {
		t.Fatalf("child session read as %d sessions, %d skipped", len(res.Sessions), res.Skipped)
	}
}

func TestToolSummary(t *testing.T) {
	long := strings.Repeat("x", 300)
	cases := []struct {
		tool, input, want string
	}{
		{"bash", `{"command":"go build ./...","description":"Build"}`, "bash: go build ./..."},
		{"read", `{"filePath":"C:/a/b.go","offset":3}`, "read: C:/a/b.go"},
		{"grep", `{"pattern":"TODO","path":"C:/a"}`, "grep: TODO"},
		{"webfetch", `{"url":"https://example.com"}`, "webfetch: https://example.com"},
		{"websearch", `{"query":"go generics"}`, "websearch: go generics"},
		{"task", `{"description":"Find callers","prompt":"find every caller","subagent_type":"general"}`, "task: Find callers"},
		{"todowrite", `{"todos":[{"id":"1","content":"a"}]}`, `todowrite: {"todos":[{"id":"1","content":"a"}]}`},
		{"custom", `{"blob":"` + long + `"}`, "custom: " + (`{"blob":"` + long)[:200]},
		{"noinput", ``, "noinput"},
		{"emptyinput", `{}`, "emptyinput"},
	}
	for _, c := range cases {
		if got := toolSummary(c.tool, []byte(c.input)); got != c.want {
			t.Errorf("toolSummary(%s, %s) = %q, want %q", c.tool, c.input, got, c.want)
		}
	}
}

func TestTruncateKeepsRunes(t *testing.T) {
	s := strings.Repeat("ب", 2000) // 2 bytes each: 4000 bytes
	got := truncate(s, model.MaxToolOutput)
	if len(got) > model.MaxToolOutput || !strings.HasPrefix(s, got) || len(got) < model.MaxToolOutput-1 {
		t.Fatalf("truncate gave %d bytes", len(got))
	}
	for _, r := range got {
		if r != 'ب' {
			t.Fatalf("broken rune in %q", got[len(got)-4:])
		}
	}
	if truncate("short", 10) != "short" {
		t.Fatal("short strings must be kept")
	}
}

func TestDefaultTitle(t *testing.T) {
	for title, want := range map[string]bool{
		"New session - 2026-07-13T08:00:00.000Z":   true,
		"Child session - 2026-07-13T08:00:00.000Z": true,
		"New session - 2026-07-13T08:00:00Z":       true,
		"":                                         true,
		"   ":                                      true,
		"New session about parsers":                false,
		"Fix login":                                false,
	} {
		if got := isDefaultTitle(title); got != want {
			t.Errorf("isDefaultTitle(%q) = %v, want %v", title, got, want)
		}
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
