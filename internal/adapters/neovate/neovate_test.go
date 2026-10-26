package neovate

import (
	"context"
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
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func fixtureEnv(t *testing.T) adapters.Env {
	t.Helper()
	return adapterstest.FixtureEnv(t, "testdata", nil)
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

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "neovate" || a.Name() != "Neovate" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestSources(t *testing.T) {
	env := fixtureEnv(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	dataJSON := filepath.Join(env.Home, ".neovate", "data.json")
	var got []string
	for _, s := range srcs {
		if s.Adapter != "neovate" {
			t.Errorf("%s: Adapter = %q", s.Path, s.Adapter)
		}
		if s.Root != "" {
			t.Errorf("%s: Root = %q, want default", s.Path, s.Root)
		}
		want := []string{s.Path, dataJSON}
		if !reflect.DeepEqual(s.Files, want) {
			t.Errorf("%s: Files = %v, want %v", s.Path, s.Files, want)
		}
		rel, _ := filepath.Rel(filepath.Join(env.Home, ".neovate", "projects"), s.Path)
		got = append(got, filepath.ToSlash(rel))
	}
	want := []string{
		"c-work-alpha-proj/99990000.jsonl",
		"c-work-alpha-proj/a1b2c3d4.jsonl",
		"c-work-alpha-proj/e5f6a7b8.jsonl",
		"c-x-a-b/11112222.jsonl",
		"d-code-beta/0badc0de.jsonl",
		"unknown-folder/33334444.jsonl",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sources =\n%v\nwant\n%v", got, want)
	}
}

func TestSourcesMissingRoot(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on empty home = %v, %v; want none, nil", srcs, err)
	}
}

func TestReadFixtureTree(t *testing.T) {
	env := fixtureEnv(t)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (one malformed line)", skipped)
	}
	got := byID(t, sessions)
	var ids []string
	for id := range got {
		ids = append(ids, id)
	}
	if len(got) != 5 {
		t.Fatalf("got %d sessions %v, want 5 (config-only file and nested file ignored)", len(got), ids)
	}
	for _, s := range sessions {
		if s.Agent != "neovate" || s.Coverage != model.CoverageFull || s.Origin != model.OriginLive || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: agent/coverage/origin/root/machine = %q/%q/%q/%q/%q", s.NativeID, s.Agent, s.Coverage, s.Origin, s.Root, s.Machine)
		}
		if s.StartedAt.Location() != time.UTC || s.UpdatedAt.Location() != time.UTC {
			t.Errorf("%s: times not UTC", s.NativeID)
		}
		for i, m := range s.Messages {
			if !m.At.IsZero() && m.At.Location() != time.UTC {
				t.Errorf("%s msg %d: At not UTC", s.NativeID, i)
			}
		}
	}

	t.Run("full session", func(t *testing.T) {
		s := got["a1b2c3d4"]
		if s.Cwd != `C:\work\Alpha Proj` {
			t.Errorf("Cwd = %q, want the data.json key (case variants of one path resolve to the first)", s.Cwd)
		}
		if s.Title != "Fix the widget parser" || s.TitleSource != model.TitleAgent {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ts("2025-10-08T10:00:00Z")) || !s.UpdatedAt.Equal(ts("2025-10-08T10:04:00Z")) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		want := []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: ts("2025-10-08T10:00:00Z"), Text: "Please fix the widget parser in parser.go"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2025-10-08T10:00:05Z"), Text: "I will read the file first."},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ts("2025-10-08T10:00:05Z"), Text: `read: C:\work\Alpha Proj\parser.go`},
			{Role: model.RoleTool, Kind: model.KindToolOutput, At: ts("2025-10-08T10:00:06Z"), Text: "package parser\n\nfunc Parse() {}"},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ts("2025-10-08T10:00:10Z"), Text: "bash: go test ./parser/..."},
			{Role: model.RoleTool, Kind: model.KindToolOutput, At: ts("2025-10-08T10:00:12Z"), Text: "ok  \tparser\t0.01s"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2025-10-08T10:00:15Z"), Text: "The parser is fixed."},
			{Role: model.RoleUser, Kind: model.KindText, At: ts("2025-10-08T10:03:00Z"), Text: "Now add tests"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2025-10-08T10:04:00Z"), Text: "Tests added."},
		}
		assertMessages(t, s.Messages, want)
		for _, m := range s.Messages {
			if strings.Contains(m.Text, "private reasoning") {
				t.Error("reasoning was archived")
			}
			if strings.Contains(m.Text, "compacted") || strings.Contains(m.Text, "Generated summary") {
				t.Error("compaction summary was archived")
			}
			if strings.Contains(m.Text, "Request interrupted") {
				t.Error("interruption marker was archived")
			}
		}
	})

	t.Run("prompt title and tolerant parsing", func(t *testing.T) {
		s := got["e5f6a7b8"]
		if s.Cwd != `C:\work\Alpha Proj` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		wantTitle := "مرحبا بالعالم Build a CLI " + strings.Repeat("abcdefghij", 7) + "abcd"
		if s.Title != wantTitle || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s), want %q (prompt)", s.Title, s.TitleSource, wantTitle)
		}
		if n := len([]rune(s.Title)); n != 100 {
			t.Errorf("title has %d runes, want 100", n)
		}
		if !s.StartedAt.Equal(ts("2025-10-09T08:00:00Z")) || !s.UpdatedAt.Equal(ts("2025-10-09T08:00:30Z")) {
			t.Errorf("times = %v .. %v (the unterminated last line must not count)", s.StartedAt, s.UpdatedAt)
		}
		want := []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: ts("2025-10-09T08:00:00Z"), Text: "  مرحبا   بالعالم\n\tBuild a CLI " + strings.Repeat("abcdefghij", 12)},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2025-10-09T08:00:30Z"), Text: "Fallback text answer"},
		}
		assertMessages(t, s.Messages, want)
	})

	t.Run("record cwd and epoch-ms timestamps", func(t *testing.T) {
		s := got["0badc0de"]
		if s.Cwd != `D:\Code\Beta` {
			t.Errorf("Cwd = %q, want the FIRST record cwd", s.Cwd)
		}
		if s.Title != "hello beta" || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ts("2025-10-08T11:00:00Z")) || !s.UpdatedAt.Equal(ts("2025-10-08T11:05:30Z")) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		want := []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: ts("2025-10-08T11:00:00Z"), Text: "hello beta"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2025-10-08T11:01:00Z"), Text: "hi from beta"},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ts("2025-10-08T11:01:00Z"), Text: "todoWrite: 1 todo"},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ts("2025-10-08T11:02:00Z"), Text: `mcp__x__y: {"zeta":1,"alpha":[true]}`},
			{Role: model.RoleTool, Kind: model.KindToolOutput, At: ts("2025-10-08T11:05:30Z"), Text: "tool said hi"},
		}
		assertMessages(t, s.Messages, want)
	})

	t.Run("ambiguous project folder", func(t *testing.T) {
		s := got["11112222"]
		if s.Cwd != "" {
			t.Errorf("Cwd = %q, want empty: two different data.json paths encode to this folder", s.Cwd)
		}
		if s.Title != "ambiguous project prompt" || len(s.Messages) != 1 {
			t.Errorf("title %q, %d messages", s.Title, len(s.Messages))
		}
	})

	t.Run("unknown project folder", func(t *testing.T) {
		s := got["33334444"]
		if s.Cwd != "" {
			t.Errorf("Cwd = %q, want empty (never decoded from the folder name)", s.Cwd)
		}
		want := []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: ts("2025-10-12T09:00:00Z"), Text: "prompt in an unknown project"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2025-10-12T09:00:07Z"), Text: "answer one\nanswer two"},
		}
		assertMessages(t, s.Messages, want)
	})
}

func assertMessages(t *testing.T, got, want []model.Message) {
	t.Helper()
	if len(got) != len(want) {
		for i, m := range got {
			t.Logf("got[%d] = %s/%s %v %q", i, m.Role, m.Kind, m.At, m.Text)
		}
		t.Fatalf("got %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Role != w.Role || g.Kind != w.Kind || !g.At.Equal(w.At) || g.Text != w.Text {
			t.Errorf("message %d = %s/%s %v %q\n            want %s/%s %v %q", i, g.Role, g.Kind, g.At, g.Text, w.Role, w.Kind, w.At, w.Text)
		}
	}
}

func TestEncodeProject(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Users\someone\neovate`, "c-users-someone-neovate"},
		{`C:\work\Alpha Proj`, "c-work-alpha-proj"},
		{`/home/dev/my_app/`, "home-dev-my-app"},
		{`D:\مشروع\app--x`, "d-app-x"},
		{`C:\x\a_b`, "c-x-a-b"},
		{"", ""},
	}
	for _, c := range cases {
		if got := encodeProject(c.in); got != c.want {
			t.Errorf("encodeProject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// writeTree creates files (relative slash paths → content) under dir/home/.neovate.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, "home", ".neovate", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLargeSingleLineRecord(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 2<<20)
	writeTree(t, dir, map[string]string{
		"projects/proj/abcd1234.jsonl": `{"parentUuid":null,"uuid":"u1","role":"user","content":"` + big + `","type":"message","timestamp":"2025-10-08T10:00:00.000Z","sessionId":"abcd1234"}` + "\n" +
			`{"parentUuid":"u1","uuid":"u2","role":"user","content":[{"type":"tool_result","id":"c","name":"bash","input":{},"result":{"llmContent":"` + big + `"}}],"type":"message","timestamp":"2025-10-08T10:00:01.000Z","sessionId":"abcd1234"}` + "\n",
	})
	sessions, skipped := adapterstest.ReadAll(t, New(), adapterstest.FixtureEnv(t, dir, nil))
	if skipped != 0 || len(sessions) != 1 {
		t.Fatalf("sessions=%d skipped=%d", len(sessions), skipped)
	}
	msgs := sessions[0].Messages
	if len(msgs) != 2 || len(msgs[0].Text) != len(big) {
		t.Fatalf("got %d messages; first has %d bytes, want %d", len(msgs), len(msgs[0].Text), len(big))
	}
	if msgs[1].Kind != model.KindToolOutput || len(msgs[1].Text) > model.MaxToolOutput {
		t.Errorf("tool output kind %s with %d bytes, want <= %d", msgs[1].Kind, len(msgs[1].Text), model.MaxToolOutput)
	}
	if sessions[0].Cwd != "" {
		t.Errorf("Cwd = %q without data.json", sessions[0].Cwd)
	}
}

func TestToolOutputTruncatedOnRuneBoundary(t *testing.T) {
	s := strings.Repeat("ب", 3000) // 2 bytes per rune
	got := truncateBytes(s, model.MaxToolOutput)
	if len(got) > model.MaxToolOutput || !strings.HasPrefix(s, got) || len(got) < model.MaxToolOutput-3 {
		t.Fatalf("truncateBytes kept %d bytes", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a rune")
	}
}

func TestTimesFallBackToFileMtime(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"projects/proj/beef0001.jsonl": `{"parentUuid":null,"uuid":"u1","role":"user","content":"no timestamps here","type":"message"}` + "\n",
	})
	p := filepath.Join(dir, "home", ".neovate", "projects", "proj", "beef0001.jsonl")
	mt := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	sessions, _ := adapterstest.ReadAll(t, New(), adapterstest.FixtureEnv(t, dir, nil))
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d", len(sessions))
	}
	s := sessions[0]
	if !s.StartedAt.Equal(mt) || !s.UpdatedAt.Equal(mt) || s.StartedAt.Location() != time.UTC {
		t.Errorf("times = %v .. %v, want file mtime %v", s.StartedAt, s.UpdatedAt, mt)
	}
	if !s.Messages[0].At.IsZero() {
		t.Errorf("message At = %v, want zero (unknown)", s.Messages[0].At)
	}
}

func TestBadDataJSON(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"data.json":                   `{"projects": {"C:\\p": `,
		"projects/c-p/cafe0001.jsonl": `{"parentUuid":null,"uuid":"u1","role":"user","content":"hi","type":"message","timestamp":"2025-10-08T10:00:00.000Z"}` + "\n",
	})
	env := adapterstest.FixtureEnv(t, dir, nil)
	a := New()
	srcs, err := a.Sources(env)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("sources %v %v", srcs, err)
	}
	res, err := a.Read(context.Background(), env, srcs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].Cwd != "" {
		t.Fatalf("sessions %+v", res.Sessions)
	}
	if len(res.Warnings) == 0 {
		t.Error("want a warning about the unreadable data.json")
	}
}

func TestDataJSONCacheSeesChanges(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"projects/c-p/cafe0002.jsonl": `{"parentUuid":null,"uuid":"u1","role":"user","content":"hi","type":"message","timestamp":"2025-10-08T10:00:00.000Z"}` + "\n",
	})
	env := adapterstest.FixtureEnv(t, dir, nil)
	a := New()
	read := func() string {
		srcs, err := a.Sources(env)
		if err != nil || len(srcs) != 1 {
			t.Fatalf("sources %v %v", srcs, err)
		}
		res, err := a.Read(context.Background(), env, srcs[0])
		if err != nil || len(res.Sessions) != 1 {
			t.Fatalf("read %+v %v", res, err)
		}
		return res.Sessions[0].Cwd
	}
	if got := read(); got != "" {
		t.Fatalf("Cwd before data.json exists = %q", got)
	}
	writeTree(t, dir, map[string]string{"data.json": `{"projects":{"C:\\p":{"history":["hi"]}}}`})
	if got := read(); got != `C:\p` {
		t.Fatalf("Cwd after data.json appeared = %q", got)
	}
}

func TestReadOnly(t *testing.T) {
	type info struct {
		size  int64
		mtime time.Time
	}
	snap := func() map[string]info {
		m := map[string]info{}
		err := filepath.WalkDir("testdata", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() { // folder mtimes change lazily on Windows; compare files only
				return nil
			}
			fi, err := os.Stat(p) // Stat, not DirEntry.Info: the listing can be stale on Windows
			if err != nil {
				return err
			}
			m[p] = info{fi.Size(), fi.ModTime()}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	before := snap()
	adapterstest.ReadAll(t, New(), fixtureEnv(t))
	after := snap()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("fixture tree changed during read:\nbefore %v\nafter  %v", before, after)
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if !reflect.DeepEqual(c.Resume, []string{"neovate", "--resume", "{id}"}) {
		t.Errorf("Resume = %v", c.Resume)
	}
	if !reflect.DeepEqual(c.SkipPermissions, []string{"--approval-mode", "yolo"}) {
		t.Errorf("SkipPermissions = %v", c.SkipPermissions)
	}
	if c.Fork != nil || c.App != nil || c.RootEnv != "" {
		t.Errorf("Fork/App/RootEnv = %v/%v/%q", c.Fork, c.App, c.RootEnv)
	}
	if !reflect.DeepEqual(c.ProgramCandidates, []string{`%APPDATA%\npm\neovate.cmd`}) {
		t.Errorf("ProgramCandidates = %v", c.ProgramCandidates)
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }

func TestSamePathKey(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{`C:\Work\Proj`, `c:/work/proj/`, true},
		{`C:\`, `c:/`, true},
		{`C:\Work\Proj`, `C:\Work\Other`, false},
		{`\\server\Share\x`, `\\SERVER\share\x\`, true},
		{"/home/u/Proj", "/home/u/Proj/", true},
		{"/home/u/Proj", "/home/u/proj", false}, // Unix folders are case-sensitive
		{"/", "/", true},
	}
	for _, c := range cases {
		if got := samePathKey(c.a) == samePathKey(c.b); got != c.same {
			t.Errorf("samePathKey(%q) == samePathKey(%q) = %v, want %v", c.a, c.b, got, c.same)
		}
	}
}
