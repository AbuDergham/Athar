package codebuff

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

// testZone is the "local" time zone used for Codebuff's local time-of-day stamps.
var testZone = time.FixedZone("UTC+3", 3*60*60)

// fixtureClock pins the package clock and zone for a test: the local zone is
// UTC+3 and "now" is far after every fixture file's mtime (so the 5 s rule
// for whole-file JSON never triggers by accident).
func fixtureClock(t *testing.T) {
	t.Helper()
	oldNow, oldZone := now, localZone
	now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	localZone = testZone
	t.Cleanup(func() { now, localZone = oldNow, oldZone })
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

type wantMsg struct {
	Role model.Role
	Kind model.Kind
	Text string
	At   string // RFC3339 UTC; "" = do not check
}

func checkMessages(t *testing.T, got []model.Message, want []wantMsg) {
	t.Helper()
	if len(got) != len(want) {
		for i, m := range got {
			t.Logf("got[%d] %s/%s %q", i, m.Role, m.Kind, m.Text)
		}
		t.Fatalf("got %d messages, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Role != w.Role || g.Kind != w.Kind || g.Text != w.Text {
			t.Errorf("message %d = %s/%s %q, want %s/%s %q", i, g.Role, g.Kind, g.Text, w.Role, w.Kind, w.Text)
		}
		if w.At != "" && !g.At.Equal(utc(w.At)) {
			t.Errorf("message %d At = %s, want %s", i, g.At.UTC().Format(time.RFC3339Nano), w.At)
		}
		if !g.At.IsZero() && g.At.Location() != time.UTC {
			t.Errorf("message %d At %v is not UTC", i, g.At)
		}
	}
}

func fixtureEnv(t *testing.T) adapters.Env {
	return adapterstest.FixtureEnv(t, "testdata", nil)
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "codebuff" || a.Name() == "" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
	c := a.Caps()
	if c.Resume != nil || c.Fork != nil || c.SkipPermissions != nil || c.App != nil || c.RootEnv != "" || len(c.ProgramCandidates) != 0 {
		t.Fatalf("Caps must be empty (never run freebuff): %+v", c)
	}
}

func TestSources(t *testing.T) {
	fixtureClock(t)
	env := fixtureEnv(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	def := filepath.Join(env.Home, ".config", "manicode")
	dev := filepath.Join(env.Home, ".config", "manicode-dev")
	type want struct {
		path  string
		files []string
		root  string
	}
	alpha := filepath.Join(def, "projects", "alpha", "chats", "2026-05-16T19-23-40.000Z")
	beta1 := filepath.Join(def, "projects", "beta", "chats", "2026-07-01T08-00-00.000Z")
	beta2 := filepath.Join(def, "projects", "beta", "chats", "2026-07-02T09-00-00.000Z")
	gamma := filepath.Join(dev, "projects", "gamma", "chats", "2026-08-01T12-00-00.000Z")
	wants := []want{
		{alpha, []string{filepath.Join(alpha, "log.jsonl")}, ""},
		{beta1, []string{filepath.Join(beta1, "chat-messages.json"), filepath.Join(beta1, "run-state.json"), filepath.Join(beta1, "chat-meta.json")}, ""},
		{beta2, []string{filepath.Join(beta2, "log.jsonl")}, ""},
		{filepath.Join(def, "message-history.json"), []string{filepath.Join(def, "message-history.json")}, ""},
		{gamma, []string{filepath.Join(gamma, "log.jsonl")}, dev},
		{filepath.Join(dev, "message-history.json"), []string{filepath.Join(dev, "message-history.json")}, dev},
	}
	if len(srcs) != len(wants) {
		for _, s := range srcs {
			t.Logf("source %s %v", s.Path, s.Files)
		}
		t.Fatalf("got %d sources, want %d", len(srcs), len(wants))
	}
	for i, w := range wants {
		s := srcs[i]
		if s.Adapter != "codebuff" || s.Path != w.path || s.Root != w.root || strings.Join(s.Files, "|") != strings.Join(w.files, "|") {
			t.Errorf("source %d = %+v, want path %s files %v root %q", i, s, w.path, w.files, w.root)
		}
	}
}

func TestSourcesSkipsEmptyAndMissing(t *testing.T) {
	dir := t.TempDir()
	env := adapterstest.FixtureEnv(t, dir, nil)
	// No .config at all: no sources, no error.
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("missing root: %d sources, err %v", len(srcs), err)
	}
	chats := filepath.Join(env.Home, ".config", "manicode", "projects", "p", "chats")
	mustMkdir(t, filepath.Join(chats, "2026-01-01T00-00-00.000Z")) // empty chat folder
	mustWrite(t, filepath.Join(chats, "stray.txt"), "not a chat")
	mustWrite(t, filepath.Join(env.Home, ".config", "manicodex", "message-history.json"), `["other tool"]`)
	mustWrite(t, filepath.Join(env.Home, ".config", "manicode-file"), "a file, not a root")
	srcs, err = New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("empty chat folder: %d sources (%+v), err %v", len(srcs), srcs, err)
	}
}

func TestReadFixtures(t *testing.T) {
	fixtureClock(t)
	env := fixtureEnv(t)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3 (one bad log line, one bad chat message, one non-string prompt)", skipped)
	}
	if len(sessions) != 4 {
		for _, s := range sessions {
			t.Logf("session %s", s.NativeID)
		}
		t.Fatalf("got %d sessions, want 4", len(sessions))
	}
	for _, s := range sessions {
		if s.Agent != "codebuff" {
			t.Errorf("%s: Agent = %q", s.NativeID, s.Agent)
		}
		for _, m := range s.Messages {
			for _, bad := range []string{"SHOULD NOT", "someone@example", "TESTHOST"} {
				if strings.Contains(m.Text, bad) {
					t.Errorf("%s: message leaked %q: %q", s.NativeID, bad, m.Text)
				}
			}
		}
	}

	t.Run("log.jsonl session", func(t *testing.T) {
		s := sessions[0]
		if s.NativeID != "alpha:2026-05-16T19-23-40.000Z" {
			t.Fatalf("NativeID = %q", s.NativeID)
		}
		if s.Cwd != `C:\work\alpha` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if s.Title != "Build a tiny todo app" || s.TitleSource != model.TitlePrompt {
			t.Errorf("Title = %q (%s)", s.Title, s.TitleSource)
		}
		if s.Coverage != model.CoveragePartial || s.Origin != model.OriginLive || s.Root != "" || s.Machine != "" {
			t.Errorf("coverage %s origin %s root %q machine %q", s.Coverage, s.Origin, s.Root, s.Machine)
		}
		if !s.StartedAt.Equal(utc("2026-05-16T19:23:40Z")) || !s.UpdatedAt.Equal(utc("2026-05-16T19:30:40Z")) {
			t.Errorf("times %s .. %s", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []wantMsg{
			{model.RoleUser, model.KindText, "Build a tiny todo app", "2026-05-16T19:24:00Z"},
			{model.RoleAssistant, model.KindText, "I will scaffold the app.", "2026-05-16T19:24:10Z"},
			{model.RoleAssistant, model.KindToolCall, "run_terminal_command: npm init -y", "2026-05-16T19:24:10Z"},
			{model.RoleTool, model.KindToolOutput, `{"stdout":"Wrote package.json"}`, "2026-05-16T19:24:10Z"},
			{model.RoleAssistant, model.KindToolCall, "write_file: src/todo.ts", "2026-05-16T19:24:20Z"},
			{model.RoleTool, model.KindToolOutput, "File written", "2026-05-16T19:24:20Z"},
			{model.RoleAssistant, model.KindToolCall, "spawn_agents: basher: run the tests", "2026-05-16T19:24:20Z"},
			{model.RoleTool, model.KindToolOutput, `[{"agentName":"Basher","value":{"output":"all passed"}}]`, "2026-05-16T19:24:20Z"},
			{model.RoleUser, model.KindText, "Add dark mode please", "2026-05-16T19:30:00Z"},
			{model.RoleAssistant, model.KindText, "Dark mode added. مرحبا بالعالم", "2026-05-16T19:30:30Z"},
			{model.RoleSystem, model.KindText, "Out of credits", "2026-05-16T19:30:40Z"},
		})
	})

	t.Run("chat-messages.json session", func(t *testing.T) {
		s := sessions[1]
		if s.NativeID != "beta:2026-07-01T08-00-00.000Z" {
			t.Fatalf("NativeID = %q", s.NativeID)
		}
		if s.Cwd != `C:\work\beta` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if s.Title != "Plan a REST API for books" || s.TitleSource != model.TitlePrompt {
			t.Errorf("Title = %q (%s)", s.Title, s.TitleSource)
		}
		if s.Coverage != model.CoverageFull || s.Origin != model.OriginLive || s.Root != "" {
			t.Errorf("coverage %s origin %s root %q", s.Coverage, s.Origin, s.Root)
		}
		if !s.StartedAt.Equal(utc("2026-07-01T08:00:00Z")) || !s.UpdatedAt.Equal(utc("2026-07-01T21:05:10Z")) {
			t.Errorf("times %s .. %s", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []wantMsg{
			{model.RoleUser, model.KindText, "Plan a REST API for books", "2026-07-01T08:00:30Z"},
			{model.RoleAssistant, model.KindText, "Here is the plan.\n\nFirst, the routes.", "2026-07-01T08:02:00Z"},
			{model.RoleAssistant, model.KindToolCall, "write_file: api/routes.ts", "2026-07-01T08:02:00Z"},
			{model.RoleTool, model.KindToolOutput, "Wrote api/routes.ts", "2026-07-01T08:02:00Z"},
			{model.RoleAssistant, model.KindToolCall, "file-picker: find config files", "2026-07-01T08:02:00Z"},
			{model.RoleTool, model.KindToolOutput, "config.json, tsconfig.json", "2026-07-01T08:02:00Z"},
			{model.RoleAssistant, model.KindText, "1. routes\n2. models", "2026-07-01T08:02:00Z"},
			{model.RoleAssistant, model.KindText, "Which database?\nAuth needed?", "2026-07-01T08:02:00Z"},
			{model.RoleAssistant, model.KindText, "Done.", "2026-07-01T08:02:00Z"},
			{model.RoleAssistant, model.KindText, "Late reply", "2026-07-01T20:58:00Z"},
			// 12:01 AM after 11:58 PM rolls over to the next local day.
			{model.RoleUser, model.KindText, "Thanks! شكرا جزيلا", "2026-07-01T21:01:00Z"},
			{model.RoleSystem, model.KindText, "Service unavailable", "2026-07-01T21:05:00Z"},
			{model.RoleSystem, model.KindText, "Switched to max mode", "2026-07-01T21:05:10Z"},
		})
	})

	t.Run("non-default root", func(t *testing.T) {
		s := sessions[2]
		if s.NativeID != "gamma:2026-08-01T12-00-00.000Z" {
			t.Fatalf("NativeID = %q", s.NativeID)
		}
		if want := filepath.Join(env.Home, ".config", "manicode-dev"); s.Root != want {
			t.Errorf("Root = %q, want %q", s.Root, want)
		}
		if s.Cwd != "" {
			t.Errorf("Cwd = %q, want empty (a first prompt carries no previous run state)", s.Cwd)
		}
		if !s.StartedAt.Equal(utc("2026-08-01T12:00:00Z")) || !s.UpdatedAt.Equal(utc("2026-08-01T12:00:20Z")) {
			t.Errorf("times %s .. %s", s.StartedAt, s.UpdatedAt)
		}
		checkMessages(t, s.Messages, []wantMsg{
			{model.RoleUser, model.KindText, "Explain gamma", "2026-08-01T12:00:10Z"},
			{model.RoleAssistant, model.KindText, "Gamma is the third letter.", "2026-08-01T12:00:20Z"},
		})
	})

	t.Run("prompt history", func(t *testing.T) {
		s := sessions[3]
		if s.NativeID != "message-history" {
			t.Fatalf("NativeID = %q", s.NativeID)
		}
		if s.Coverage != model.CoveragePrompts || s.Origin != model.OriginPromptLog || s.Cwd != "" || s.Root != "" {
			t.Errorf("coverage %s origin %s cwd %q root %q", s.Coverage, s.Origin, s.Cwd, s.Root)
		}
		if s.Title != "What is the capital of Mars?" || s.TitleSource != model.TitlePrompt {
			t.Errorf("Title = %q (%s)", s.Title, s.TitleSource)
		}
		_, mtime := statFile(t, filepath.Join(env.Home, ".config", "manicode", "message-history.json"))
		if !s.StartedAt.Equal(mtime) || !s.UpdatedAt.Equal(mtime) || s.UpdatedAt.Location() != time.UTC {
			t.Errorf("times %s .. %s, want file mtime %s", s.StartedAt, s.UpdatedAt, mtime)
		}
		// Prompts that belong to a chat on disk are not repeated here.
		checkMessages(t, s.Messages, []wantMsg{
			{model.RoleUser, model.KindText, "What is the capital of Mars?", ""},
		})
		if !s.Messages[0].At.IsZero() {
			t.Errorf("prompt history has no times; At = %s", s.Messages[0].At)
		}
	})
}

func TestReadIsReadOnly(t *testing.T) {
	fixtureClock(t)
	before := snapshot(t, "testdata")
	adapterstest.ReadAll(t, New(), fixtureEnv(t))
	after := snapshot(t, "testdata")
	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	for p, b := range before {
		if a, ok := after[p]; !ok || a != b {
			t.Errorf("%s changed: %v -> %v", p, b, a)
		}
	}
}

func TestLargeRecord(t *testing.T) {
	fixtureClock(t)
	dir := t.TempDir()
	env := adapterstest.FixtureEnv(t, dir, nil)
	chat := filepath.Join(env.Home, ".config", "manicode", "projects", "big", "chats", "2026-03-03T03-03-03.003Z")
	big := strings.Repeat("x", 2<<20)
	log := `{"level":"INFO","msg":"[send-message] Sending message with sdk run config","data":{"runConfig":{"agent":"base2","prompt":"` + big + `"}},"timestamp":"2026-03-03T03:03:04.000Z"}` + "\n" +
		`{"level":"DEBUG","msg":"Start agent base2 step 1 (bigRun - x)","data":{},"timestamp":"2026-03-03T03:03:05.000Z"}` + "\n" +
		`{"level":"DEBUG","msg":"End agent base2 step 1 (bigRun - x)","data":{"fullResponse":"` + big + `","toolCalls":[{"toolName":"read_files","toolCallId":"r1","input":{"paths":["a.go","b.go"]}}],"toolResults":[{"toolCallId":"r1","toolName":"read_files","content":[{"type":"json","value":"` + big + `"}]}]},"timestamp":"2026-03-03T03:03:06.000Z"}` + "\n"
	mustWrite(t, filepath.Join(chat, "log.jsonl"), log)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 0 || len(sessions) != 1 {
		t.Fatalf("skipped %d, sessions %d", skipped, len(sessions))
	}
	msgs := sessions[0].Messages
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(msgs))
	}
	if len(msgs[0].Text) != len(big) || len(msgs[1].Text) != len(big) {
		t.Errorf("2 MB texts not kept whole: %d, %d", len(msgs[0].Text), len(msgs[1].Text))
	}
	if msgs[2].Text != "read_files: a.go b.go" {
		t.Errorf("tool call = %q", msgs[2].Text)
	}
	if msgs[3].Kind != model.KindToolOutput || len(msgs[3].Text) != model.MaxToolOutput {
		t.Errorf("tool output kind %s len %d, want %d bytes", msgs[3].Kind, len(msgs[3].Text), model.MaxToolOutput)
	}
	if n := len([]rune(sessions[0].Title)); n != 100 {
		t.Errorf("title has %d runes, want 100", n)
	}
}

func TestChatWithLogPrefersChatMessages(t *testing.T) {
	fixtureClock(t)
	dir := t.TempDir()
	env := adapterstest.FixtureEnv(t, dir, nil)
	chat := filepath.Join(env.Home, ".config", "manicode", "projects", "both", "chats", "2026-04-04T04-04-04.004Z")
	mustWrite(t, filepath.Join(chat, "chat-messages.json"), `[{"id":"user-1775275445000","variant":"user","content":"from chat messages","timestamp":"2026-04-04T04:04:05Z"}]`)
	mustWrite(t, filepath.Join(chat, "log.jsonl"), `{"level":"INFO","msg":"[send-message] Sending message with sdk run config","data":{"runConfig":{"agent":"base2","prompt":"from the log"}},"timestamp":"2026-04-04T04:04:05.000Z"}`+"\n")
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("sources %d err %v", len(srcs), err)
	}
	if len(srcs[0].Files) != 1 || filepath.Base(srcs[0].Files[0]) != "chat-messages.json" {
		t.Errorf("Files = %v", srcs[0].Files)
	}
	sessions, _ := adapterstest.ReadAll(t, New(), env)
	if len(sessions) != 1 || sessions[0].Coverage != model.CoverageFull || len(sessions[0].Messages) != 1 || sessions[0].Messages[0].Text != "from chat messages" {
		t.Fatalf("sessions = %+v", sessions)
	}
	if sessions[0].Cwd != "" {
		t.Errorf("Cwd = %q without run-state.json", sessions[0].Cwd)
	}
}

func TestRecentWholeFileJSONIsSkipped(t *testing.T) {
	dir := t.TempDir()
	env := adapterstest.FixtureEnv(t, dir, nil)
	root := filepath.Join(env.Home, ".config", "manicode")
	chat := filepath.Join(root, "projects", "p", "chats", "2026-04-04T04-04-04.004Z")
	mustWrite(t, filepath.Join(chat, "chat-messages.json"), `[{"variant":"user","content":"hello there","timestamp":"2026-04-04T04:04:05Z"}]`)
	mustWrite(t, filepath.Join(root, "message-history.json"), `["another prompt"]`)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 2 {
		t.Fatalf("sources %d err %v", len(srcs), err)
	}
	oldNow := now
	t.Cleanup(func() { now = oldNow })
	for _, src := range srcs {
		_, mtime := statFile(t, src.Files[0])
		now = func() time.Time { return mtime.Add(2 * time.Second) }
		if _, err := New().Read(context.Background(), env, src); !errors.Is(err, ErrRecentlyModified) || !errors.Is(err, adapters.ErrRetryLater) {
			t.Errorf("%s modified 2 s ago: err = %v, want ErrRecentlyModified wrapping adapters.ErrRetryLater", src.Path, err)
		}
		now = func() time.Time { return mtime.Add(10 * time.Second) }
		res, err := New().Read(context.Background(), env, src)
		if err != nil || len(res.Sessions) != 1 {
			t.Errorf("%s modified 10 s ago: %d sessions, err %v", src.Path, len(res.Sessions), err)
		}
	}
}

func TestInvalidChatMessagesIsAnError(t *testing.T) {
	fixtureClock(t)
	dir := t.TempDir()
	env := adapterstest.FixtureEnv(t, dir, nil)
	chat := filepath.Join(env.Home, ".config", "manicode", "projects", "p", "chats", "2026-04-04T04-04-04.004Z")
	mustWrite(t, filepath.Join(chat, "chat-messages.json"), `[{"variant":"user","content":"torn`)
	srcs, _ := New().Sources(env)
	if len(srcs) != 1 {
		t.Fatalf("sources = %d", len(srcs))
	}
	if _, err := New().Read(context.Background(), env, srcs[0]); err == nil {
		t.Fatal("torn chat-messages.json: want an error so the source is read again next sync")
	}
}

func TestTimeOfDay(t *testing.T) {
	cases := []struct {
		in     string
		minute int
		ok     bool
	}{
		{"07:23 PM", 19*60 + 23, true},
		{"7:23 pm", 19*60 + 23, true},
		{"19:23", 19*60 + 23, true},
		{"12:00 AM", 0, true},
		{"12:30 PM", 12*60 + 30, true},
		{"11:05 a.m.", 11*60 + 5, true},
		{"٠٧:٢٣ م", 19*60 + 23, true},
		{"٠٧:٢٣ ص", 7*60 + 23, true},
		{"", 0, false},
		{"soon", 0, false},
		{"25:00", 0, false},
	}
	for _, c := range cases {
		m, ok := parseTimeOfDay(c.in)
		if ok != c.ok || (ok && m != c.minute) {
			t.Errorf("parseTimeOfDay(%q) = %d, %v; want %d, %v", c.in, m, ok, c.minute, c.ok)
		}
	}
}

func TestChatDirTime(t *testing.T) {
	cases := map[string]string{
		"2026-05-16T19-23-40.000Z": "2026-05-16T19:23:40Z",
		"2026-05-16T19-23-49Z":     "2026-05-16T19:23:49Z",
		"2026-01-02T03-04-05.678Z": "2026-01-02T03:04:05.678Z",
		"not-a-time":               "",
	}
	for in, want := range cases {
		got := chatDirTime(in)
		if want == "" {
			if !got.IsZero() {
				t.Errorf("chatDirTime(%q) = %s, want zero", in, got)
			}
			continue
		}
		if !got.Equal(utc(want)) || got.Location() != time.UTC {
			t.Errorf("chatDirTime(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestToolCallText(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"run_terminal_command", `{"command":"go test ./...","process_type":"SYNC"}`, "run_terminal_command: go test ./..."},
		{"read_files", `{"paths":["a.go","b/c.go"]}`, "read_files: a.go b/c.go"},
		{"str_replace", `{"path":"x.go","replacements":[{"old":"a","new":"b"}]}`, "str_replace: x.go"},
		{"code_search", `{"pattern":"func main","flags":"-n"}`, "code_search: func main"},
		{"web_search", `{"query":"go generics"}`, "web_search: go generics"},
		{"chrome-devtools__navigate_page", `{"url":"http://localhost:3000"}`, "chrome-devtools__navigate_page: http://localhost:3000"},
		{"spawn_agents", `{"agents":[{"agent_type":"basher","prompt":"run tests"},{"agent_type":"file-picker"}]}`, "spawn_agents: basher: run tests; file-picker"},
		{"think_deeply", `{"x":1}`, `think_deeply: {"x":1}`},
		{"end_turn", `{}`, "end_turn"},
		{"end_turn", ``, "end_turn"},
		{"", `null`, "tool"},
	}
	for _, c := range cases {
		if got := toolCallText(c.name, []byte(c.input)); got != c.want {
			t.Errorf("toolCallText(%q, %s) = %q, want %q", c.name, c.input, got, c.want)
		}
	}
}

func TestHistoryNonDefaultRoot(t *testing.T) {
	fixtureClock(t)
	dir := t.TempDir()
	env := adapterstest.FixtureEnv(t, dir, nil)
	root := filepath.Join(env.Home, ".config", "manicode-staging")
	mustWrite(t, filepath.Join(root, "message-history.json"), `["only here", "only here", "  "]`)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 0 || len(sessions) != 1 {
		t.Fatalf("skipped %d, sessions %d", skipped, len(sessions))
	}
	s := sessions[0]
	if s.NativeID != "message-history:manicode-staging" || s.Root != root || s.Coverage != model.CoveragePrompts {
		t.Errorf("NativeID %q Root %q Coverage %s", s.NativeID, s.Root, s.Coverage)
	}
	checkMessages(t, s.Messages, []wantMsg{{model.RoleUser, model.KindText, "only here", ""}})
}

func TestIDTime(t *testing.T) {
	cases := map[string]string{
		"user-1782892830000":    "2026-07-01T08:00:30Z",
		"ai-1782892830123-9f3a": "2026-07-01T08:00:30.123Z",
		"divider-1782892830000": "2026-07-01T08:00:30Z",
		"ai-response-1":         "",
		"bash-result-toolu_abc": "",
		"1782892830000":         "",
		"user-0000000000001":    "",
	}
	for in, want := range cases {
		got := idTime(in)
		if want == "" {
			if !got.IsZero() {
				t.Errorf("idTime(%q) = %s, want zero", in, got)
			}
			continue
		}
		if !got.Equal(utc(want)) {
			t.Errorf("idTime(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }

// ---- helpers ----

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func statFile(t *testing.T, path string) (int64, time.Time) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size(), info.ModTime().UTC()
}

type fileState struct {
	size  int64
	mtime time.Time
}

func snapshot(t *testing.T, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() { // directory mtimes change lazily on Windows; compare names only
			out[p] = fileState{}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fileState{info.Size(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
