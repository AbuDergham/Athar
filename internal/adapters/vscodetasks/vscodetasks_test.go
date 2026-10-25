package vscodetasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

const (
	idFull   = "1767225600000"                        // Cline, XML tools, in taskHistory.json
	idUIOnly = "1767225900000"                        // Cline, no API history
	idEmpty  = "1767226000000"                        // Cline, empty files
	idNative = "1767312000000"                        // Cline in Cursor, native tools, no index
	idKilo   = "019b76da-a800-7000-8000-000000000001" // Kilo legacy in Antigravity
	idSDK    = "sess_abc"
)

// fixture folders, with the separator of the OS the tests run on
var (
	codeTasks = filepath.FromSlash("Code/User/globalStorage/saoudrizwan.claude-dev/tasks")
	kiloTasks = filepath.FromSlash("Antigravity/User/globalStorage/kilocode.kilo-code/tasks")
)

// later returns an adapter whose clock runs an hour ahead, so freshly
// written fixtures are never "modified within the last 5 s".
func later(f flavor) *adapter {
	return &adapter{f: f, now: func() time.Time { return time.Now().Add(time.Hour) }}
}

func fixtureEnv(t *testing.T) adapters.Env {
	t.Helper()
	root, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	return adapterstest.FixtureEnv(t, root, nil)
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

func ms(n int64) time.Time { return time.UnixMilli(n).UTC() }

type wantMsg struct {
	role model.Role
	kind model.Kind
	text string
}

func checkMessages(t *testing.T, got []model.Message, want []wantMsg) {
	t.Helper()
	for i := 0; i < len(got) || i < len(want); i++ {
		var g, w wantMsg
		if i < len(got) {
			g = wantMsg{got[i].Role, got[i].Kind, got[i].Text}
		}
		if i < len(want) {
			w = want[i]
		}
		if g != w {
			t.Errorf("message %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestIdentity(t *testing.T) {
	for _, c := range []struct {
		a    adapters.Adapter
		id   string
		name string
		app  string
	}{
		{NewCline(), "cline", "Cline", "code"},
		{NewKiloLegacy(), "kilo", "Kilo Code (legacy)", "antigravity"},
	} {
		if c.a.ID() != c.id || c.a.Name() != c.name || c.a.Version() < 1 {
			t.Errorf("identity = %q %q %d", c.a.ID(), c.a.Name(), c.a.Version())
		}
		caps := c.a.Caps()
		if caps.Resume != nil || caps.App == nil || caps.App.Program != c.app || strings.Join(caps.App.Args, " ") != "{cwd}" {
			t.Errorf("%s caps = %+v (app %+v)", c.id, caps, caps.App)
		}
	}
}

func TestSources(t *testing.T) {
	env := fixtureEnv(t)
	cases := []struct {
		f    flavor
		want []string // Path relative to the fixture root, kind, host
	}{
		{clineFlavor, []string{
			`appdata\` + codeTasks + `\` + idFull + ` task Code 2`,
			`appdata\` + codeTasks + `\` + idUIOnly + ` task Code 1`,
			`appdata\` + codeTasks + `\` + idEmpty + ` task Code 2`,
			`appdata\Cursor\User\globalStorage\saoudrizwan.claude-dev\tasks\` + idNative + ` task Cursor 1`,
			`home\.cline\data\sessions\sess_abc sdk cline-cli 2`,
			`home\.cline\data\sessions\sess_sub sdk cline-cli 2`,
		}},
		{kiloFlavor, []string{
			`appdata\` + kiloTasks + `\` + idKilo + ` task Antigravity 2`,
		}},
	}
	root := filepath.Dir(env.Home)
	for _, c := range cases {
		srcs, err := later(c.f).Sources(env)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, s := range srcs {
			if s.Adapter != c.f.id || s.Root != "" {
				t.Errorf("%s: Adapter %q Root %q", s.Path, s.Adapter, s.Root)
			}
			for _, f := range s.Files {
				if filepath.Dir(f) != s.Path {
					t.Errorf("%s: file %s outside the source folder", s.Path, f)
				}
			}
			rel, _ := filepath.Rel(root, s.Path)
			got = append(got, strings.Join([]string{filepath.FromSlash(rel), s.Extra[extraKind], s.Extra[extraHost], strconv.Itoa(len(s.Files))}, " "))
		}
		want := make([]string, len(c.want))
		for i, w := range c.want {
			want[i] = strings.ReplaceAll(w, `\`, string(filepath.Separator))
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s sources:\n%s\nwant:\n%s", c.f.id, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

func TestSourcesMissingRoots(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	for _, a := range []*adapter{later(clineFlavor), later(kiloFlavor)} {
		srcs, err := a.Sources(env)
		if err != nil || len(srcs) != 0 {
			t.Fatalf("%s Sources on an empty tree = %v, %v", a.ID(), srcs, err)
		}
	}
}

func TestReadCline(t *testing.T) {
	env := fixtureEnv(t)
	sessions, skipped := adapterstest.ReadAll(t, later(clineFlavor), env)
	got := byID(t, sessions)
	if len(got) != 4 {
		t.Fatalf("got %d sessions, want 4 (empty task and subagent session dropped)", len(got))
	}
	if _, ok := got[idEmpty]; ok {
		t.Error("a task with empty chat files must be skipped")
	}
	if _, ok := got["sess_sub"]; ok {
		t.Error("a subagent SDK session must be skipped")
	}
	// Skipped: the number entry and the object content (full task), the
	// non-object chat log entry (UI-only task).
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
	for _, s := range sessions {
		if s.Agent != "cline" || s.Origin != model.OriginLive || s.Root != "" {
			t.Errorf("%s: agent %q origin %q root %q", s.NativeID, s.Agent, s.Origin, s.Root)
		}
		for _, m := range s.Messages {
			if strings.Contains(m.Text, "hidden reasoning") || strings.Contains(m.Text, "<environment_details>") {
				t.Errorf("%s: message leaks reasoning or environment details: %q", s.NativeID, m.Text)
			}
		}
	}

	t.Run("XML tool protocol", func(t *testing.T) {
		s := got[idFull]
		if s.Cwd != `C:\work\todo` {
			t.Errorf("Cwd = %q, want the index cwdOnTaskInitialization", s.Cwd)
		}
		if s.Title != "Build a tiny todo CLI in Go" || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if s.Coverage != model.CoverageFull {
			t.Errorf("coverage = %s", s.Coverage)
		}
		if !s.StartedAt.Equal(ms(1767225600000)) || !s.UpdatedAt.Equal(ms(1767225800000)) {
			t.Errorf("times = %v .. %v, want id time .. last chat log / index ts", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []wantMsg{
			{model.RoleUser, model.KindText, "Build a tiny todo CLI in Go"},
			{model.RoleAssistant, model.KindText, "I will look at the folder first."},
			{model.RoleAssistant, model.KindToolCall, "list_files: ."},
			{model.RoleTool, model.KindToolOutput, "main.go\ngo.mod"},
			{model.RoleAssistant, model.KindToolCall, "execute_command: go test ./..."},
			{model.RoleTool, model.KindToolOutput, "The user denied this operation and provided the following feedback:"},
			{model.RoleUser, model.KindText, "Run it with -race please"},
			{model.RoleAssistant, model.KindText, "Which Go version should I target?"},
			{model.RoleUser, model.KindText, "1.23"},
			{model.RoleAssistant, model.KindText, "Writing the file now."},
			{model.RoleAssistant, model.KindToolCall, "write_to_file: main.go"},
			{model.RoleTool, model.KindToolOutput, "The content was successfully saved to main.go."},
			{model.RoleAssistant, model.KindText, "The todo CLI is ready."},
			{model.RoleUser, model.KindText, "Add a README that links 'docs/notes.md' (see below for file content)"},
			{model.RoleAssistant, model.KindText, "Sure."},
			{model.RoleUser, model.KindText, "Also add a Makefile"},
		})
		if len(s.Messages) == 16 {
			if !s.Messages[0].At.Equal(ms(1767225601000)) || !s.Messages[14].At.IsZero() || !s.Messages[15].At.Equal(ms(1767225700000)) {
				t.Errorf("message times = %v, %v, %v", s.Messages[0].At, s.Messages[14].At, s.Messages[15].At)
			}
		}
	})

	t.Run("chat log only", func(t *testing.T) {
		s := got[idUIOnly]
		if s.Coverage != model.CoveragePartial || s.Cwd != "" {
			t.Errorf("coverage %s cwd %q", s.Coverage, s.Cwd)
		}
		if s.Title != "Explain the build error" || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1767225900000)) || !s.UpdatedAt.Equal(ms(1767225900600)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []wantMsg{
			{model.RoleUser, model.KindText, "Explain the build error"},
			{model.RoleAssistant, model.KindText, "The build fails because a package is missing."},
			{model.RoleAssistant, model.KindText, "Shall I fix it?"},
			{model.RoleUser, model.KindText, "Yes"},
			{model.RoleAssistant, model.KindToolCall, "execute_command: go build ./..."},
		})
	})

	t.Run("native tools in another host", func(t *testing.T) {
		s := got[idNative]
		if s.Cwd != `c:\work\cursor-proj` {
			t.Errorf("Cwd = %q, want the environment details folder", s.Cwd)
		}
		if s.Title != "Fix the flaky test" {
			t.Errorf("title = %q", s.Title)
		}
		api := filepath.Join(env.AppData, filepath.FromSlash("Cursor/User/globalStorage/saoudrizwan.claude-dev/tasks"), idNative, apiFile)
		fi, err := os.Stat(api)
		if err != nil {
			t.Fatal(err)
		}
		if !s.StartedAt.Equal(ms(1767312000000)) || !s.UpdatedAt.Equal(fi.ModTime().UTC()) {
			t.Errorf("times = %v .. %v, want id time .. file mtime", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []wantMsg{
			{model.RoleUser, model.KindText, "Fix the flaky test"},
			{model.RoleAssistant, model.KindText, "Reading it."},
			{model.RoleAssistant, model.KindToolCall, "read_file: a_test.go b_test.go"},
			{model.RoleTool, model.KindToolOutput, "package a"},
			{model.RoleAssistant, model.KindText, "How many retries?"},
			{model.RoleUser, model.KindText, "three"},
			{model.RoleAssistant, model.KindToolCall, `update_todo_list: {"todos":"[x] fix"}`},
			{model.RoleTool, model.KindToolOutput, "ok"},
			{model.RoleAssistant, model.KindText, "Fixed."},
		})
	})

	t.Run("SDK session", func(t *testing.T) {
		s := got[idSDK]
		if s.Cwd != `C:\work\sdk` || s.Title != "Summarize the repo" || s.TitleSource != model.TitlePrompt || s.Coverage != model.CoverageFull {
			t.Errorf("session = %q %q (%s) %s", s.Cwd, s.Title, s.TitleSource, s.Coverage)
		}
		start, _ := time.Parse(time.RFC3339, "2026-01-02T10:00:00Z")
		end, _ := time.Parse(time.RFC3339, "2026-01-02T10:05:00Z")
		if !s.StartedAt.Equal(start) || !s.UpdatedAt.Equal(end) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []wantMsg{
			{model.RoleUser, model.KindText, "Summarize the repo"},
			{model.RoleAssistant, model.KindText, "It is a Go CLI."},
			{model.RoleAssistant, model.KindToolCall, "run_commands: go list ./..."},
			{model.RoleTool, model.KindToolOutput, "example.com/app"},
		})
	})
}

// copyTree copies the fixture tree to a temp folder, so tests can add files.
func copyTree(t *testing.T, from string) string {
	t.Helper()
	to := t.TempDir()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return to
}

// writeStateDB creates a host state.vscdb whose ItemTable holds value under key.
func writeStateDB(t *testing.T, path, key string, value any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	data, _ := json.Marshal(value)
	for _, q := range []string{
		`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`,
		`INSERT INTO ItemTable VALUES ('other.extension', '{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO ItemTable VALUES (?, ?)`, key, data); err != nil {
		t.Fatal(err)
	}
}

func TestReadKiloLegacy(t *testing.T) {
	kiloWant := []wantMsg{
		{model.RoleUser, model.KindText, "Make the page responsive"},
		{model.RoleAssistant, model.KindText, "Let me check."},
		{model.RoleAssistant, model.KindToolCall, "edit_file: index.html"},
		{model.RoleTool, model.KindToolOutput, "File edited"},
		{model.RoleAssistant, model.KindText, "Done."},
		{model.RoleUser, model.KindText, "Also the footer"},
	}

	t.Run("no index: environment details", func(t *testing.T) {
		sessions, skipped := adapterstest.ReadAll(t, later(kiloFlavor), fixtureEnv(t))
		if len(sessions) != 1 || skipped != 0 {
			t.Fatalf("got %d sessions, %d skipped", len(sessions), skipped)
		}
		s := sessions[0]
		if s.Agent != "kilo" || s.NativeID != idKilo || s.Cwd != `c:\work\site` || s.Title != "Make the page responsive" {
			t.Errorf("session = %q %q %q %q", s.Agent, s.NativeID, s.Cwd, s.Title)
		}
		if !s.StartedAt.Equal(ms(1767225600000)) || !s.UpdatedAt.Equal(ms(1767225606000)) {
			t.Errorf("times = %v .. %v, want UUIDv7 time .. last chat log ts", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, kiloWant)
	})

	t.Run("host state.vscdb index", func(t *testing.T) {
		root := copyTree(t, "testdata")
		env := adapterstest.FixtureEnv(t, root, nil)
		gs := filepath.Join(env.AppData, filepath.FromSlash("Antigravity/User/globalStorage"))
		db := filepath.Join(gs, "state.vscdb")
		writeStateDB(t, db, kiloExt, map[string]any{
			"mode": "code",
			"taskHistory": []map[string]any{{
				"id": idKilo, "number": 1, "ts": 1767225700000, "task": "Make the page\nresponsive please",
				"workspace": `C:\work\site`, "mode": "code",
			}},
		})
		before := snapshot(t, gs)
		a := later(kiloFlavor)
		sessions, _ := adapterstest.ReadAll(t, a, env)
		if len(sessions) != 1 {
			t.Fatalf("got %d sessions", len(sessions))
		}
		s := sessions[0]
		if s.Cwd != `C:\work\site` || s.Title != "Make the page responsive please" || s.TitleSource != model.TitlePrompt {
			t.Errorf("session = %q %q (%s), want the index values", s.Cwd, s.Title, s.TitleSource)
		}
		if !s.UpdatedAt.Equal(ms(1767225700000)) {
			t.Errorf("UpdatedAt = %v, want the index ts (latest)", s.UpdatedAt)
		}
		checkMessages(t, s.Messages, kiloWant)
		if after := snapshot(t, gs); after != before {
			t.Errorf("reading changed the host folder:\n%s\nwant:\n%s", after, before)
		}
		// A second read uses the cached index (same fingerprint).
		if len(a.index) != 1 {
			t.Errorf("index cache has %d entries, want 1", len(a.index))
		}
		adapterstest.ReadAll(t, a, env)
	})
}

// snapshot lists (name, size, mtime) of the files in dir.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, _ := os.Stat(p) // Stat, not DirEntry.Info: the listing can be stale on Windows
		sb.WriteString(p + " " + strconv.FormatInt(fi.Size(), 10) + " " + fi.ModTime().String() + "\n")
		return nil
	})
	return sb.String()
}

func TestReadRetryLater(t *testing.T) {
	root := copyTree(t, "testdata")
	env := adapterstest.FixtureEnv(t, root, nil)
	dir := filepath.Join(env.AppData, codeTasks, idFull)
	src := adapters.Source{Adapter: "cline", Path: dir, Files: []string{filepath.Join(dir, apiFile)}, Extra: map[string]string{extraKind: kindTask}}
	a := &adapter{f: clineFlavor} // real clock: the copied files are brand new
	if _, err := a.Read(context.Background(), env, src); !errors.Is(err, adapters.ErrRetryLater) {
		t.Fatalf("Read of a just-written task = %v, want ErrRetryLater", err)
	}
	sdk := filepath.Join(env.Home, filepath.FromSlash(".cline/data/sessions"), idSDK)
	src = adapters.Source{Adapter: "cline", Path: sdk, Extra: map[string]string{extraKind: kindSDK}}
	if _, err := a.Read(context.Background(), env, src); !errors.Is(err, adapters.ErrRetryLater) {
		t.Fatalf("Read of a just-written SDK session = %v, want ErrRetryLater", err)
	}
}

func TestReadTornFiles(t *testing.T) {
	root := copyTree(t, "testdata")
	env := adapterstest.FixtureEnv(t, root, nil)
	dir := filepath.Join(env.AppData, codeTasks, idFull)
	src := adapters.Source{Adapter: "cline", Path: dir, Extra: map[string]string{extraKind: kindTask}}
	api := filepath.Join(dir, apiFile)
	if err := os.WriteFile(api, []byte(`[{"role":"user","content":"cut`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := later(clineFlavor)
	res, err := a.Read(context.Background(), env, src)
	if err != nil {
		t.Fatalf("torn API history with a chat log: %v", err)
	}
	if len(res.Warnings) != 1 || len(res.Sessions) != 1 || res.Sessions[0].Coverage != model.CoveragePartial {
		t.Fatalf("torn API history: warnings %v, sessions %d", res.Warnings, len(res.Sessions))
	}
	if s := res.Sessions[0]; s.Title != "Build a tiny todo CLI in Go" || len(s.Messages) != 1 {
		t.Errorf("fallback session = %q with %d messages", s.Title, len(s.Messages))
	}
	if err := os.Remove(filepath.Join(dir, uiFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(context.Background(), env, src); err == nil {
		t.Fatal("torn API history and no chat log: want an error")
	}
}

func TestReadBigRecord(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	dir := filepath.Join(env.AppData, codeTasks, "1767225600001")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("output line é\n", 2<<20/15)
	api := []map[string]any{
		{"role": "user", "content": "<task>\nbig\n</task>"},
		{"role": "assistant", "content": "<read_file>\n<path>big.txt</path>\n</read_file>"},
		{"role": "user", "content": []map[string]any{
			{"type": "text", "text": "[read_file for 'big.txt'] Result:"},
			{"type": "text", "text": big},
		}},
	}
	data, _ := json.Marshal(api)
	if len(data) < 2<<20 {
		t.Fatalf("fixture is %d bytes, want >= 2 MB", len(data))
	}
	if err := os.WriteFile(filepath.Join(dir, apiFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	sessions, _ := adapterstest.ReadAll(t, later(clineFlavor), env)
	if len(sessions) != 1 || len(sessions[0].Messages) != 3 {
		t.Fatalf("got %d sessions", len(sessions))
	}
	out := sessions[0].Messages[2]
	if out.Kind != model.KindToolOutput || len(out.Text) > model.MaxToolOutput || !strings.HasPrefix(out.Text, "output line é") {
		t.Errorf("tool output kind %s, %d bytes", out.Kind, len(out.Text))
	}
}

func TestRealData(t *testing.T) {
	adapterstest.RealData(t, NewCline())
	adapterstest.RealData(t, NewKiloLegacy())
}

func TestReadHistoryItem(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	dir := filepath.Join(env.AppData, "VSCodium", "User", "globalStorage", kiloExt, "tasks", "task-a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		apiFile:  `[{"role":"user","ts":1767225601000,"content":"<task>\nfirst prompt\n</task>"}]`,
		itemFile: `{"id":"task-a","number":3,"ts":1767225609000,"task":"Named from the item","workspace":"D:/work/item"}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := later(kiloFlavor)
	srcs, err := a.Sources(env)
	if err != nil || len(srcs) != 1 || len(srcs[0].Files) != 2 || srcs[0].Extra[extraHost] != "VSCodium" {
		t.Fatalf("Sources = %+v, %v", srcs, err)
	}
	sessions, _ := adapterstest.ReadAll(t, a, env)
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions", len(sessions))
	}
	s := sessions[0]
	if s.Title != "Named from the item" || s.Cwd != "D:/work/item" || !s.UpdatedAt.Equal(ms(1767225609000)) || !s.StartedAt.Equal(ms(1767225601000)) {
		t.Errorf("session = %q %q %v..%v", s.Title, s.Cwd, s.StartedAt, s.UpdatedAt)
	}
}
