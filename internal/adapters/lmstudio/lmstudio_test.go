package lmstudio

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

// testAdapter returns an adapter whose clock is far in the future, so the
// "changed less than 5 s ago" rule never triggers on freshly checked-out fixtures.
func testAdapter() adapter {
	return adapter{now: func() time.Time { return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }}
}

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "lmstudio" || a.Name() != "LM Studio" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if c.Resume != nil || c.Fork != nil || c.SkipPermissions != nil || c.RootEnv != "" {
		t.Fatalf("LM Studio has no terminal resume: %+v", c)
	}
	if c.App == nil {
		t.Fatal("App is nil; LM Studio is opened as an app")
	}
	if c.App.Name != "LM Studio" || c.App.Program != "LM Studio.exe" {
		t.Errorf("App = %+v", *c.App)
	}
	for _, a := range c.App.Args {
		if strings.Contains(a, "{id}") || strings.Contains(a, "{cwd}") {
			t.Errorf("App.Args must not reference a session or folder: %q", a)
		}
	}
	want := `%LOCALAPPDATA%\Programs\LM Studio\LM Studio.exe`
	found := false
	for _, cand := range c.App.Candidates {
		if cand == want {
			found = true
		}
	}
	if !found {
		t.Errorf("Candidates %q lack %q", c.App.Candidates, want)
	}
}

func TestSources(t *testing.T) {
	env := adapterstest.FixtureEnv(t, "testdata", nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	conv := filepath.Join(env.Home, ".lmstudio", "conversations")
	want := []string{
		filepath.Join(conv, "1767225600000.conversation.json"),
		filepath.Join(conv, "1767398400000.conversation.json"),
		filepath.Join(conv, "1767484800000.conversation.json"),
		filepath.Join(conv, "1767571200000.conversation.json"),
		filepath.Join(conv, "Folder One", "1767312000000.conversation.json"),
	}
	var got []string
	for _, s := range srcs {
		got = append(got, s.Path)
		if s.Adapter != "lmstudio" || s.Root != "" || len(s.Files) != 1 || s.Files[0] != s.Path {
			t.Errorf("source %+v", s)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sources:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSourcesMissingHome(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on an empty home = %v, %v", srcs, err)
	}
	env.Home = ""
	srcs, err = New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources with no home = %v, %v", srcs, err)
	}
}

func TestReadFixtures(t *testing.T) {
	env := adapterstest.FixtureEnv(t, "testdata", nil)
	sessions, skipped := adapterstest.ReadAll(t, testAdapter(), env)
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	// The empty chat (1767571200000) has no messages and is not a session.
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.NativeID)
		if s.Agent != "lmstudio" || s.Origin != model.OriginLive || s.Coverage != model.CoverageFull || s.Cwd != "" || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: common fields %+v", s.NativeID, s)
		}
	}
	if strings.Join(ids, ",") != "1767225600000,1767312000000,1767398400000,1767484800000" {
		t.Fatalf("sessions = %v", ids)
	}

	t.Run("thinking, structural tokens and branches", func(t *testing.T) {
		s := sessions[0]
		if s.Title != "Sourdough starter schedule" || s.TitleSource != model.TitleAgent {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1767225600000)) || !s.UpdatedAt.Equal(ms(1767225780000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, Text: "How often should I feed a sourdough starter?"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1767225606000), Text: "Feed it once a day at room temperature."},
			{Role: model.RoleUser, Kind: model.KindText, Text: "And in the fridge?"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1767225780000), Text: "Once a week is enough in the fridge."},
		})
		for _, m := range s.Messages {
			if m.At.Location() != time.UTC {
				t.Errorf("message time not UTC: %v", m.At)
			}
		}
	})

	t.Run("null name, files, subfolder", func(t *testing.T) {
		s := sessions[1]
		if s.Title != "Plan a three-day trip to the mountains" || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1767312000000)) || !s.UpdatedAt.Equal(ms(1767312060000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, Text: "Plan a   three-day\n trip to the mountains\n[file: packing-list.txt]\n[image]"},
			{Role: model.RoleAssistant, Kind: model.KindText, Text: "Day one: hike to the lake."},
		})
		for _, m := range s.Messages {
			if strings.Contains(m.Text, "unsent draft") {
				t.Errorf("clientInput (unsent draft) must not be read: %q", m.Text)
			}
		}
	})

	t.Run("tool calls and outputs", func(t *testing.T) {
		s := sessions[2]
		if s.Title != "Directory listing" || s.TitleSource != model.TitleAgent {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1767398400000)) || !s.UpdatedAt.Equal(ms(1767398430000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		var out strings.Builder
		for i := 1; i <= 200; i++ {
			fmt.Fprintf(&out, "file-%03d.txt\n", i)
		}
		checkMessages(t, s.Messages, []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, Text: "List the files in the demo folder"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1767398412000), Text: "I will list them."},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ms(1767398412000), Text: `list_dir: C:\work\demo`},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ms(1767398414000), Text: `search_notes: {"topic":"lighthouses","limit":3}`},
			{Role: model.RoleTool, Kind: model.KindToolOutput, Text: out.String()[:model.MaxToolOutput]},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1767398430000), Text: "There are 200 files."},
		})
	})

	t.Run("placeholder name, selected version, Arabic", func(t *testing.T) {
		s := sessions[3]
		prompt := strings.TrimSpace(strings.Repeat("كيف أزرع الطماطم في الشرفة؟ ", 5))
		wantTitle := string([]rune(prompt)[:100])
		if s.Title != wantTitle || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s), want %q", s.Title, s.TitleSource, wantTitle)
		}
		if !s.StartedAt.Equal(ms(1767484800000)) || !s.UpdatedAt.Equal(ms(1767484860000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, Text: prompt},
		})
	})
}

func checkMessages(t *testing.T, got, want []model.Message) {
	t.Helper()
	if len(got) != len(want) {
		for i, m := range got {
			t.Logf("got[%d] = %s/%s %v %q", i, m.Role, m.Kind, m.At, trunc(m.Text))
		}
		t.Fatalf("%d messages, want %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Role != w.Role || g.Kind != w.Kind || !g.At.Equal(w.At) || g.Text != w.Text {
			t.Errorf("message %d:\n got %s/%s %v %q\nwant %s/%s %v %q", i, g.Role, g.Kind, g.At, trunc(g.Text), w.Role, w.Kind, w.At, trunc(w.Text))
		}
	}
}

func trunc(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

// writeConv writes a conversation file under <home>/.lmstudio/conversations and
// returns an Env for that home and the Source for the file.
func writeConv(t *testing.T, name, body string) (adapters.Env, adapters.Source) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "home", ".lmstudio", "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	env := adapterstest.FixtureEnv(t, root, nil)
	return env, adapters.Source{Adapter: "lmstudio", Path: p, Files: []string{p}}
}

func TestReadBrokenJSON(t *testing.T) {
	env, src := writeConv(t, "1767000000000.conversation.json", `{"name": "half written", "messages": [{"versions": [`)
	if _, err := testAdapter().Read(context.Background(), env, src); err == nil {
		t.Fatal("a torn JSON file must fail the source so the next sync reads it again")
	}
}

func TestReadRecentlyModified(t *testing.T) {
	body := `{"name": "Fresh", "createdAt": 1767000000000, "userLastMessagedAt": 1767000000000,
	  "messages": [{"versions": [{"type": "singleStep", "role": "user", "content": [{"type": "text", "text": "hello"}]}], "currentlySelected": 0}]}`
	env, src := writeConv(t, "1767000000000.conversation.json", body)
	mtime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(src.Path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	a := adapter{now: func() time.Time { return mtime.Add(2 * time.Second) }}
	if _, err := a.Read(context.Background(), env, src); err == nil || !strings.Contains(err.Error(), "next sync") {
		t.Fatalf("a file changed 2 s ago must be left for the next sync, got err=%v", err)
	}
	a.now = func() time.Time { return mtime.Add(6 * time.Second) }
	res, err := a.Read(context.Background(), env, src)
	if err != nil || len(res.Sessions) != 1 {
		t.Fatalf("after 6 s: %v, %v", res, err)
	}
}

func TestReadMalformedMessagesSkipped(t *testing.T) {
	body := `{"name": "Mixed", "createdAt": 1767000000000, "userLastMessagedAt": 1767000060000,
	  "messages": [
	    {"versions": [{"type": "singleStep", "role": "user", "content": [{"type": "text", "text": "kept"}]}], "currentlySelected": 0},
	    "not an object",
	    {"versions": [{"type": "singleStep", "role": "alien", "content": [{"type": "text", "text": "unknown role"}]}], "currentlySelected": 0},
	    {"versions": [], "currentlySelected": 0},
	    {"versions": [{"type": "singleStep", "role": "assistant", "content": [{"type": "text", "text": 42}, {"type": "text", "text": "answer"}]}], "currentlySelected": 7},
	    {"versions": [{"type": "multiStep", "role": "assistant", "steps": [{"type": "contentBlock", "content": []}, {"type": "debugInfoBlock", "debugInfo": "x"}]}], "currentlySelected": 0}
	  ]}`
	env, src := writeConv(t, "1767000000000.conversation.json", body)
	res, err := testAdapter().Read(context.Background(), env, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 4 { // "not an object", unknown role, no versions, the numeric text part
		t.Errorf("skipped = %d, want 4", res.Skipped)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions = %d", len(res.Sessions))
	}
	checkMessages(t, res.Sessions[0].Messages, []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, Text: "kept"},
		// currentlySelected out of range falls back to the last version.
		{Role: model.RoleAssistant, Kind: model.KindText, Text: "answer"},
	})
}

func TestReadTimeFallbacks(t *testing.T) {
	// No createdAt and no *LastMessagedAt: StartedAt comes from the file name
	// (the creation time in ms), UpdatedAt from the newest message time.
	body := `{"name": "No dates", "messages": [
	    {"versions": [{"type": "singleStep", "role": "user", "content": "plain string content"}], "currentlySelected": 0},
	    {"versions": [{"type": "multiStep", "role": "assistant", "steps": [{"type": "contentBlock", "stepIdentifier": "1767000090000-0.5", "content": [{"type": "text", "text": "ok"}]}]}], "currentlySelected": 0}
	  ]}`
	env, src := writeConv(t, "1767000000000.conversation.json", body)
	res, err := testAdapter().Read(context.Background(), env, src)
	if err != nil || len(res.Sessions) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	s := res.Sessions[0]
	if !s.StartedAt.Equal(ms(1767000000000)) || !s.UpdatedAt.Equal(ms(1767000090000)) {
		t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
	}
	checkMessages(t, s.Messages, []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, Text: "plain string content"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1767000090000), Text: "ok"},
	})
}

func TestReadLeadingBOM(t *testing.T) {
	bom := string(rune(0xFEFF))
	body := bom + `{"name": "With BOM", "createdAt": 1767000000000,
	  "messages": [{"versions": [{"type": "singleStep", "role": "user", "content": [{"type": "text", "text": "hi"}]}], "currentlySelected": 0}]}`
	env, src := writeConv(t, "1767000000000.conversation.json", body)
	res, err := testAdapter().Read(context.Background(), env, src)
	if err != nil || len(res.Sessions) != 1 || res.Sessions[0].Title != "With BOM" {
		t.Fatalf("BOM-prefixed file: %+v, %v", res, err)
	}
}

func TestReadLargeRecord(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 2<<20/16) // 2 MB in a single message
	body := `{"name": "Big", "createdAt": 1767000000000, "userLastMessagedAt": 1767000000000,
	  "messages": [{"versions": [{"type": "singleStep", "role": "user", "content": [{"type": "text", "text": "` + big + `"}]}], "currentlySelected": 0}]}`
	env, src := writeConv(t, "1767000000000.conversation.json", body)
	res, err := testAdapter().Read(context.Background(), env, src)
	if err != nil || len(res.Sessions) != 1 || len(res.Sessions[0].Messages) != 1 {
		t.Fatalf("%v", err)
	}
	if got := res.Sessions[0].Messages[0].Text; got != big {
		t.Errorf("2 MB message: got %d bytes", len(got))
	}
}

func TestHomePointer(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	moved := filepath.Join(root, "elsewhere", "lms-home")
	for _, d := range []string{
		filepath.Join(home, ".lmstudio", "conversations"),
		filepath.Join(moved, "conversations"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a := filepath.Join(home, ".lmstudio", "conversations", "1767000000001.conversation.json")
	b := filepath.Join(moved, "conversations", "1767000000002.conversation.json")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte(`{"messages": []}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pointer := filepath.Join(home, ".lmstudio-home-pointer")
	env := adapterstest.FixtureEnv(t, root, nil)

	paths := func() []string {
		t.Helper()
		srcs, err := New().Sources(env)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range srcs {
			out = append(out, s.Path)
		}
		sort.Strings(out)
		return out
	}

	// Pointer to a relocated home (with a BOM and a trailing newline): both roots are read.
	if err := os.WriteFile(pointer, []byte("\xEF\xBB\xBF"+moved+"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{a, b}
	sort.Strings(want)
	if got := paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("relocated: %v, want %v", got, want)
	}

	// Pointer to the default home: no duplicates (case-insensitive on Windows paths).
	if err := os.WriteFile(pointer, []byte(strings.ToUpper(filepath.Join(home, ".lmstudio"))), 0o644); err != nil {
		t.Fatal(err)
	}
	got := paths()
	if len(got) == 0 || len(got) > 2 || got[0] != a {
		t.Errorf("pointer to default: %v", got)
	}
	if runtime.GOOS == "windows" && len(got) != 1 {
		t.Errorf("pointer to default (same folder, other case) listed twice: %v", got)
	}

	// Garbage pointer (relative path) is ignored.
	if err := os.WriteFile(pointer, []byte("not/an/absolute/path"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := paths(); len(got) != 1 || got[0] != a {
		t.Errorf("garbage pointer: %v", got)
	}
}

// TestReadOnly checks that listing and reading the fixture tree changes nothing in it.
func TestReadOnly(t *testing.T) {
	env := adapterstest.FixtureEnv(t, "testdata", nil)
	before := snapshot(t, "testdata")
	adapterstest.ReadAll(t, testAdapter(), env)
	after := snapshot(t, "testdata")
	if before != after {
		t.Errorf("fixture tree changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() { // directory mtimes change lazily on Windows; compare names only
			fmt.Fprintf(&b, "%s|dir\n", p)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s|%d|%d\n", p, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
