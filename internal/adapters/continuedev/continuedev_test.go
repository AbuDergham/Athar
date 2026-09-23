package continuedev

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

const (
	idFull   = "11111111-1111-4111-8111-111111111111"
	idPrompt = "22222222-2222-4222-8222-222222222222"
	idEmpty  = "33333333-3333-4333-8333-333333333333"
	idPlain  = "44444444-4444-4444-8444-444444444444"
	idAlt    = "66666666-6666-4666-8666-666666666666"
)

// later is an adapter whose clock runs an hour ahead, so freshly checked-out
// fixtures are never "modified within the last 5 s".
func later() adapter {
	return adapter{now: func() time.Time { return time.Now().Add(time.Hour) }}
}

func fixtureEnv(t *testing.T, getenv map[string]string) adapters.Env {
	t.Helper()
	root, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	return adapterstest.FixtureEnv(t, root, getenv)
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

func mtimeUTC(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime().UTC()
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "continue" || a.Name() != "Continue" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestSources(t *testing.T) {
	env := fixtureEnv(t, nil)
	srcs, err := later().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range srcs {
		if s.Adapter != "continue" {
			t.Errorf("%s: Adapter = %q", s.Path, s.Adapter)
		}
		if len(s.Files) != 1 || s.Files[0] != s.Path {
			t.Errorf("%s: Files = %v, want [Path]", s.Path, s.Files)
		}
		if s.Root != "" {
			t.Errorf("%s: Root = %q, want default", s.Path, s.Root)
		}
		names = append(names, filepath.Base(s.Path))
	}
	want := []string{idFull + ".json", idPrompt + ".json", idEmpty + ".json", idPlain + ".json", "notes.json"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("sources = %v, want %v (sessions.json is the index, not a source)", names, want)
	}
}

func TestSourcesMissingRoot(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := later().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on an empty home = %v, %v; want none, nil", srcs, err)
	}
}

func TestReadFixtures(t *testing.T) {
	env := fixtureEnv(t, nil)
	sessions, skipped := adapterstest.ReadAll(t, later(), env)
	got := byID(t, sessions)
	if len(got) != 3 {
		t.Fatalf("got %d sessions %v, want 3 (empty history and non-session files dropped)", len(got), keys(got))
	}
	if _, ok := got[idEmpty]; ok {
		t.Errorf("session with an empty history must be skipped")
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 (a non-object message and an object content)", skipped)
	}
	dir := filepath.Join(env.Home, ".continue", "sessions")

	t.Run("full session", func(t *testing.T) {
		s := got[idFull]
		if s.Agent != "continue" || s.Origin != model.OriginLive || s.Coverage != model.CoverageFull || s.Root != "" {
			t.Errorf("agent/origin/coverage/root = %q %q %q %q", s.Agent, s.Origin, s.Coverage, s.Root)
		}
		if s.Title != "Add retry to the uploader" || s.TitleSource != model.TitleAgent {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if want := `D:\work\alpha project`; s.Cwd != want {
			t.Errorf("Cwd = %q, want %q", s.Cwd, want)
		}
		if want := time.UnixMilli(1774000000000).UTC(); !s.StartedAt.Equal(want) || s.StartedAt.Location() != time.UTC {
			t.Errorf("StartedAt = %v, want %v (index dateCreated, UTC)", s.StartedAt, want)
		}
		if want := mtimeUTC(t, filepath.Join(dir, idFull+".json")); !s.UpdatedAt.Equal(want) || s.UpdatedAt.Location() != time.UTC {
			t.Errorf("UpdatedAt = %v, want file mtime %v", s.UpdatedAt, want)
		}
		want := []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, Text: "Please add retry logic to the uploader"},
			{Role: model.RoleAssistant, Kind: model.KindText, Text: "I will read the file first."},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, Text: "read_file: src/uploader.go"},
			{Role: model.RoleTool, Kind: model.KindToolOutput, Text: "package upload\n\nfunc Upload() error { return nil }"},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, Text: "run_terminal_command: go test ./..."},
			{Role: model.RoleTool, Kind: model.KindToolOutput, Text: "ok  example/upload 0.01s"},
			{Role: model.RoleUser, Kind: model.KindText, Text: "Now make it retry three times"},
			{Role: model.RoleAssistant, Kind: model.KindText, Text: "Done: the uploader now retries three times."},
			{Role: model.RoleUser, Kind: model.KindText, Text: "[image]\nDoes this screenshot look right?"},
			{Role: model.RoleAssistant, Kind: model.KindText, Text: "Yes, it looks right."},
		}
		assertMessages(t, s.Messages, want)
		for _, m := range s.Messages {
			for _, marker := range []string{"THINKING_MARKER", "SYSTEM_MARKER", "CONTEXT_ONLY_MARKER", "PROMPTLOG_MARKER"} {
				if strings.Contains(m.Text, marker) {
					t.Errorf("message %q leaks %s", m.Text, marker)
				}
			}
		}
	})

	t.Run("default title and no workspace", func(t *testing.T) {
		s := got[idPrompt]
		if want := "كيف أرتب الملفات حسب التاريخ؟"; s.Title != want || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s), want %q from the first prompt", s.Title, s.TitleSource, want)
		}
		if s.Cwd != "" {
			t.Errorf("Cwd = %q, want empty", s.Cwd)
		}
		// Not in the index: StartedAt falls back to the file times, never after UpdatedAt.
		if s.StartedAt.IsZero() || s.StartedAt.After(s.UpdatedAt) {
			t.Errorf("StartedAt = %v, UpdatedAt = %v", s.StartedAt, s.UpdatedAt)
		}
		if len(s.Messages) != 2 || s.Messages[0].Role != model.RoleUser || s.Messages[1].Role != model.RoleAssistant {
			t.Errorf("messages = %+v", s.Messages)
		}
	})

	t.Run("plain path and bad records", func(t *testing.T) {
		s := got[idPlain]
		if s.Cwd != `D:\plain\path` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if want := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC); !s.StartedAt.Equal(want) {
			t.Errorf("StartedAt = %v, want %v (ISO dateCreated)", s.StartedAt, want)
		}
		assertMessages(t, s.Messages, []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, Text: "hello plain"},
			{Role: model.RoleAssistant, Kind: model.KindText, Text: "hi there"},
		})
	})
}

func TestReadWarnsOnNonSessionJSON(t *testing.T) {
	env := fixtureEnv(t, nil)
	p := filepath.Join(env.Home, ".continue", "sessions", "notes.json")
	res, err := later().Read(context.Background(), env, adapters.Source{Adapter: "continue", Path: p, Files: []string{p}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 0 || len(res.Warnings) != 1 {
		t.Fatalf("notes.json: sessions=%d warnings=%v; want 0 sessions and one warning", len(res.Sessions), res.Warnings)
	}
}

func TestCustomGlobalDir(t *testing.T) {
	alt, err := filepath.Abs(filepath.Join("testdata", "alt-global"))
	if err != nil {
		t.Fatal(err)
	}
	env := fixtureEnv(t, map[string]string{"CONTINUE_GLOBAL_DIR": alt})
	sessions, _ := adapterstest.ReadAll(t, later(), env)
	got := byID(t, sessions)
	if len(got) != 4 {
		t.Fatalf("got %d sessions, want 4 (3 default root + 1 custom root)", len(got))
	}
	s := got[idAlt]
	if s.Root != alt {
		t.Errorf("Root = %q, want %q", s.Root, alt)
	}
	if got[idFull].Root != "" {
		t.Errorf("default-root session Root = %q, want empty", got[idFull].Root)
	}
	if s.Cwd != `\\fileserver\share\beta` {
		t.Errorf("Cwd = %q, want UNC path", s.Cwd)
	}
	if want := time.UnixMilli(1775000000000).UTC(); !s.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %v, want %v", s.StartedAt, want)
	}
	if s.Title != "Session in a custom global dir" || s.TitleSource != model.TitleAgent {
		t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
	}
}

func TestCustomGlobalDirSameAsDefaultOrRelative(t *testing.T) {
	for _, v := range []string{"", "relative\\dir"} {
		env := fixtureEnv(t, map[string]string{"CONTINUE_GLOBAL_DIR": v})
		if v == "" {
			env.Getenv = func(k string) string {
				if k == "CONTINUE_GLOBAL_DIR" {
					return filepath.Join(env.Home, ".continue") + string(os.PathSeparator)
				}
				return ""
			}
		}
		srcs, err := later().Sources(env)
		if err != nil {
			t.Fatal(err)
		}
		if len(srcs) != 5 {
			t.Errorf("CONTINUE_GLOBAL_DIR=%q: %d sources, want the 5 of the default root only", v, len(srcs))
		}
		for _, s := range srcs {
			if s.Root != "" {
				t.Errorf("CONTINUE_GLOBAL_DIR=%q: Root = %q, want default", v, s.Root)
			}
		}
	}
}

// writeSession writes a synthetic session file under <home>/.continue/sessions
// in a temp tree and returns the env and the file path.
func writeSession(t *testing.T, id string, session any, mtime time.Time) (adapters.Env, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "home", ".continue", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return adapterstest.FixtureEnv(t, root, nil), p
}

func simpleSession(id string) map[string]any {
	return map[string]any{
		"sessionId":          id,
		"title":              "Synthetic",
		"workspaceDirectory": "file:///c%3A/tmp/x",
		"history": []any{
			map[string]any{"message": map[string]any{"role": "user", "content": "hi"}, "contextItems": []any{}},
			map[string]any{"message": map[string]any{"role": "assistant", "content": "hello"}, "contextItems": []any{}},
		},
	}
}

func TestLargeRecordAndToolOutputTruncation(t *testing.T) {
	const id = "77777777-7777-4777-8777-777777777777"
	big := strings.Repeat("large assistant answer ", 2<<20/23+1) // > 2 MB in one message
	toolOut := "x" + strings.Repeat("é", 3000)                   // 6001 bytes, odd offset for rune boundaries
	session := map[string]any{
		"sessionId":          id,
		"title":              "Big one",
		"workspaceDirectory": "file:///c%3A/tmp/big",
		"history": []any{
			map[string]any{"message": map[string]any{"role": "user", "content": "give me a long answer"}},
			map[string]any{"message": map[string]any{"role": "assistant", "content": big}},
			map[string]any{"message": map[string]any{"role": "assistant", "content": "",
				"toolCalls": []any{map[string]any{"id": "c1", "type": "function",
					"function": map[string]any{"name": "grep_search", "arguments": `{"query":"needle"}`}}}}},
			map[string]any{"message": map[string]any{"role": "tool", "content": toolOut, "toolCallId": "c1"}},
		},
	}
	env, _ := writeSession(t, id, session, time.Now().Add(-time.Hour))
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if len(sessions) != 1 || skipped != 0 {
		t.Fatalf("sessions=%d skipped=%d", len(sessions), skipped)
	}
	msgs := sessions[0].Messages
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}
	if msgs[1].Text != big {
		t.Errorf("2 MB assistant message not kept whole (len %d)", len(msgs[1].Text))
	}
	if msgs[2].Kind != model.KindToolCall || msgs[2].Text != "grep_search: needle" {
		t.Errorf("tool call = %+v", msgs[2])
	}
	out := msgs[3]
	if out.Kind != model.KindToolOutput || len(out.Text) > model.MaxToolOutput || len(out.Text) < model.MaxToolOutput-4 {
		t.Errorf("tool output kind %s len %d, want <= %d", out.Kind, len(out.Text), model.MaxToolOutput)
	}
	if !utf8.ValidString(out.Text) || !strings.HasPrefix(toolOut, out.Text) {
		t.Errorf("tool output must be a valid UTF-8 prefix")
	}
}

func TestRecentlyModifiedIsRetriedLater(t *testing.T) {
	const id = "88888888-8888-4888-8888-888888888888"
	env, p := writeSession(t, id, simpleSession(id), time.Time{}) // mtime = now
	src := adapters.Source{Adapter: "continue", Path: p, Files: []string{p}}
	if _, err := New().Read(context.Background(), env, src); err == nil {
		t.Fatal("a file modified within the last 5 s must fail the read so the next sync retries it")
	}
	res, err := later().Read(context.Background(), env, src)
	if err != nil || len(res.Sessions) != 1 {
		t.Fatalf("after 5 s: sessions=%d err=%v", len(res.Sessions), err)
	}
	// A timestamp in the future (clock skew) must not block the file forever.
	future := time.Now().Add(24 * time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if res, err := New().Read(context.Background(), env, src); err != nil || len(res.Sessions) != 1 {
		t.Fatalf("future mtime: sessions=%d err=%v", len(res.Sessions), err)
	}
}

func TestTornFileIsAnError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "home", ".continue", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "99999999-9999-4999-8999-999999999999.json")
	if err := os.WriteFile(p, []byte(`{"sessionId":"99999999-9999-4999-8999-999999999999","history":[{"mess`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	env := adapterstest.FixtureEnv(t, root, nil)
	if _, err := New().Read(context.Background(), env, adapters.Source{Adapter: "continue", Path: p, Files: []string{p}}); err == nil {
		t.Fatal("invalid JSON must fail the source")
	}
}

func TestFieldOfUnexpectedTypeIsTolerated(t *testing.T) {
	const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	s := simpleSession(id)
	s["title"] = 42 // valid JSON, wrong type
	env, p := writeSession(t, id, s, time.Now().Add(-time.Hour))
	res, err := New().Read(context.Background(), env, adapters.Source{Adapter: "continue", Path: p, Files: []string{p}})
	if err != nil {
		t.Fatalf("a mistyped field must not fail the whole file: %v", err)
	}
	if len(res.Sessions) != 1 || res.Skipped != 1 {
		t.Fatalf("sessions=%d skipped=%d, want 1 and 1", len(res.Sessions), res.Skipped)
	}
	if got := res.Sessions[0]; got.Title != "hi" || got.TitleSource != model.TitlePrompt || len(got.Messages) != 2 {
		t.Errorf("title %q (%s), %d messages", got.Title, got.TitleSource, len(got.Messages))
	}
}

func TestTopLevelArrayIsNotASession(t *testing.T) {
	const id = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	env, p := writeSession(t, id, []any{1, 2}, time.Now().Add(-time.Hour))
	res, err := New().Read(context.Background(), env, adapters.Source{Adapter: "continue", Path: p, Files: []string{p}})
	if err != nil || len(res.Sessions) != 0 || len(res.Warnings) != 1 {
		t.Fatalf("err=%v sessions=%d warnings=%v; want nil, 0 and one warning", err, len(res.Sessions), res.Warnings)
	}
}

func TestReadOnlyTowardFixtures(t *testing.T) {
	root, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, root)
	alt := filepath.Join(root, "alt-global")
	env := fixtureEnv(t, map[string]string{"CONTINUE_GLOBAL_DIR": alt})
	adapterstest.ReadAll(t, later(), env)
	after := snapshot(t, root)
	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	for p, b := range before {
		if a, ok := after[p]; !ok || a != b {
			t.Errorf("%s changed: %q -> %q", p, b, a)
		}
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[p] = "dir"
			return nil
		}
		out[p] = fi.ModTime().UTC().Format(time.RFC3339Nano) + "|" + itoa(fi.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if c.Resume != nil || c.Fork != nil || c.SkipPermissions != nil {
		t.Errorf("Continue has no terminal resume: %+v", c)
	}
	if c.RootEnv != "CONTINUE_GLOBAL_DIR" {
		t.Errorf("RootEnv = %q", c.RootEnv)
	}
	if c.App == nil || c.App.Name != "VS Code" || c.App.Program != "code" || len(c.App.Args) != 1 || c.App.Args[0] != "{cwd}" {
		t.Fatalf("App = %+v", c.App)
	}
	found := false
	for _, p := range c.App.Candidates {
		if p == `%LOCALAPPDATA%\Programs\Microsoft VS Code\bin\code.cmd` {
			found = true
		}
	}
	if !found {
		t.Errorf("Candidates = %v, want the per-user VS Code install", c.App.Candidates)
	}
}

func TestWorkspacePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"file:///d%3A/work/alpha%20project", `D:\work\alpha project`},
		{"file:///c:/Users/someone/proj", `C:\Users\someone\proj`},
		{"file:///D:/work/trailing/", `D:\work\trailing`},
		{"file:///d%3A/", `D:\`},
		{"file:///d%3A/a%23b%25c", `D:\a#b%c`},
		{"file://fileserver/share/beta", `\\fileserver\share\beta`},
		{"file:///home/someone/proj", "/home/someone/proj"},
		{`D:\plain\path`, `D:\plain\path`},
		{"vscode-remote://ssh-remote%2Bbox/home/someone/proj", "vscode-remote://ssh-remote%2Bbox/home/someone/proj"},
		{"file:///d%3A/bad%zzescape", `D:\bad%zzescape`},
	}
	for _, c := range cases {
		if got := workspacePath(c.in); got != c.want {
			t.Errorf("workspacePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseDateCreated(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"1774000000000", time.UnixMilli(1774000000000).UTC()},
		{" 1774000000000 ", time.UnixMilli(1774000000000).UTC()},
		{"1774000000", time.Unix(1774000000, 0).UTC()},
		{"2026-02-01T10:00:00.000Z", time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)},
		{"2026-02-01T12:00:00+02:00", time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)},
		{"", time.Time{}},
		{"yesterday", time.Time{}},
		{"0", time.Time{}},
	}
	for _, c := range cases {
		got := parseDateCreated(c.in)
		if !got.Equal(c.want) || (!got.IsZero() && got.Location() != time.UTC) {
			t.Errorf("parseDateCreated(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPromptTitle(t *testing.T) {
	long := strings.Repeat("ab ", 60)
	got := promptTitle(long)
	if utf8.RuneCountInString(got) != 100 {
		t.Errorf("promptTitle keeps %d runes, want 100", utf8.RuneCountInString(got))
	}
	if got := promptTitle("  a\n\tb   c "); got != "a b c" {
		t.Errorf("promptTitle collapses whitespace: %q", got)
	}
}

func TestToolCallText(t *testing.T) {
	cases := []struct {
		name, args string
		parsed     map[string]any
		want       string
	}{
		{"run_terminal_command", `{"command":"go vet ./..."}`, nil, "run_terminal_command: go vet ./..."},
		{"read_file", `{"filepath":"a/b.go"}`, nil, "read_file: a/b.go"},
		{"file_glob_search", `{"pattern":"**/*.go"}`, nil, "file_glob_search: **/*.go"},
		{"ls", `{"dirPath":"src","recursive":false}`, nil, "ls: src"},
		{"fetch_url_content", `{"url":"https://example.com/x"}`, nil, "fetch_url_content: https://example.com/x"},
		{"edit_existing_file", `{"filepath":"src/ma`, map[string]any{"filepath": "src/main.go"}, "edit_existing_file: src/main.go"},
		{"edit_existing_file", `{"filepath":"src/ma`, nil, `edit_existing_file: {"filepath":"src/ma`},
		{"view_diff", `{}`, nil, "view_diff"},
		{"view_diff", ``, nil, "view_diff"},
		{"custom_tool", `{"alpha":1,"beta":"two"}`, nil, `custom_tool: {"alpha":1,"beta":"two"}`},
	}
	for _, c := range cases {
		var parsed map[string]json.RawMessage
		if c.parsed != nil {
			b, _ := json.Marshal(c.parsed)
			_ = json.Unmarshal(b, &parsed)
		}
		if got := toolCallText(c.name, c.args, parsed); got != c.want {
			t.Errorf("toolCallText(%q, %q) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
	long := toolCallText("custom_tool", `{"blob":"`+strings.Repeat("z", 500)+`"}`, nil)
	if n := utf8.RuneCountInString(strings.TrimPrefix(long, "custom_tool: ")); n != 200 {
		t.Errorf("unknown args are cut to 200 runes, got %d", n)
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }

func assertMessages(t *testing.T, got, want []model.Message) {
	t.Helper()
	if len(got) != len(want) {
		for i, m := range got {
			t.Logf("got[%d] = %s/%s %q", i, m.Role, m.Kind, m.Text)
		}
		t.Fatalf("got %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Role != w.Role || g.Kind != w.Kind || g.Text != w.Text || !g.At.IsZero() {
			t.Errorf("message %d = %s/%s %q (at %v), want %s/%s %q", i, g.Role, g.Kind, g.Text, g.At, w.Role, w.Kind, w.Text)
		}
	}
}

func keys(m map[string]model.Session) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
