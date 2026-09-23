package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

const (
	idAlpha   = "11111111-1111-4111-8111-111111111111"
	idParser  = "22222222-2222-4222-8222-222222222222"
	idBeta    = "33333333-3333-4333-8333-333333333333"
	idSDK     = "44444444-4444-4444-8444-444444444444"
	idGamma   = "55555555-5555-4555-8555-555555555555"
	idHistory = "66666666-6666-4666-8666-666666666666"
	idEmpty   = "77777777-7777-4777-8777-777777777777"
	idDelta   = "88888888-8888-4888-8888-888888888888"
)

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func utc(s string) time.Time {
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return tm.UTC()
}

// find returns the only session with the given id and origin.
func find(t *testing.T, sessions []model.Session, id string, origin model.Origin) model.Session {
	t.Helper()
	var hits []model.Session
	for _, s := range sessions {
		if s.NativeID == id && s.Origin == origin {
			hits = append(hits, s)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one session %s (%s), got %d", id, origin, len(hits))
	}
	return hits[0]
}

// msg builds an expected message.
func msg(role model.Role, kind model.Kind, at time.Time, text string) model.Message {
	return model.Message{Role: role, Kind: kind, At: at, Text: text}
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "claude" || a.Name() != "Claude Code" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	want := adapters.Caps{
		Resume:            []string{"claude", "--resume", "{id}"},
		ProgramCandidates: []string{`%USERPROFILE%\.local\bin\claude.exe`},
		Fork:              []string{"--fork-session"},
		SkipPermissions:   []string{"--dangerously-skip-permissions"},
		RootEnv:           "CLAUDE_CONFIG_DIR",
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("Caps = %+v\nwant %+v", c, want)
	}
}

func TestSources(t *testing.T) {
	root := fixtureRoot(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(root, "home", ".claude-local")
	type row struct{ rel, root string }
	var got []row
	for _, s := range srcs {
		if s.Adapter != "claude" {
			t.Errorf("source %s: Adapter = %q", s.Path, s.Adapter)
		}
		if !reflect.DeepEqual(s.Files, []string{s.Path}) {
			t.Errorf("source %s: Files = %v", s.Path, s.Files)
		}
		rel, err := filepath.Rel(root, s.Path)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, row{filepath.ToSlash(rel), s.Root})
	}
	sort.Slice(got, func(i, j int) bool { return got[i].rel < got[j].rel })
	want := []row{
		{"home/.claude-local/history.jsonl", local},
		{"home/.claude-local/projects/C--work-gamma/" + idGamma + ".jsonl", local},
		{"home/.claude/history.jsonl", ""},
		{"home/.claude/projects/C--work-alpha/" + idAlpha + ".jsonl", ""},
		{"home/.claude/projects/C--work-alpha/" + idParser + ".jsonl", ""},
		{"home/.claude/projects/C--work-beta/" + idBeta + ".jsonl", ""},
		{"home/.claude/projects/C--work-beta/" + idSDK + ".jsonl", ""},
		{"home/.claude/projects/C--work-beta/" + idEmpty + ".jsonl", ""},
	}
	sort.Slice(want, func(i, j int) bool { return want[i].rel < want[j].rel })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sources:\n got %v\nwant %v", got, want)
	}
}

func TestSourcesMissingHome(t *testing.T) {
	env := adapterstest.FixtureEnv(t, filepath.Join(t.TempDir(), "nothing"), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on a missing home = %v, %v; want none, nil", srcs, err)
	}
}

func TestConfigDirEnv(t *testing.T) {
	root := fixtureRoot(t)
	alt := filepath.Join(root, "altconfig")

	// A CLAUDE_CONFIG_DIR outside the home folder is an extra root.
	env := adapterstest.FixtureEnv(t, root, map[string]string{"CLAUDE_CONFIG_DIR": alt + string(os.PathSeparator)})
	sessions, _ := adapterstest.ReadAll(t, New(), env)
	delta := find(t, sessions, idDelta, model.OriginLive)
	if delta.Root != alt || delta.Cwd != `E:\delta` || len(delta.Messages) != 2 {
		t.Fatalf("delta session: Root=%q Cwd=%q messages=%d", delta.Root, delta.Cwd, len(delta.Messages))
	}

	// CLAUDE_CONFIG_DIR pointing at the default root adds nothing and keeps Root "".
	def := filepath.Join(root, "home", ".claude")
	env2 := adapterstest.FixtureEnv(t, root, map[string]string{"CLAUDE_CONFIG_DIR": def})
	base, err := New().Sources(adapterstest.FixtureEnv(t, root, nil))
	if err != nil {
		t.Fatal(err)
	}
	srcs, err := New().Sources(env2)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != len(base) {
		t.Fatalf("CLAUDE_CONFIG_DIR=default root: %d sources, want %d", len(srcs), len(base))
	}
	for _, s := range srcs {
		if strings.HasPrefix(s.Path, def+string(os.PathSeparator)) && s.Root != "" {
			t.Errorf("%s: Root = %q, want default", s.Path, s.Root)
		}
	}
}

func TestReadFixtureTree(t *testing.T) {
	root := fixtureRoot(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)

	// 4 transcripts (alpha, parser, beta, gamma) + 3 prompt-log groups (alpha, history-only, gamma).
	// Skipped: sdk-cli session, metadata-only session, subagents, non-UUID files.
	type key struct {
		id     string
		origin model.Origin
	}
	var keys []key
	for _, s := range sessions {
		keys = append(keys, key{s.NativeID, s.Origin})
		if s.Agent != "claude" {
			t.Errorf("%s: Agent = %q", s.NativeID, s.Agent)
		}
	}
	wantKeys := []key{
		{idAlpha, model.OriginLive}, {idAlpha, model.OriginPromptLog},
		{idParser, model.OriginLive},
		{idBeta, model.OriginLive},
		{idGamma, model.OriginLive}, {idGamma, model.OriginPromptLog},
		{idHistory, model.OriginPromptLog},
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].id != keys[j].id {
			return keys[i].id < keys[j].id
		}
		return keys[i].origin < keys[j].origin
	})
	sort.Slice(wantKeys, func(i, j int) bool {
		if wantKeys[i].id != wantKeys[j].id {
			return wantKeys[i].id < wantKeys[j].id
		}
		return wantKeys[i].origin < wantKeys[j].origin
	})
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("sessions:\n got %v\nwant %v", keys, wantKeys)
	}
	// One broken line in the parser transcript, one broken line and one line
	// without a session id in history.jsonl. The half-written last line of the
	// parser transcript is not delivered and not counted.
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}

	for _, s := range sessions {
		for _, m := range s.Messages {
			for _, bad := range []string{"SYNTHETIC_THINKING", "SYNTHETIC_SUBAGENT", "SYNTHETIC_SIDECHAIN",
				"SYNTHETIC_COMPACT", "SYNTHETIC_META", "SYNTHETIC_ARGS", "SYNTHETIC_REMINDER", "SYNTHETIC_STDERR",
				"SYNTHETIC_HEADLESS", "SYNTHETIC_NON_UUID", "SYNTHETIC_PARTIAL", "SYNTHETIC_TOOL_RESULT_FILE",
				"system-reminder", "task-notification", "command-name", "Request interrupted", "bash-stdout", "Caveat:"} {
				if strings.Contains(m.Text, bad) {
					t.Errorf("%s: message %q contains %q", s.NativeID, m.Text, bad)
				}
			}
		}
	}
}

func TestAlphaSession(t *testing.T) {
	env := adapterstest.FixtureEnv(t, fixtureRoot(t), nil)
	sessions, _ := adapterstest.ReadAll(t, New(), env)
	s := find(t, sessions, idAlpha, model.OriginLive)

	if s.Cwd != `C:\work\alpha` {
		t.Errorf("Cwd = %q, want the FIRST cwd", s.Cwd)
	}
	if s.Title != "My alpha work" || s.TitleSource != model.TitleUser {
		t.Errorf("title = %q (%s), want the custom title", s.Title, s.TitleSource)
	}
	if s.Coverage != model.CoverageFull || s.Root != "" || s.Machine != "" {
		t.Errorf("Coverage=%q Root=%q Machine=%q", s.Coverage, s.Root, s.Machine)
	}
	if !s.StartedAt.Equal(utc("2026-09-01T10:00:00Z")) || !s.UpdatedAt.Equal(utc("2026-09-01T10:30:05Z")) {
		t.Errorf("StartedAt=%v UpdatedAt=%v", s.StartedAt, s.UpdatedAt)
	}
	if s.StartedAt.Location() != time.UTC || s.UpdatedAt.Location() != time.UTC {
		t.Errorf("times must be UTC")
	}
	want := []model.Message{
		msg(model.RoleUser, model.KindText, utc("2026-09-01T10:01:00Z"), "Add a login page to the alpha app"),
		msg(model.RoleAssistant, model.KindText, utc("2026-09-01T10:01:06Z"), "I will inspect the project first."),
		msg(model.RoleAssistant, model.KindToolCall, utc("2026-09-01T10:01:07Z"), "Bash: ls -la"),
		msg(model.RoleTool, model.KindToolOutput, utc("2026-09-01T10:01:08Z"), "total 0\nfile.txt"),
		msg(model.RoleAssistant, model.KindToolCall, utc("2026-09-01T10:02:00Z"), `Read: C:\work\alpha\main.go`),
		msg(model.RoleTool, model.KindToolOutput, utc("2026-09-01T10:02:01Z"), "package main"),
		msg(model.RoleAssistant, model.KindToolCall, utc("2026-09-01T10:03:00Z"), "Grep: TODO"),
		msg(model.RoleAssistant, model.KindToolCall, utc("2026-09-01T10:04:00Z"), `WebSearch: {"query":"go login example"}`),
		msg(model.RoleUser, model.KindText, utc("2026-09-01T10:05:00Z"), "!git status"),
		msg(model.RoleUser, model.KindText, utc("2026-09-01T10:06:00Z"), "Also add a logout button"),
		msg(model.RoleAssistant, model.KindText, utc("2026-09-01T10:30:00Z"), "Done."),
	}
	if !reflect.DeepEqual(s.Messages, want) {
		t.Errorf("messages:")
		for i := 0; i < len(s.Messages) || i < len(want); i++ {
			var g, w model.Message
			if i < len(s.Messages) {
				g = s.Messages[i]
			}
			if i < len(want) {
				w = want[i]
			}
			mark := "  "
			if !reflect.DeepEqual(g, w) {
				mark = "!!"
			}
			t.Logf("%s %d got  %+v\n      want %+v", mark, i, g, w)
		}
	}
}

func TestTitlesAndPrompts(t *testing.T) {
	root := fixtureRoot(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	sessions, _ := adapterstest.ReadAll(t, New(), env)

	// ai-title only: the LAST one wins.
	p := find(t, sessions, idParser, model.OriginLive)
	if p.Title != "Parser test fix v2" || p.TitleSource != model.TitleAgent {
		t.Errorf("parser title = %q (%s)", p.Title, p.TitleSource)
	}
	if len(p.Messages) != 2 || !p.UpdatedAt.Equal(utc("2026-09-02T08:05:00Z")) {
		t.Errorf("parser: %d messages, UpdatedAt %v", len(p.Messages), p.UpdatedAt)
	}

	// No title records: the first real prompt, whitespace collapsed, 100 runes.
	b := find(t, sessions, idBeta, model.OriginLive)
	full := "مرحبا Please refactor the beta module: " + strings.Repeat("abcdefghij", 10)
	wantTitle := string([]rune(full)[:100])
	if b.Title != wantTitle || b.TitleSource != model.TitlePrompt {
		t.Errorf("beta title = %q (%s)\nwant %q", b.Title, b.TitleSource, wantTitle)
	}
	if b.Cwd != `D:\work\beta` || len(b.Messages) != 2 || b.Messages[0].Role != model.RoleUser {
		t.Errorf("beta: Cwd=%q messages=%+v", b.Cwd, b.Messages)
	}
	if !strings.HasPrefix(b.Messages[0].Text, "مرحبا   Please   refactor\n\nthe beta module:") {
		t.Errorf("message text must be kept verbatim, got %q", b.Messages[0].Text)
	}

	// agent-name (user tier) beats ai-title and summary; second root sets Root.
	g := find(t, sessions, idGamma, model.OriginLive)
	local := filepath.Join(root, "home", ".claude-local")
	if g.Title != "gamma-helper" || g.TitleSource != model.TitleUser || g.Root != local {
		t.Errorf("gamma: title %q (%s) root %q", g.Title, g.TitleSource, g.Root)
	}
	// Assistant records without message.role are still assistant messages.
	if len(g.Messages) != 2 || g.Messages[1].Role != model.RoleAssistant || g.Messages[1].Text != "Summary ready." {
		t.Errorf("gamma messages = %+v", g.Messages)
	}
}

func TestHistoryPromptLog(t *testing.T) {
	root := fixtureRoot(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	sessions, _ := adapterstest.ReadAll(t, New(), env)

	h := find(t, sessions, idHistory, model.OriginPromptLog)
	if h.Coverage != model.CoveragePrompts || h.Cwd != `C:\old\dashboard` || h.Root != "" {
		t.Errorf("history session: coverage %q cwd %q root %q", h.Coverage, h.Cwd, h.Root)
	}
	// "/clear" is not a real prompt, so the title is the next one.
	if h.Title != "Build the old dashboard" || h.TitleSource != model.TitlePrompt {
		t.Errorf("history title = %q (%s)", h.Title, h.TitleSource)
	}
	want := []model.Message{
		msg(model.RoleUser, model.KindText, time.UnixMilli(1756000000000).UTC(), "/clear"),
		msg(model.RoleUser, model.KindText, time.UnixMilli(1756000060000).UTC(), "Build the old dashboard"),
		msg(model.RoleUser, model.KindText, time.UnixMilli(1756000120000).UTC(), "Add charts [Pasted text #1 +3 lines]"),
	}
	if !reflect.DeepEqual(h.Messages, want) {
		t.Errorf("history messages = %+v\nwant %+v", h.Messages, want)
	}
	if !h.StartedAt.Equal(time.UnixMilli(1756000000000)) || !h.UpdatedAt.Equal(time.UnixMilli(1756000120000)) {
		t.Errorf("history times %v .. %v", h.StartedAt, h.UpdatedAt)
	}
	if h.StartedAt.Location() != time.UTC {
		t.Errorf("history times must be UTC")
	}

	a := find(t, sessions, idAlpha, model.OriginPromptLog)
	if len(a.Messages) != 2 || a.Cwd != `C:\work\alpha` || a.Coverage != model.CoveragePrompts {
		t.Errorf("alpha prompt-log: %+v", a)
	}
	g := find(t, sessions, idGamma, model.OriginPromptLog)
	if g.Root != filepath.Join(root, "home", ".claude-local") {
		t.Errorf("gamma prompt-log Root = %q", g.Root)
	}

	// The history source reports the line without a session id.
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		if s.Path != filepath.Join(root, "home", ".claude", "history.jsonl") {
			continue
		}
		res, err := New().Read(context.Background(), env, s)
		if err != nil {
			t.Fatal(err)
		}
		if res.Skipped != 2 || len(res.Warnings) != 1 {
			t.Errorf("history: skipped %d warnings %v", res.Skipped, res.Warnings)
		}
	}
}

// TestLargeRecord proves a single 2 MB JSONL record is read whole and that
// tool outputs are cut to model.MaxToolOutput bytes on a UTF-8 boundary.
func TestLargeRecord(t *testing.T) {
	root := t.TempDir()
	const id = "99999999-9999-4999-8999-999999999999"
	dir := filepath.Join(root, "home", ".claude", "projects", "C--big")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("large prompt ", 2<<20/13+1)
	output := "x" + strings.Repeat("ب", 2<<20/2) // odd prefix: a byte cut would split a rune
	rec := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	content := rec(map[string]any{
		"type": "user", "cwd": `C:\big`, "sessionId": id, "timestamp": "2026-09-07T10:00:00Z", "entrypoint": "cli",
		"message": map[string]any{"role": "user", "content": big},
	}) + rec(map[string]any{
		"type": "assistant", "cwd": `C:\big`, "sessionId": id, "timestamp": "2026-09-07T10:00:01Z",
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "toolu_big", "name": "Bash", "input": map[string]any{"command": strings.Repeat("echo big && ", 1000)}},
		}},
	}) + rec(map[string]any{
		"type": "user", "cwd": `C:\big`, "sessionId": id, "timestamp": "2026-09-07T10:00:02Z",
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_big", "content": output},
		}},
	})
	if len(content) < 4<<20 {
		t.Fatalf("fixture too small: %d", len(content))
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	env := adapterstest.FixtureEnv(t, root, nil)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 0 || len(sessions) != 1 {
		t.Fatalf("sessions=%d skipped=%d", len(sessions), skipped)
	}
	s := sessions[0]
	if len(s.Messages) != 3 {
		t.Fatalf("messages = %d", len(s.Messages))
	}
	if s.Messages[0].Text != strings.TrimSpace(big) {
		t.Errorf("the 2 MB prompt was not kept whole (len %d)", len(s.Messages[0].Text))
	}
	if n := utf8.RuneCountInString(s.Title); n != 100 {
		t.Errorf("title has %d runes, want 100", n)
	}
	for _, m := range s.Messages[1:] {
		if len(m.Text) > model.MaxToolOutput || !utf8.ValidString(m.Text) {
			t.Errorf("%s: %d bytes, valid UTF-8 %v", m.Kind, len(m.Text), utf8.ValidString(m.Text))
		}
	}
	if !strings.HasPrefix(s.Messages[1].Text, "Bash: echo big && ") {
		t.Errorf("tool call = %.40q", s.Messages[1].Text)
	}
	if s.Messages[2].Kind != model.KindToolOutput || len(s.Messages[2].Text) < model.MaxToolOutput-4 {
		t.Errorf("tool output kind %s len %d", s.Messages[2].Kind, len(s.Messages[2].Text))
	}
}

func TestReadCancelled(t *testing.T) {
	root := fixtureRoot(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	src := adapters.Source{
		Adapter: "claude",
		Path:    filepath.Join(root, "home", ".claude", "projects", "C--work-alpha", idAlpha+".jsonl"),
	}
	src.Files = []string{src.Path}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Read(ctx, env, src); err == nil {
		t.Fatal("Read with a cancelled context should fail")
	}
}

func TestReadMissingFile(t *testing.T) {
	env := adapterstest.FixtureEnv(t, fixtureRoot(t), nil)
	p := filepath.Join(t.TempDir(), idAlpha+".jsonl")
	if _, err := New().Read(context.Background(), env, adapters.Source{Adapter: "claude", Path: p, Files: []string{p}}); err == nil {
		t.Fatal("Read of a missing file should return an error")
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
