package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

// Fixture session ids (synthetic UUIDs).
const (
	idCLI      = "0199b000-0000-7000-8000-000000000001" // full CLI session
	idVSCode   = "0199b000-0000-7000-8000-000000000002" // VS Code session with IDE context
	idEmpty    = "0199b000-0000-7000-8000-000000000003" // only injected context: skipped
	idSubagent = "0199b000-0000-7000-8000-000000000004" // sub-agent: skipped
	idExec     = "0199b000-0000-7000-8000-000000000005" // codex exec: skipped
	idNoEvents = "0199b000-0000-7000-8000-000000000006" // response items only (no event_msg)
	idArchived = "0199b000-0000-7000-8000-000000000007" // archived_sessions, thread name events
	idHistOnly = "0199b000-0000-7000-8000-000000000008" // prompt log only
	idAltRoot  = "0199b000-0000-7000-8000-000000000009" // CODEX_HOME root
	idLegacy   = "0199b000-0000-7000-8000-00000000000a" // pre-2025-08 rollout format
)

type wantMsg struct {
	Role model.Role
	Kind model.Kind
	Text string
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func msgsOf(s model.Session) []wantMsg {
	out := make([]wantMsg, len(s.Messages))
	for i, m := range s.Messages {
		out[i] = wantMsg{m.Role, m.Kind, m.Text}
	}
	return out
}

func checkMsgs(t *testing.T, s model.Session, want []wantMsg) {
	t.Helper()
	got := msgsOf(s)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s messages:\n got  %q\n want %q", s.NativeID, got, want)
	}
}

// byKey indexes sessions by origin + native id.
func byKey(t *testing.T, sessions []model.Session) map[string]model.Session {
	t.Helper()
	m := map[string]model.Session{}
	for _, s := range sessions {
		k := string(s.Origin) + "/" + s.NativeID
		if _, dup := m[k]; dup {
			t.Errorf("duplicate session %s", k)
		}
		m[k] = s
	}
	return m
}

func fixtureCodexDir() string {
	return filepath.Join("testdata", "home", ".codex")
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "codex" || a.Name() != "Codex" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if want := []string{"codex", "resume", "{id}"}; !reflect.DeepEqual(c.Resume, want) {
		t.Errorf("Resume = %q, want %q", c.Resume, want)
	}
	if want := []string{"--dangerously-bypass-approvals-and-sandbox"}; !reflect.DeepEqual(c.SkipPermissions, want) {
		t.Errorf("SkipPermissions = %q, want %q", c.SkipPermissions, want)
	}
	if c.Fork != nil || c.App != nil {
		t.Errorf("Fork/App must be nil: %q %v", c.Fork, c.App)
	}
	if c.RootEnv != "CODEX_HOME" {
		t.Errorf("RootEnv = %q", c.RootEnv)
	}
	if want := []string{`%APPDATA%\npm\codex.cmd`}; !reflect.DeepEqual(c.ProgramCandidates, want) {
		t.Errorf("ProgramCandidates = %q, want %q", c.ProgramCandidates, want)
	}
}

func TestSourcesFixture(t *testing.T) {
	env := adapterstest.FixtureEnv(t, "testdata", nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	root := fixtureCodexDir()
	want := []string{
		filepath.Join(root, "archived_sessions", "rollout-2025-12-01T20-00-00-"+idArchived+".jsonl"),
		filepath.Join(root, "sessions", "2025", "05", "10", "rollout-2025-05-10T12-00-00-"+idLegacy+".jsonl"),
		filepath.Join(root, "sessions", "2026", "01", "15", "rollout-2026-01-15T10-00-00-"+idCLI+".jsonl"),
		filepath.Join(root, "sessions", "2026", "01", "16", "rollout-2026-01-16T09-30-00-"+idVSCode+".jsonl"),
		filepath.Join(root, "sessions", "2026", "01", "17", "rollout-2026-01-17T08-00-00-"+idEmpty+".jsonl"),
		filepath.Join(root, "sessions", "2026", "01", "18", "rollout-2026-01-18T12-00-00-"+idSubagent+".jsonl"),
		filepath.Join(root, "sessions", "2026", "01", "18", "rollout-2026-01-18T12-05-00-"+idExec+".jsonl"),
		filepath.Join(root, "sessions", "2026", "01", "19", "rollout-2026-01-19T07-00-00-"+idNoEvents+".jsonl"),
		filepath.Join(root, "history.jsonl"), // prompt log last
	}
	var got []string
	for _, s := range srcs {
		got = append(got, s.Path)
		if s.Adapter != "codex" {
			t.Errorf("%s: Adapter = %q", s.Path, s.Adapter)
		}
		if !reflect.DeepEqual(s.Files, []string{s.Path}) {
			t.Errorf("%s: Files = %q", s.Path, s.Files)
		}
		if s.Root != "" {
			t.Errorf("%s: Root = %q, want default", s.Path, s.Root)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sources:\n got  %q\n want %q", got, want)
	}
}

func TestReadFixture(t *testing.T) {
	env := adapterstest.FixtureEnv(t, "testdata", nil)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 3 { // one bad rollout line, one bad history line, one history line without id
		t.Errorf("skipped = %d, want 3", skipped)
	}
	m := byKey(t, sessions)
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	wantKeys := []string{
		"live/" + idCLI, "live/" + idVSCode, "live/" + idNoEvents, "live/" + idArchived, "live/" + idLegacy,
		"prompt-log/" + idCLI, "prompt-log/" + idHistOnly,
	}
	sort.Strings(wantKeys)
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("sessions:\n got  %q\n want %q", keys, wantKeys)
	}
	for _, s := range sessions {
		if s.Agent != "codex" || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: Agent/Root/Machine = %q %q %q", s.NativeID, s.Agent, s.Root, s.Machine)
		}
		for _, msg := range s.Messages {
			if msg.Kind == model.KindToolOutput && len(msg.Text) > model.MaxToolOutput {
				t.Errorf("%s: tool output of %d bytes", s.NativeID, len(msg.Text))
			}
		}
	}

	t.Run("cli", func(t *testing.T) {
		s := m["live/"+idCLI]
		if s.Cwd != `C:\work\alpha` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if s.Title != "Fix the flaky test in parser_test.go please" || s.TitleSource != model.TitlePrompt {
			t.Errorf("Title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(utc("2026-01-15T08:00:00Z")) {
			t.Errorf("StartedAt = %v", s.StartedAt)
		}
		// the last complete line; the unterminated tail is left for the next sync
		if !s.UpdatedAt.Equal(utc("2026-01-15T08:06:30Z")) {
			t.Errorf("UpdatedAt = %v", s.UpdatedAt)
		}
		if s.StartedAt.Location() != time.UTC || s.UpdatedAt.Location() != time.UTC {
			t.Errorf("times not UTC")
		}
		if s.Coverage != model.CoverageFull || s.Origin != model.OriginLive {
			t.Errorf("Coverage/Origin = %s %s", s.Coverage, s.Origin)
		}
		checkMsgs(t, s, []wantMsg{
			{model.RoleUser, model.KindText, "Fix   the flaky\n test in parser_test.go please"},
			{model.RoleAssistant, model.KindToolCall, "shell: go test ./parser/..."},
			{model.RoleTool, model.KindToolOutput, "--- FAIL: TestParse (0.00s)\nFAIL\n"},
			{model.RoleAssistant, model.KindToolCall, "apply_patch: parser/parser.go, parser/helper.go"},
			{model.RoleTool, model.KindToolOutput, "Success. Updated the following files:\nM parser/parser.go\nA parser/helper.go\n"},
			{model.RoleAssistant, model.KindText, "The test was flaky because of map ordering; fixed."},
			{model.RoleUser, model.KindText, "اشرح التغيير بالعربية"},
			{model.RoleAssistant, model.KindToolCall, "shell: git diff --stat"},
			{model.RoleTool, model.KindToolOutput, "parser/parser.go | 2 +-"},
			{model.RoleAssistant, model.KindText, "رتّبت المفاتيح قبل المقارنة."},
		})
		if len(s.Messages) > 0 && !s.Messages[0].At.Equal(utc("2026-01-15T08:00:05Z")) {
			t.Errorf("first message At = %v", s.Messages[0].At)
		}
	})

	t.Run("vscode", func(t *testing.T) {
		s := m["live/"+idVSCode]
		if s.Cwd != `D:\proj\beta` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if s.Title != "Add a dark mode toggle" || s.TitleSource != model.TitlePrompt {
			t.Errorf("Title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(utc("2026-01-16T07:30:00Z")) || !s.UpdatedAt.Equal(utc("2026-01-16T07:30:20Z")) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		checkMsgs(t, s, []wantMsg{
			{model.RoleUser, model.KindText, "Add a dark mode toggle\n[image]"},
			{model.RoleAssistant, model.KindToolCall, "shell: npm run build"},
			{model.RoleTool, model.KindToolOutput, "build ok\n[image]"},
			{model.RoleAssistant, model.KindToolCall, "shell: dir"},
			{model.RoleAssistant, model.KindToolCall, "web_search: css prefers-color-scheme"},
			{model.RoleAssistant, model.KindToolCall, "exec_command: ls -la"},
			{model.RoleTool, model.KindToolOutput, "total 0"},
			{model.RoleAssistant, model.KindToolCall, "read_file: src/app.ts"},
			{model.RoleAssistant, model.KindToolCall, `docs__search: {"q":"theme tokens"}`},
			{model.RoleAssistant, model.KindText, "Added the toggle."},
		})
	})

	t.Run("no events falls back to response items", func(t *testing.T) {
		s := m["live/"+idNoEvents]
		if s.Cwd != `C:\work\gamma` { // session_meta has no cwd: first turn_context wins over environment_context
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if s.Title != "Rename the module" || s.TitleSource != model.TitlePrompt {
			t.Errorf("Title = %q (%s)", s.Title, s.TitleSource)
		}
		checkMsgs(t, s, []wantMsg{
			{model.RoleUser, model.KindText, "Rename the module"},
			{model.RoleAssistant, model.KindText, "Done."},
		})
	})

	t.Run("archived with thread name", func(t *testing.T) {
		s := m["live/"+idArchived]
		if s.Cwd != `C:\work\delta` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if s.Title != "Release checklist" || s.TitleSource != model.TitleAgent {
			t.Errorf("Title = %q (%s)", s.Title, s.TitleSource)
		}
		checkMsgs(t, s, []wantMsg{
			{model.RoleUser, model.KindText, "Old archived question"},
			{model.RoleAssistant, model.KindText, "Old answer"},
		})
	})

	t.Run("legacy format", func(t *testing.T) {
		s := m["live/"+idLegacy]
		if s.Cwd != `C:\legacy\proj` { // from <environment_context>
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if s.Title != "Legacy question" {
			t.Errorf("Title = %q", s.Title)
		}
		if !s.StartedAt.Equal(utc("2025-05-10T10:00:00Z")) {
			t.Errorf("StartedAt = %v", s.StartedAt)
		}
		if s.UpdatedAt.Before(s.StartedAt) { // no per-line times: file mtime
			t.Errorf("UpdatedAt %v before StartedAt %v", s.UpdatedAt, s.StartedAt)
		}
		checkMsgs(t, s, []wantMsg{
			{model.RoleUser, model.KindText, "Legacy question"},
			{model.RoleAssistant, model.KindToolCall, "shell: ls"},
			{model.RoleTool, model.KindToolOutput, "a.txt\n"},
			{model.RoleAssistant, model.KindText, "Legacy answer"},
		})
	})

	t.Run("prompt log", func(t *testing.T) {
		s := m["prompt-log/"+idCLI]
		if s.Coverage != model.CoveragePrompts || s.Origin != model.OriginPromptLog {
			t.Errorf("Coverage/Origin = %s %s", s.Coverage, s.Origin)
		}
		if s.Title != "Fix the flaky test in parser_test.go please" || s.TitleSource != model.TitlePrompt {
			t.Errorf("Title = %q (%s)", s.Title, s.TitleSource)
		}
		checkMsgs(t, s, []wantMsg{
			{model.RoleUser, model.KindText, "Fix   the flaky\n test in parser_test.go please"},
			{model.RoleUser, model.KindText, "اشرح التغيير بالعربية"},
		})
		if !s.StartedAt.Equal(time.Unix(1768464005, 0)) || !s.UpdatedAt.Equal(time.Unix(1768464300, 0)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		if s.Messages[1].At.Location() != time.UTC {
			t.Errorf("message time not UTC")
		}
		h := m["prompt-log/"+idHistOnly]
		if h.Cwd != "" || h.Title != "what does this regex do" || len(h.Messages) != 2 {
			t.Errorf("history-only session = %q %q %d msgs", h.Cwd, h.Title, len(h.Messages))
		}
	})
}

func TestCodexHomeRoot(t *testing.T) {
	alt, err := filepath.Abs(filepath.Join("testdata", "alt", "codexhome"))
	if err != nil {
		t.Fatal(err)
	}
	env := adapterstest.FixtureEnv(t, "testdata", map[string]string{"CODEX_HOME": alt})
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	var altSrcs, defSrcs int
	for _, s := range srcs {
		if strings.HasPrefix(s.Path, alt) {
			altSrcs++
			if s.Root != alt {
				t.Errorf("%s: Root = %q, want %q", s.Path, s.Root, alt)
			}
		} else {
			defSrcs++
			if s.Root != "" {
				t.Errorf("%s: Root = %q, want default", s.Path, s.Root)
			}
		}
	}
	if altSrcs != 1 || defSrcs != 9 {
		t.Errorf("sources: %d in CODEX_HOME, %d in default root; want 1 and 9", altSrcs, defSrcs)
	}
	sessions, _ := adapterstest.ReadAll(t, New(), env)
	m := byKey(t, sessions)
	s, ok := m["live/"+idAltRoot]
	if !ok {
		t.Fatalf("CODEX_HOME session missing")
	}
	if s.Root != alt || s.Cwd != `E:\other root\project` || len(s.Messages) != 2 {
		t.Errorf("alt session: Root %q Cwd %q msgs %d", s.Root, s.Cwd, len(s.Messages))
	}
}

func TestCodexHomeIsDefaultRoot(t *testing.T) {
	def, err := filepath.Abs(fixtureCodexDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{def, strings.ToUpper(def) + `\`} {
		env := adapterstest.FixtureEnv(t, "testdata", map[string]string{"CODEX_HOME": v})
		srcs, err := New().Sources(env)
		if err != nil {
			t.Fatal(err)
		}
		if len(srcs) != 9 {
			t.Errorf("CODEX_HOME=%s: %d sources, want 9 (no duplicates)", v, len(srcs))
		}
		for _, s := range srcs {
			if s.Root != "" {
				t.Errorf("CODEX_HOME=%s: %s has Root %q, want default", v, s.Path, s.Root)
			}
		}
	}
}

func TestMissingRoots(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), map[string]string{"CODEX_HOME": filepath.Join(t.TempDir(), "nope")})
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources = %v, %v; want none", srcs, err)
	}
}

func TestReadMissingSourceFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout-2026-01-01T00-00-00-"+idCLI+".jsonl")
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	_, err := New().Read(context.Background(), env, adapters.Source{Adapter: "codex", Path: p, Files: []string{p}})
	if err == nil {
		t.Fatal("reading a missing rollout must fail")
	}
}

func TestReadHonoursCancel(t *testing.T) {
	env := adapterstest.FixtureEnv(t, "testdata", nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Read(ctx, env, srcs[2]); err == nil {
		t.Fatal("Read with a cancelled context must fail")
	}
}

// snapshot records every file under root with its size and mtime.
// Folders are recorded by name only: NTFS updates a folder's timestamp lazily
// after files were created in it, which would make fresh temp trees flaky.
// A file or folder created by the reader still shows up as a new key.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[p] = "dir"
			return nil
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		out[p] = fmt.Sprintf("%s|%d", info.ModTime().UTC().Format(time.RFC3339Nano), info.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFixtureTreeUnchanged(t *testing.T) {
	before := snapshot(t, "testdata")
	env := adapterstest.FixtureEnv(t, "testdata", nil)
	adapterstest.ReadAll(t, New(), env)
	after := snapshot(t, "testdata")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("reading changed the fixture tree")
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
