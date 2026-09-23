package kilo

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

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

// createDB builds a WAL-mode database at path from SQL scripts in testdata and
// keeps the connection open until the test ends, so the rows stay in the -wal
// file (a reader that ignored the WAL would miss them).
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

// fixture returns a root whose home\.local\share\kilo holds kilo.db,
// kilo-beta.db and files that are not databases.
func fixture(t *testing.T) (root, data string) {
	t.Helper()
	root = t.TempDir()
	data = filepath.Join(root, "home", ".local", "share", "kilo")
	createDB(t, filepath.Join(data, "kilo.db"), "schema.sql", "kilo.sql")
	createDB(t, filepath.Join(data, "kilo-beta.db"), "schema.sql", "beta.sql")
	for _, d := range []string{"log", "bin", "repos"} {
		if err := os.MkdirAll(filepath.Join(data, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(data, "log", "2026-07-14.log"), []byte("synthetic log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "notes.txt"), []byte("not a database\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Fixed times for the non-database entries, so a late directory-time
	// update from writing them cannot look like a change made by Read.
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, p := range []string{"log/2026-07-14.log", "notes.txt", "log", "bin", "repos"} {
		if err := os.Chtimes(filepath.Join(data, filepath.FromSlash(p)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	return root, data
}

// snapshot records (path, size, mtime) of every file and folder under root.
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
		out[p] = fmt.Sprintf("%v|%d|%d", d.IsDir(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
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
	if a.ID() != "kilo" || a.Name() != "Kilo Code" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if !reflect.DeepEqual(c.Resume, []string{"kilo", "--session", "{id}"}) {
		t.Errorf("Resume = %q", c.Resume)
	}
	if c.RootEnv != "KILO_DB" {
		t.Errorf("RootEnv = %q", c.RootEnv)
	}
	if c.Fork != nil || c.SkipPermissions != nil || c.App != nil {
		t.Errorf("unexpected caps %+v", c)
	}
	for _, p := range c.ProgramCandidates {
		if !strings.HasPrefix(p, "%") {
			t.Errorf("candidate %q is not an env-relative install path", p)
		}
	}
}

func TestSources(t *testing.T) {
	root, data := fixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 2 {
		t.Fatalf("got %d sources: %+v", len(srcs), srcs)
	}
	beta, main := filepath.Join(data, "kilo-beta.db"), filepath.Join(data, "kilo.db")
	want := []adapters.Source{
		{Adapter: "kilo", Path: beta, Files: []string{beta, beta + "-wal", beta + "-shm"}, Root: beta},
		{Adapter: "kilo", Path: main, Files: []string{main, main + "-wal", main + "-shm"}, Root: ""},
	}
	for i := range want {
		got := srcs[i]
		got.Extra = nil
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("source %d =\n%+v\nwant\n%+v", i, got, want[i])
		}
	}
}

func TestSourcesMissingRootIsSilent(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources = %v, %v", srcs, err)
	}
}

// XDG_DATA_HOME\kilo is read next to the default folder; KILO_DB adds one
// database (absolute, or relative to Kilo's data folder); ":memory:" and
// missing files are ignored, and a database listed twice appears once.
func TestSourcesXDGAndKiloDB(t *testing.T) {
	root, data := fixture(t)
	xdg := filepath.Join(root, "xdg")
	xdgDB := filepath.Join(xdg, "kilo", "kilo.db")
	createDB(t, xdgDB, "schema.sql", "custom.sql")
	custom := filepath.Join(root, "elsewhere", "mine.db")
	createDB(t, custom, "schema.sql", "custom.sql")

	cases := []struct {
		name  string
		vars  map[string]string
		paths []string
	}{
		{"xdg", map[string]string{"XDG_DATA_HOME": xdg},
			[]string{xdgDB, filepath.Join(data, "kilo-beta.db"), filepath.Join(data, "kilo.db")}},
		{"absolute KILO_DB", map[string]string{"KILO_DB": custom},
			[]string{filepath.Join(data, "kilo-beta.db"), filepath.Join(data, "kilo.db"), custom}},
		{"relative KILO_DB", map[string]string{"KILO_DB": "kilo-beta.db"},
			[]string{filepath.Join(data, "kilo-beta.db"), filepath.Join(data, "kilo.db")}},
		{"memory KILO_DB", map[string]string{"KILO_DB": ":memory:"},
			[]string{filepath.Join(data, "kilo-beta.db"), filepath.Join(data, "kilo.db")}},
		{"missing KILO_DB", map[string]string{"KILO_DB": filepath.Join(root, "nope.db")},
			[]string{filepath.Join(data, "kilo-beta.db"), filepath.Join(data, "kilo.db")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := adapterstest.FixtureEnv(t, root, tc.vars)
			srcs, err := New().Sources(env)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, s := range srcs {
				got = append(got, s.Path)
				wantRoot := s.Path
				if s.Path == filepath.Join(data, "kilo.db") {
					wantRoot = ""
				}
				if s.Root != wantRoot {
					t.Errorf("%s: Root = %q, want %q", s.Path, s.Root, wantRoot)
				}
			}
			if !reflect.DeepEqual(got, tc.paths) {
				t.Errorf("paths =\n%q\nwant\n%q", got, tc.paths)
			}
		})
	}
}

// Kilo keeps using opencode-<channel>.db from its own data folder when no
// kilo-<channel>.db exists, so those are sources too.
func TestSourcesOpencodeNamedChannelDB(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "home", ".local", "share", "kilo")
	createDB(t, filepath.Join(data, "opencode-dev.db"), "schema.sql", "custom.sql")
	srcs, err := New().Sources(adapterstest.FixtureEnv(t, root, nil))
	if err != nil || len(srcs) != 1 || srcs[0].Path != filepath.Join(data, "opencode-dev.db") {
		t.Fatalf("Sources = %+v, %v", srcs, err)
	}
}

func TestReadAllGolden(t *testing.T) {
	root, _ := fixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the broken message)", skipped)
	}
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.NativeID)
	}
	if want := []string{"ses_b01", "ses_k01", "ses_k03"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("sessions = %q, want %q", ids, want)
	}

	type head struct {
		Agent, Cwd, Title string
		TitleSource       model.TitleSource
		Started, Updated  time.Time
		Coverage          model.Coverage
		Origin            model.Origin
		Root              string
	}
	beta := filepath.Join(root, "home", ".local", "share", "kilo", "kilo-beta.db")
	wantHeads := []head{
		{"kilo", filepath.FromSlash("C:/work/beta"), "Beta channel task", model.TitleAgent,
			ms(1784300000000), ms(1784300100000), model.CoverageFull, model.OriginLive, beta},
		{"kilo", filepath.FromSlash("C:/work/kilo-app"), "Add a retry to the fetch helper", model.TitleAgent,
			ms(1784019600000), ms(1784019660000), model.CoverageFull, model.OriginLive, ""},
		{"kilo", filepath.FromSlash("D:/notes"), "لخّص ملاحظات الاجتماع", model.TitlePrompt,
			ms(1784106000000), ms(1784106100000), model.CoverageFull, model.OriginLive, ""},
	}
	for i, s := range sessions {
		got := head{s.Agent, s.Cwd, s.Title, s.TitleSource, s.StartedAt, s.UpdatedAt, s.Coverage, s.Origin, s.Root}
		if !reflect.DeepEqual(got, wantHeads[i]) {
			t.Errorf("%s =\n%+v\nwant\n%+v", s.NativeID, got, wantHeads[i])
		}
	}

	wantMsgs := [][]string{
		{"user/text@1784300000000: try the beta build"},
		{
			"user/text@1784019600000: add a retry to fetchJSON",
			"assistant/tool_call@1784019612000: bash: go test ./...",
			"tool/tool_output@1784019615000: ok  example/fetch",
			"assistant/text@1784019650000: Added a retry with backoff.",
		},
		{
			"user/text@1784106000000: لخّص   ملاحظات الاجتماع",
			"assistant/text@1784106050000: Here is a summary.",
		},
	}
	for i, s := range sessions {
		if got := lines(s.Messages); !reflect.DeepEqual(got, wantMsgs[i]) {
			t.Errorf("%s messages =\n%s\nwant\n%s", s.NativeID, strings.Join(got, "\n"), strings.Join(wantMsgs[i], "\n"))
		}
	}
}

// Reading never changes anything in Kilo's folder and leaves no temp copies.
func TestReadIsReadOnlyAndCleansUp(t *testing.T) {
	root, data := fixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	before := snapshot(t, data)
	path := filepath.Join(data, "kilo.db")
	res, err := New().Read(context.Background(), env, adapters.Source{Adapter: "kilo", Path: path, Files: fsread.SQLiteSidecars(path)})
	if err != nil || len(res.Sessions) != 2 {
		t.Fatalf("Read = %d sessions, %v", len(res.Sessions), err)
	}
	if after := snapshot(t, data); !reflect.DeepEqual(before, after) {
		for p, b := range before {
			if a := after[p]; a != b {
				t.Errorf("reading changed %s: %s -> %s", p, b, a)
			}
		}
		for p, a := range after {
			if _, ok := before[p]; !ok {
				t.Errorf("reading created %s (%s)", p, a)
			}
		}
	}
	if ents, _ := os.ReadDir(env.TempDir); len(ents) != 0 {
		t.Errorf("temp copies left behind: %v", ents)
	}
}

// The owner's kilo.db holds a project but no session: that is not an error.
func TestReadEmptyDB(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "home", ".local", "share", "kilo", "kilo.db")
	createDB(t, path, "schema.sql")
	env := adapterstest.FixtureEnv(t, root, nil)
	res, err := New().Read(context.Background(), env, adapters.Source{Adapter: "kilo", Path: path, Files: fsread.SQLiteSidecars(path)})
	if err != nil || len(res.Sessions) != 0 || res.Skipped != 0 {
		t.Fatalf("Read = %+v, %v", res, err)
	}
}

func TestReadMissingDB(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	path := filepath.Join(env.Home, ".local", "share", "kilo", "kilo.db")
	if _, err := New().Read(context.Background(), env, adapters.Source{Adapter: "kilo", Path: path}); err == nil {
		t.Fatal("Read of a missing database succeeded")
	}
}

func TestReadUnsupportedSchema(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "home", ".local", "share", "kilo", "kilo.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE something_else (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	env := adapterstest.FixtureEnv(t, root, nil)
	_, err = New().Read(context.Background(), env, adapters.Source{Adapter: "kilo", Path: path})
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want an error naming the database", err)
	}
}

func TestReadCancelled(t *testing.T) {
	root, data := fixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().Read(ctx, env, adapters.Source{Adapter: "kilo", Path: filepath.Join(data, "kilo.db")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
