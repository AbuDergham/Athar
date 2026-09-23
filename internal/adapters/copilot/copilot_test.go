package copilot

import (
	"context"
	"errors"
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

// testAdapter returns an adapter whose clock is far in the future, so the
// "changed less than 5 s ago" rule never triggers on freshly checked-out fixtures.
func testAdapter() adapter {
	return adapter{now: func() time.Time { return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }}
}

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "copilot" || a.Name() != "VS Code Copilot Chat" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if c.Resume != nil || c.Fork != nil || c.SkipPermissions != nil || c.RootEnv != "" || c.ProgramCandidates != nil {
		t.Fatalf("VS Code chat has no terminal resume: %+v", c)
	}
	if c.App == nil || c.App.Name != "VS Code" || c.App.Program != "code" || strings.Join(c.App.Args, " ") != "{cwd}" {
		t.Fatalf("App = %+v", c.App)
	}
	want := `%LOCALAPPDATA%\Programs\Microsoft VS Code\bin\code.cmd`
	if len(c.App.Candidates) == 0 || c.App.Candidates[0] != want {
		t.Errorf("Candidates = %q, want %q first", c.App.Candidates, want)
	}
}

func TestSources(t *testing.T) {
	env := adapterstest.FixtureEnv(t, "testdata", nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	code := filepath.Join(env.AppData, "Code", "User")
	ws := filepath.Join(code, "workspaceStorage")
	want := map[string]string{
		filepath.Join(ws, "0a1b2c3d4e5f60718293a4b5c6d7e8f9", "chatSessions", "11111111-1111-4111-8111-111111111111.json"):  "Code",
		filepath.Join(ws, "0a1b2c3d4e5f60718293a4b5c6d7e8f9", "chatSessions", "22222222-2222-4222-8222-222222222222.json"):  "Code",
		filepath.Join(ws, "1b2c3d4e5f60718293a4b5c6d7e8f90a", "chatSessions", "33333333-3333-4333-8333-333333333333.jsonl"): "Code",
		filepath.Join(ws, "1b2c3d4e5f60718293a4b5c6d7e8f90a", "chatSessions", "88888888-8888-4888-8888-888888888888.jsonl"): "Code",
		filepath.Join(ws, "2c3d4e5f60718293a4b5c6d7e8f90a1b", "chatSessions", "44444444-4444-4444-8444-444444444444.jsonl"): "Code",
		filepath.Join(code, "globalStorage", "emptyWindowChatSessions", "55555555-5555-4555-8555-555555555555.json"):        "Code",
		filepath.Join(env.AppData, "Code - Insiders", "User", "workspaceStorage", "3d4e5f60718293a4b5c6d7e8f90a1b2c",
			"chatSessions", "66666666-6666-4666-8666-666666666666.json"): "Code - Insiders",
	}
	got := map[string]string{}
	for _, s := range srcs {
		if s.Adapter != "copilot" || s.Root != "" || len(s.Files) != 1 || s.Files[0] != s.Path {
			t.Errorf("source %+v", s)
		}
		got[s.Path] = s.Extra[extraHost]
	}
	if len(got) != len(want) || len(srcs) != len(want) {
		t.Errorf("got %d sources, want %d: %v", len(srcs), len(want), got)
	}
	for p, host := range want {
		if got[p] != host {
			t.Errorf("source %s: host %q, want %q", p, got[p], host)
		}
	}
}

func TestSourcesMissing(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	if srcs, err := New().Sources(env); err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on an empty tree = %v, %v", srcs, err)
	}
	env.AppData = ""
	if srcs, err := New().Sources(env); err != nil || len(srcs) != 0 {
		t.Fatalf("Sources with no APPDATA = %v, %v", srcs, err)
	}
}

// TestSourcesPreferNewer: a session saved in both formats is listed once.
func TestSourcesPreferNewer(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "appdata", "Code", "User", "workspaceStorage", "h1", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	files := map[string]time.Time{
		"aaaa.json": old.Add(time.Hour), "aaaa.jsonl": old, // .json is newer
		"bbbb.json": old, "bbbb.jsonl": old.Add(time.Hour), // .jsonl is newer
		"cccc.json": old, "cccc.jsonl": old, // tie: the log wins
		"notes.txt": old,
	}
	for name, mt := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	srcs, err := New().Sources(adapterstest.FixtureEnv(t, root, nil))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range srcs {
		got = append(got, filepath.Base(s.Path))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "aaaa.json,bbbb.jsonl,cccc.jsonl" {
		t.Errorf("sources = %v", got)
	}
}

func TestReadFixtures(t *testing.T) {
	env := adapterstest.FixtureEnv(t, "testdata", nil)
	sessions, skipped := adapterstest.ReadAll(t, testAdapter(), env)
	// One undecodable response part (session 1) and one garbage log line (session 3).
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.NativeID[:8])
		if s.Agent != "copilot" || s.Origin != model.OriginLive || s.Coverage != model.CoverageFull || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: common fields %+v", s.NativeID, s)
		}
		if s.StartedAt.Location() != time.UTC || s.UpdatedAt.Location() != time.UTC {
			t.Errorf("%s: times not UTC", s.NativeID)
		}
		for _, m := range s.Messages {
			if strings.Contains(m.Text, "private reasoning") || strings.Contains(m.Text, "step one") {
				t.Errorf("%s: thinking was indexed: %q", s.NativeID, m.Text)
			}
			if strings.Contains(m.Text, "unsent draft") || strings.Contains(m.Text, "working for a while") {
				t.Errorf("%s: UI state was indexed: %q", s.NativeID, m.Text)
			}
		}
	}
	// 22222222 and 88888888 are empty drafts.
	if strings.Join(ids, ",") != "11111111,33333333,44444444,55555555,66666666" {
		t.Fatalf("sessions = %v", ids)
	}

	t.Run("json: custom title, references, tools, edits", func(t *testing.T) {
		s := sessions[0]
		if s.NativeID != "11111111-1111-4111-8111-111111111111" || s.Cwd != `C:\work\demo` {
			t.Errorf("id/cwd = %q %q", s.NativeID, s.Cwd)
		}
		if s.Title != "Refactor the lexer" || s.TitleSource != model.TitleUser {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1767225600000)) || !s.UpdatedAt.Equal(ms(1767225780000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		t1, t2, t3 := ms(1767225660000), ms(1767225720000), ms(1767225780000)
		checkMessages(t, s.Messages, []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: t1, Text: "Explain the tokenizer"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: t1, Text: "The tokenizer in lexer.go splits input into tokens.\n\nSee C:\\work\\demo\\token.go too."},
			{Role: model.RoleUser, Kind: model.KindText, At: t2, Text: "Rename Scan to Next and run the tests"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: t2, Text: "I will read the file first."},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: t2, Text: `copilot_readFile: Read C:\work\demo\lexer.go, lines 1 to 40`},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: t2, Text: "copilot_runInTerminal: go test ./..."},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: t2, Text: "copilot_findTextInFiles: Searching text for `Scan` (`**/*.go`)"},
			// The hidden copilot_insertEdit invocation is replaced by its textEditGroup.
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: t2, Text: `edit: C:\work\demo\lexer.go`},
			{Role: model.RoleAssistant, Kind: model.KindText, At: t2, Text: "Done. All tests pass."},
			{Role: model.RoleUser, Kind: model.KindText, At: t3, Text: "Add a doc comment"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: t3, Text: "Added the comment."},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: t3, Text: `edit: C:\work\demo\lexer.go`},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: t3, Text: `edit: C:\work\demo\doc.go`},
		})
	})

	t.Run("jsonl: replayed log", func(t *testing.T) {
		s := sessions[1]
		if s.Cwd != `D:\notes app` {
			t.Errorf("cwd = %q", s.Cwd)
		}
		if s.Title != "Notes app questions" || s.TitleSource != model.TitleUser {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1767312000000)) || !s.UpdatedAt.Equal(ms(1767312120000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: ms(1767312060000), Text: "First question about notes"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1767312060000), Text: "Full answer about notes."},
			{Role: model.RoleUser, Kind: model.KindText, At: ms(1767312120000), Text: "Second question, edited"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1767312120000), Text: "Second answer."},
		})
	})

	t.Run("multi-root workspace, Arabic", func(t *testing.T) {
		s := sessions[2]
		if s.Cwd != "" {
			t.Errorf("cwd = %q, want none for a .code-workspace", s.Cwd)
		}
		prompt := "ما هو أفضل اسم للمتغير؟"
		if s.Title != prompt || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1767398400000)) || !s.UpdatedAt.Equal(ms(1767398460000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: ms(1767398460000), Text: prompt},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1767398460000), Text: "اختر اسماً واضحاً يصف القيمة."},
		})
	})

	t.Run("empty window, long prompt title", func(t *testing.T) {
		s := sessions[3]
		if s.Cwd != "" {
			t.Errorf("cwd = %q", s.Cwd)
		}
		prompt := strings.TrimSpace("Write a small script that " + strings.Repeat("renames every photo in a folder by its date ", 4))
		if s.Title != prompt[:100] || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1767484800000)) || !s.UpdatedAt.Equal(ms(1767484860000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		if len(s.Messages) != 2 || s.Messages[0].Text != prompt {
			t.Errorf("messages = %+v", s.Messages)
		}
	})

	t.Run("insiders, remote folder, message parts", func(t *testing.T) {
		s := sessions[4]
		if s.Cwd != "vscode-remote://wsl%2Bubuntu/home/dev/app" {
			t.Errorf("cwd = %q", s.Cwd)
		}
		if s.Title != "Hello from parts" || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		checkMessages(t, s.Messages, []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: ms(1767571260000), Text: "Hello from parts"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1767571260000), Text: "Hello back."},
		})
	})
}

// writeFile writes data to name in a temp dir and sets its mtime.
func writeFile(t *testing.T, name, data string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func readOne(t *testing.T, a adapter, path string) (adapters.ReadResult, error) {
	t.Helper()
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	return a.Read(context.Background(), env, adapters.Source{Adapter: id, Path: path, Files: []string{path}})
}

const oneRequest = `{"version":3,"sessionId":"s-1","creationDate":1767225600000,"requests":[{"message":{"text":"hi"},"timestamp":1767225660000,"response":[{"value":"hello"}]}]}`

func TestRetryLater(t *testing.T) {
	mtime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	soon := adapter{now: func() time.Time { return mtime.Add(2 * time.Second) }}
	later := adapter{now: func() time.Time { return mtime.Add(time.Minute) }}

	t.Run("json written just now", func(t *testing.T) {
		p := writeFile(t, "s-1.json", oneRequest, mtime)
		if _, err := readOne(t, soon, p); !errors.Is(err, adapters.ErrRetryLater) {
			t.Fatalf("err = %v, want ErrRetryLater", err)
		}
		res, err := readOne(t, later, p)
		if err != nil || len(res.Sessions) != 1 {
			t.Fatalf("later: %+v, %v", res, err)
		}
	})

	t.Run("complete log written just now", func(t *testing.T) {
		p := writeFile(t, "s-1.jsonl", `{"kind":0,"v":`+oneRequest+"}\n", mtime)
		res, err := readOne(t, soon, p)
		if err != nil || len(res.Sessions) != 1 || res.Skipped != 0 {
			t.Fatalf("an append log with whole lines is readable at once: %+v, %v", res, err)
		}
	})

	t.Run("log with a torn last line", func(t *testing.T) {
		data := `{"kind":0,"v":` + oneRequest + "}\n" + `{"kind":1,"k":["customTitle"],"v":"Ti`
		p := writeFile(t, "s-1.jsonl", data, mtime)
		if _, err := readOne(t, soon, p); !errors.Is(err, adapters.ErrRetryLater) {
			t.Fatalf("recent torn log: err = %v, want ErrRetryLater", err)
		}
		res, err := readOne(t, later, p)
		if err != nil || len(res.Sessions) != 1 || res.Skipped != 1 {
			t.Fatalf("old torn log: %+v, %v", res, err)
		}
		if s := res.Sessions[0]; s.Title != "hi" || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
	})
}

func TestReadBadFiles(t *testing.T) {
	old := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	a := testAdapter()

	p := writeFile(t, "torn.json", `{"version":3,"requests":[{"message"`, old)
	if _, err := readOne(t, a, p); err == nil || errors.Is(err, adapters.ErrRetryLater) {
		t.Errorf("corrupt json: err = %v, want a plain error", err)
	}

	p = writeFile(t, "nosnap.jsonl", `{"kind":1,"k":["customTitle"],"v":"x"}`+"\n", old)
	res, err := readOne(t, a, p)
	if err != nil || len(res.Sessions) != 0 || len(res.Warnings) != 1 || res.Skipped != 1 {
		t.Errorf("log without snapshot: %+v, %v", res, err)
	}

	// A mistyped field loses only that field.
	p = writeFile(t, "typed.json", `{"version":3,"sessionId":"s-2","customTitle":7,"requests":[{"message":{"text":"hi"},"timestamp":1767225660000,"response":[]}]}`, old)
	res, err = readOne(t, a, p)
	if err != nil || len(res.Sessions) != 1 || res.Skipped != 1 {
		t.Fatalf("mistyped field: %+v, %v", res, err)
	}
	s := res.Sessions[0]
	if s.NativeID != "s-2" || s.Title != "hi" {
		t.Errorf("session = %+v", s)
	}
	// No creationDate: StartedAt falls back to the first request.
	if !s.StartedAt.Equal(ms(1767225660000)) || !s.UpdatedAt.Equal(ms(1767225660000)) {
		t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
	}

	// No sessionId and no times: the file name and mtime are used.
	p = writeFile(t, "9999.json", `{"requests":[{"message":{"text":"hi"},"response":[]}]}`, old)
	res, err = readOne(t, a, p)
	if err != nil || len(res.Sessions) != 1 {
		t.Fatalf("minimal file: %+v, %v", res, err)
	}
	if s := res.Sessions[0]; s.NativeID != "9999" || !s.StartedAt.Equal(old) || !s.UpdatedAt.Equal(old) || s.Cwd != "" {
		t.Errorf("minimal session = %+v", s)
	}
}

// TestLargeRecord: a log whose snapshot is one ~2 MB line.
func TestLargeRecord(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 2<<20/16)
	data := `{"kind":0,"v":{"version":3,"sessionId":"big","creationDate":1767225600000,"requests":[{"message":{"text":"big answer please"},"timestamp":1767225660000,"response":[{"value":"` + big + `"}]}]}}` + "\n" +
		`{"kind":2,"k":["requests"],"v":[{"message":{"text":"thanks"},"timestamp":1767225720000,"response":[{"value":"welcome"}]}]}` + "\n"
	p := writeFile(t, "big.jsonl", data, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	res, err := readOne(t, testAdapter(), p)
	if err != nil || len(res.Sessions) != 1 {
		t.Fatalf("%+v, %v", res.Skipped, err)
	}
	msgs := res.Sessions[0].Messages
	if len(msgs) != 4 || msgs[1].Text != big || msgs[3].Text != "welcome" {
		t.Errorf("got %d messages", len(msgs))
	}
}

func TestReadCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := writeFile(t, "s.json", oneRequest, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	if _, err := testAdapter().Read(ctx, adapters.Env{}, adapters.Source{Path: p}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func checkMessages(t *testing.T, got, want []model.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("got %d messages, want %d", len(got), len(want))
		for i, m := range got {
			t.Logf("  got[%d] = %s/%s %q", i, m.Role, m.Kind, m.Text)
		}
	}
	for i := 0; i < len(got) && i < len(want); i++ {
		g, w := got[i], want[i]
		if g.Role != w.Role || g.Kind != w.Kind || g.Text != w.Text || !g.At.Equal(w.At) {
			t.Errorf("message %d:\n got  %s/%s %v %q\n want %s/%s %v %q", i, g.Role, g.Kind, g.At, g.Text, w.Role, w.Kind, w.At, w.Text)
		}
	}
}
