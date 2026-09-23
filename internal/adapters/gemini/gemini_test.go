package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

const (
	idA     = "11111111-aaaa-4aaa-8aaa-000000000001"
	idB     = "22222222-bbbb-4bbb-8bbb-000000000002"
	idC     = "33333333-cccc-4ccc-8ccc-000000000003"
	idD     = "44444444-dddd-4ddd-8ddd-000000000004"
	idSub   = "55555555-eeee-4eee-8eee-000000000005"
	idSubF  = "66666666-ffff-4fff-8fff-000000000006"
	idLog7  = "77777777-7777-4777-8777-000000000007"
	idHash8 = "88888888-8888-4888-8888-000000000008"
	idHash9 = "99999999-9999-4999-8999-000000000009"
	idEmpty = "12121212-1212-4212-8212-000000000012"
	idAlt   = "aaaaaaaa-aaaa-4aaa-8aaa-00000000000a"

	cwdAlpha = `z:\athar-fixture\alpha`
	cwdBeta  = `z:\athar-fixture\beta`
	cwdDelta = `z:\athar-fixture\delta`
)

// freezeNow makes whole-file JSON sources look old enough to read, so tests
// do not depend on how recently the fixtures were checked out.
func freezeNow(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

func fixtureEnv(t *testing.T, getenv map[string]string) adapters.Env {
	t.Helper()
	return adapterstest.FixtureEnv(t, "testdata", getenv)
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// find returns the session with the given id and origin, failing if there is not exactly one.
func find(t *testing.T, ss []model.Session, id string, origin model.Origin) model.Session {
	t.Helper()
	var out []model.Session
	for _, s := range ss {
		if s.NativeID == id && s.Origin == origin {
			out = append(out, s)
		}
	}
	if len(out) != 1 {
		t.Fatalf("want exactly 1 session %s (%s), got %d", id, origin, len(out))
	}
	return out[0]
}

func count(ss []model.Session, id string) int {
	n := 0
	for _, s := range ss {
		if s.NativeID == id {
			n++
		}
	}
	return n
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "gemini" || a.Name() != "Gemini CLI" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
	c := a.Caps()
	if !reflect.DeepEqual(c.Resume, []string{"gemini", "--resume", "{id}"}) {
		t.Errorf("Resume = %q", c.Resume)
	}
	if !reflect.DeepEqual(c.SkipPermissions, []string{"--yolo"}) {
		t.Errorf("SkipPermissions = %q", c.SkipPermissions)
	}
	if c.Fork != nil || c.App != nil {
		t.Errorf("Fork/App should be nil: %+v", c)
	}
	if c.RootEnv != "GEMINI_CLI_HOME" {
		t.Errorf("RootEnv = %q", c.RootEnv)
	}
	if len(c.ProgramCandidates) == 0 || !strings.HasSuffix(c.ProgramCandidates[0], `\npm\gemini.cmd`) {
		t.Errorf("ProgramCandidates = %q", c.ProgramCandidates)
	}
}

func TestSources(t *testing.T) {
	env := fixtureEnv(t, nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range srcs {
		if s.Adapter != "gemini" {
			t.Errorf("source %s: Adapter = %q", s.Path, s.Adapter)
		}
		if s.Root != "" {
			t.Errorf("source %s: Root = %q, want default", s.Path, s.Root)
		}
		if len(s.Files) == 0 || s.Files[0] != s.Path {
			t.Errorf("source %s: Files[0] must be the path, got %q", s.Path, s.Files)
		}
		rel, _ := filepath.Rel(filepath.Join(env.Home, ".gemini", "tmp"), s.Path)
		names = append(names, filepath.ToSlash(rel))
	}
	sort.Strings(names)
	want := []string{
		"abababababababababababababababababababababababababababababababab/logs.json",
		"alpha/chats/session-2026-06-17T10-30-11111111.jsonl",
		"alpha/logs.json",
		"bdd6026791055310da76d7efd66755f945372f3e489494b9007aea4123638092/logs.json",
		"beta/chats/session-2025-08-20T09-00-33333333.json",
		// session-2025-08-21T09-00-44444444.json is skipped: it was migrated to .jsonl.
		"beta/chats/session-2025-08-21T09-00-44444444.jsonl",
		"beta/chats/session-2026-06-18T08-00-22222222.jsonl",
		"beta/chats/session-2026-06-18T09-00-66666666.jsonl",
		"beta/chats/session-2026-06-18T09-10-12121212.jsonl",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("sources:\n got %q\nwant %q", names, want)
	}

	// The fingerprint of a chat also covers the project marker.
	for _, s := range srcs {
		if strings.HasSuffix(s.Path, "11111111.jsonl") {
			if !containsPath(s.Files, filepath.Join(env.Home, ".gemini", "tmp", "alpha", ".project_root")) {
				t.Errorf("alpha chat Files = %q, want .project_root included", s.Files)
			}
		}
	}
}

func containsPath(list []string, p string) bool {
	for _, x := range list {
		if strings.EqualFold(filepath.Clean(x), filepath.Clean(p)) {
			return true
		}
	}
	return false
}

func TestSourcesMissingRoot(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("missing root: srcs=%v err=%v", srcs, err)
	}
}

func TestReadFixtureTree(t *testing.T) {
	freezeNow(t, time.Now().Add(time.Hour))
	env := fixtureEnv(t, nil)
	ss, skipped := adapterstest.ReadAll(t, New(), env)

	// B has one malformed line; logs.json in alpha has one entry without a session id.
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	for _, id := range []string{idSub, idSubF, idEmpty} {
		if n := count(ss, id); n != 0 {
			t.Errorf("session %s must be skipped, found %d", id, n)
		}
	}
	if n := count(ss, idD); n != 1 {
		t.Errorf("migrated legacy session D: %d sessions, want 1", n)
	}
	// A (live + prompt log), B, C, D, 7, 8, 9.
	if len(ss) != 8 {
		t.Errorf("sessions = %d, want 8", len(ss))
	}
	for _, s := range ss {
		if s.Agent != "gemini" {
			t.Errorf("%s: Agent = %q", s.NativeID, s.Agent)
		}
		if s.Root != "" {
			t.Errorf("%s: Root = %q", s.NativeID, s.Root)
		}
	}
}

func TestSessionA(t *testing.T) {
	freezeNow(t, time.Now().Add(time.Hour))
	ss, _ := adapterstest.ReadAll(t, New(), fixtureEnv(t, nil))
	a := find(t, ss, idA, model.OriginLive)

	if a.Cwd != cwdAlpha {
		t.Errorf("Cwd = %q", a.Cwd)
	}
	if a.Title != "Telegram downloader plan" || a.TitleSource != model.TitleAgent {
		t.Errorf("Title = %q (%s)", a.Title, a.TitleSource)
	}
	if a.Coverage != model.CoverageFull {
		t.Errorf("Coverage = %s", a.Coverage)
	}
	if !a.StartedAt.Equal(ts("2026-06-17T10:30:00Z")) || !a.UpdatedAt.Equal(ts("2026-06-17T10:40:00Z")) {
		t.Errorf("times = %v .. %v", a.StartedAt, a.UpdatedAt)
	}
	if a.StartedAt.Location() != time.UTC || a.UpdatedAt.Location() != time.UTC {
		t.Errorf("times must be UTC")
	}
	want := []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, At: ts("2026-06-17T10:30:05Z"), Text: "Plan the telegram downloader @notes.md"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2026-06-17T10:30:10Z"), Text: "Here is the plan: fetch each video, then save it."},
		{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ts("2026-06-17T10:30:12Z"), Text: "run_shell_command: go test ./..."},
		{Role: model.RoleTool, Kind: model.KindToolOutput, At: ts("2026-06-17T10:30:12Z"), Text: "ok  \texample/downloader\t0.12s"},
		{Role: model.RoleUser, Kind: model.KindText, At: ts("2026-06-17T10:32:00Z"), Text: "Now read main.go"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2026-06-17T10:32:05Z"), Text: "Reading it."},
		{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ts("2026-06-17T10:32:05Z"), Text: `read_file: z:\athar-fixture\alpha\main.go`},
		{Role: model.RoleTool, Kind: model.KindToolOutput, At: ts("2026-06-17T10:32:06Z"), Text: "package main"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2026-06-17T10:33:00Z"), Text: "All done."},
	}
	assertMessages(t, a.Messages, want)
}

func assertMessages(t *testing.T, got, want []model.Message) {
	t.Helper()
	if len(got) != len(want) {
		for i, m := range got {
			t.Logf("got[%d] = %s/%s %v %q", i, m.Role, m.Kind, m.At, m.Text)
		}
		t.Fatalf("messages = %d, want %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Role != w.Role || g.Kind != w.Kind || g.Text != w.Text || !g.At.Equal(w.At) {
			t.Errorf("message %d:\n got %s/%s %v %q\nwant %s/%s %v %q", i, g.Role, g.Kind, g.At, g.Text, w.Role, w.Kind, w.At, w.Text)
		}
	}
}

func TestSessionBCheckpointAndPromptTitle(t *testing.T) {
	freezeNow(t, time.Now().Add(time.Hour))
	ss, _ := adapterstest.ReadAll(t, New(), fixtureEnv(t, nil))
	b := find(t, ss, idB, model.OriginLive)
	if b.Cwd != cwdBeta {
		t.Errorf("Cwd from projects.json = %q", b.Cwd)
	}
	if b.Title != "Why does the build fail?" || b.TitleSource != model.TitlePrompt {
		t.Errorf("Title = %q (%s)", b.Title, b.TitleSource)
	}
	if !b.StartedAt.Equal(ts("2026-06-18T08:00:00Z")) || !b.UpdatedAt.Equal(ts("2026-06-18T08:00:06Z")) {
		t.Errorf("times = %v .. %v", b.StartedAt, b.UpdatedAt)
	}
	assertMessages(t, b.Messages, []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, At: ts("2026-06-18T08:00:04Z"), Text: "Why does   the\nbuild fail?"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2026-06-18T08:00:05Z"), Text: "Because of a missing import."},
		{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2026-06-18T08:00:06Z"), Text: "Anything else?"},
	})
}

func TestLegacyJSON(t *testing.T) {
	freezeNow(t, time.Now().Add(time.Hour))
	ss, _ := adapterstest.ReadAll(t, New(), fixtureEnv(t, nil))
	c := find(t, ss, idC, model.OriginLive)
	if c.Cwd != cwdBeta || c.Title != "Legacy chat about arabic search" || c.TitleSource != model.TitleAgent {
		t.Errorf("C: cwd=%q title=%q (%s)", c.Cwd, c.Title, c.TitleSource)
	}
	if !c.StartedAt.Equal(ts("2025-08-20T09:00:00Z")) || !c.UpdatedAt.Equal(ts("2025-08-20T09:05:00Z")) {
		t.Errorf("times = %v .. %v", c.StartedAt, c.UpdatedAt)
	}
	assertMessages(t, c.Messages, []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, At: ts("2025-08-20T09:00:01Z"), Text: "ابحث عن المشروع"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: ts("2025-08-20T09:00:02Z"), Text: "Found it."},
		{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ts("2025-08-20T09:00:02.5Z"), Text: "glob: **/*.go"},
		{Role: model.RoleTool, Kind: model.KindToolOutput, At: ts("2025-08-20T09:00:02.5Z"), Text: "Found 3 files"},
	})

	d := find(t, ss, idD, model.OriginLive)
	if len(d.Messages) != 1 || d.Messages[0].Text != "migrated copy" {
		t.Errorf("D must come from the .jsonl copy: %+v", d.Messages)
	}
}

func TestPromptLogs(t *testing.T) {
	freezeNow(t, time.Now().Add(time.Hour))
	ss, _ := adapterstest.ReadAll(t, New(), fixtureEnv(t, nil))

	pa := find(t, ss, idA, model.OriginPromptLog)
	if pa.Coverage != model.CoveragePrompts || pa.Cwd != cwdAlpha || len(pa.Messages) != 1 {
		t.Errorf("prompt-log A: %+v", pa)
	}

	p7 := find(t, ss, idLog7, model.OriginPromptLog)
	if p7.Cwd != cwdAlpha || p7.Coverage != model.CoveragePrompts {
		t.Errorf("7: cwd=%q coverage=%s", p7.Cwd, p7.Coverage)
	}
	if p7.Title != "find the video bug" || p7.TitleSource != model.TitlePrompt {
		t.Errorf("7: title=%q (%s)", p7.Title, p7.TitleSource)
	}
	if !p7.StartedAt.Equal(ts("2026-06-16T10:00:00Z")) || !p7.UpdatedAt.Equal(ts("2026-06-16T10:01:00Z")) {
		t.Errorf("7: times = %v .. %v", p7.StartedAt, p7.UpdatedAt)
	}
	assertMessages(t, p7.Messages, []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, At: ts("2026-06-16T10:01:00Z"), Text: "find   the video\nbug"},
	})

	// Old sha256 folder whose hash matches a registered project.
	p8 := find(t, ss, idHash8, model.OriginPromptLog)
	if p8.Cwd != cwdAlpha {
		t.Errorf("8: Cwd from hash folder = %q, want %q", p8.Cwd, cwdAlpha)
	}
	// Unknown hash: no cwd.
	p9 := find(t, ss, idHash9, model.OriginPromptLog)
	if p9.Cwd != "" || len(p9.Messages) != 2 || p9.Title != "prompt from an unknown project" {
		t.Errorf("9: %+v", p9)
	}
}

func TestSecondRootFromEnv(t *testing.T) {
	freezeNow(t, time.Now().Add(time.Hour))
	alt, err := filepath.Abs(filepath.Join("testdata", "althome"))
	if err != nil {
		t.Fatal(err)
	}
	env := fixtureEnv(t, map[string]string{"GEMINI_CLI_HOME": alt})
	ss, _ := adapterstest.ReadAll(t, New(), env)
	s := find(t, ss, idAlt, model.OriginLive)
	if s.Root != alt || s.Cwd != cwdDelta || s.Title != "hello from the second root" {
		t.Errorf("alt session: root=%q cwd=%q title=%q", s.Root, s.Cwd, s.Title)
	}
	if a := find(t, ss, idA, model.OriginLive); a.Root != "" {
		t.Errorf("default root session has Root %q", a.Root)
	}

	// GEMINI_CLI_HOME equal to the home folder is the default root, read once.
	env2 := fixtureEnv(t, map[string]string{"GEMINI_CLI_HOME": env.Home})
	ss2, _ := adapterstest.ReadAll(t, New(), env2)
	if n := count(ss2, idB); n != 1 {
		t.Errorf("GEMINI_CLI_HOME=home read the root %d times", n)
	}
	if b := find(t, ss2, idB, model.OriginLive); b.Root != "" {
		t.Errorf("GEMINI_CLI_HOME=home: Root = %q, want default", b.Root)
	}
}

// writeFile creates a file with its parent folders.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTwoMegabyteRecord(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	slug := filepath.Join(env.Home, ".gemini", "tmp", "big")
	writeFile(t, filepath.Join(slug, ".project_root"), `z:\athar-fixture\big`)
	big := strings.Repeat("0123456789abcdef", 2<<20/16) // 2 MiB of text in one record
	lines := []string{
		mustJSON(t, map[string]any{"sessionId": "bbbbbbbb-bbbb-4bbb-8bbb-00000000000b", "projectHash": strings.Repeat("0", 64), "startTime": "2026-06-20T10:00:00.000Z", "lastUpdated": "2026-06-20T10:00:00.000Z", "kind": "main"}),
		mustJSON(t, map[string]any{"id": "x1", "timestamp": "2026-06-20T10:00:01.000Z", "type": "user", "content": "print the big file"}),
		mustJSON(t, map[string]any{"id": "x2", "timestamp": "2026-06-20T10:00:02.000Z", "type": "gemini", "content": []any{map[string]any{"text": big}}}),
	}
	writeFile(t, filepath.Join(slug, "chats", "session-2026-06-20T10-00-bbbbbbbb.jsonl"), strings.Join(lines, "\n")+"\n")

	ss, skipped := adapterstest.ReadAll(t, New(), env)
	if len(ss) != 1 || skipped != 0 {
		t.Fatalf("sessions=%d skipped=%d", len(ss), skipped)
	}
	if n := len(ss[0].Messages); n != 2 || len(ss[0].Messages[1].Text) != len(big) {
		t.Fatalf("big record not read whole: %d messages", n)
	}
}

func TestToolOutputTruncated(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	slug := filepath.Join(env.Home, ".gemini", "tmp", "trunc")
	out := strings.Repeat("é", 3000) // 6000 bytes, 2-byte runes
	lines := []string{
		mustJSON(t, map[string]any{"sessionId": "cccccccc-cccc-4ccc-8ccc-00000000000c", "projectHash": strings.Repeat("0", 64), "startTime": "2026-06-20T10:00:00.000Z", "lastUpdated": "2026-06-20T10:00:00.000Z"}),
		mustJSON(t, map[string]any{"id": "y1", "timestamp": "2026-06-20T10:00:01.000Z", "type": "user", "content": "run it"}),
		mustJSON(t, map[string]any{"id": "y2", "timestamp": "2026-06-20T10:00:02.000Z", "type": "gemini", "content": "", "toolCalls": []any{
			map[string]any{"id": "c1", "name": "run_shell_command", "args": map[string]any{"command": "type big.txt"}, "status": "error", "timestamp": "2026-06-20T10:00:03.000Z",
				"result": []any{map[string]any{"functionResponse": map[string]any{"id": "c1", "name": "run_shell_command", "response": map[string]any{"output": out}}}}},
			map[string]any{"id": "c2", "name": "unknown_tool", "args": map[string]any{"alpha": 1, "beta": []int{1, 2}}, "status": "error", "timestamp": "2026-06-20T10:00:04.000Z",
				"result": []any{map[string]any{"functionResponse": map[string]any{"id": "c2", "name": "unknown_tool", "response": map[string]any{"error": "tool not found"}}}}},
			// An empty output yields a call but no output message (not the raw JSON).
			map[string]any{"id": "c3", "name": "write_file", "args": map[string]any{"file_path": `z:\x\out.txt`, "content": "body"}, "status": "success", "timestamp": "2026-06-20T10:00:05.000Z",
				"result": []any{map[string]any{"functionResponse": map[string]any{"id": "c3", "name": "write_file", "response": map[string]any{"output": ""}}}}},
		}}),
	}
	writeFile(t, filepath.Join(slug, "chats", "session-2026-06-20T10-00-cccccccc.jsonl"), strings.Join(lines, "\n")+"\n")

	ss, _ := adapterstest.ReadAll(t, New(), env)
	if len(ss) != 1 {
		t.Fatalf("sessions = %d", len(ss))
	}
	if ss[0].Cwd != "" {
		t.Errorf("no marker and no projects.json: Cwd = %q", ss[0].Cwd)
	}
	msgs := ss[0].Messages
	if len(msgs) != 6 {
		for i, m := range msgs {
			t.Logf("got[%d] = %s/%s %q", i, m.Role, m.Kind, m.Text)
		}
		t.Fatalf("messages = %d, want 6", len(msgs))
	}
	if msgs[5].Kind != model.KindToolCall || msgs[5].Text != `write_file: z:\x\out.txt` {
		t.Errorf("last message = %s %q", msgs[5].Kind, msgs[5].Text)
	}
	o := msgs[2]
	if o.Kind != model.KindToolOutput || len(o.Text) > model.MaxToolOutput || len(o.Text) < model.MaxToolOutput-1 || !strings.HasPrefix(out, o.Text) {
		t.Errorf("truncated output: kind=%s len=%d", o.Kind, len(o.Text))
	}
	if msgs[3].Text != `unknown_tool: {"alpha":1,"beta":[1,2]}` {
		t.Errorf("fallback tool call text = %q", msgs[3].Text)
	}
	if msgs[4].Text != "tool not found" {
		t.Errorf("error output = %q", msgs[4].Text)
	}
}

func TestProjectRootTrueCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Gemini lowercases project paths only on Windows")
	}
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	proj := filepath.Join(root, "Work Dir", "MyProject")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	gem := filepath.Join(env.Home, ".gemini")
	// Gemini lowercases the path on Windows; Athar restores the real casing.
	writeFile(t, filepath.Join(gem, "tmp", "myproject", ".project_root"), strings.ToLower(proj))
	writeFile(t, filepath.Join(gem, "tmp", "myproject", "chats", "session-2026-06-20T10-00-dddddddd.jsonl"),
		`{"sessionId":"dddddddd-dddd-4ddd-8ddd-00000000000d","projectHash":"`+strings.Repeat("0", 64)+`","startTime":"2026-06-20T10:00:00.000Z","lastUpdated":"2026-06-20T10:00:00.000Z","kind":"main"}`+"\n"+
			`{"id":"z1","timestamp":"2026-06-20T10:00:01.000Z","type":"user","content":"hi"}`+"\n")
	writeFile(t, filepath.Join(gem, "projects.json"), mustJSON(t, map[string]any{"projects": map[string]string{strings.ToLower(proj): "myproject"}}))
	// An old sha256 folder named after the real-case path.
	sum := sha256.Sum256([]byte(proj))
	writeFile(t, filepath.Join(gem, "tmp", hex.EncodeToString(sum[:]), "logs.json"),
		`[{"sessionId":"eeeeeeee-eeee-4eee-8eee-00000000000e","messageId":0,"type":"user","message":"old prompt","timestamp":"2025-08-09T12:00:00.000Z"}]`)
	freezeNow(t, time.Now().Add(time.Hour))

	ss, _ := adapterstest.ReadAll(t, New(), env)
	if len(ss) != 2 {
		t.Fatalf("sessions = %d", len(ss))
	}
	for _, s := range ss {
		if s.Cwd != proj {
			t.Errorf("%s: Cwd = %q, want %q", s.NativeID, s.Cwd, proj)
		}
	}
}

func TestWholeFileJSONTooFresh(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	logs := filepath.Join(env.Home, ".gemini", "tmp", "fresh", "logs.json")
	writeFile(t, logs, `[{"sessionId":"s1","messageId":0,"type":"user","message":"x","timestamp":"2025-08-09T12:00:00.000Z"}]`)
	legacy := filepath.Join(env.Home, ".gemini", "tmp", "fresh", "chats", "session-2025-08-09T12-00-s1.json")
	writeFile(t, legacy, `{"sessionId":"s2","projectHash":"h","messages":[{"id":"m","type":"user","content":"x"}]}`)
	jsonl := filepath.Join(env.Home, ".gemini", "tmp", "fresh", "chats", "session-2025-08-09T12-00-s3.jsonl")
	writeFile(t, jsonl, `{"sessionId":"s3","projectHash":"h"}`+"\n"+`{"id":"m","type":"user","content":"x"}`+"\n")

	_, mtime, err := fsread.Stat(logs)
	if err != nil {
		t.Fatal(err)
	}
	freezeNow(t, mtime.Add(2*time.Second))
	a := New()
	for _, p := range []string{logs, legacy} {
		_, err := a.Read(context.Background(), env, adapters.Source{Adapter: "gemini", Path: p, Files: []string{p}})
		if err == nil {
			t.Errorf("%s: a whole-file JSON modified 2 s ago must not be read yet", filepath.Base(p))
		}
	}
	// JSONL sources are append-only and are read at once.
	if res, err := a.Read(context.Background(), env, adapters.Source{Adapter: "gemini", Path: jsonl, Files: []string{jsonl}}); err != nil || len(res.Sessions) != 1 {
		t.Errorf("fresh JSONL: %v, %d sessions", err, len(res.Sessions))
	}

	freezeNow(t, mtime.Add(6*time.Second))
	for _, p := range []string{logs, legacy} {
		res, err := a.Read(context.Background(), env, adapters.Source{Adapter: "gemini", Path: p, Files: []string{p}})
		if err != nil || len(res.Sessions) != 1 {
			t.Errorf("%s after 6 s: %v, %d sessions", filepath.Base(p), err, len(res.Sessions))
		}
	}
}

func TestPromptLogWithoutTimestamps(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	logs := filepath.Join(env.Home, ".gemini", "tmp", "notime", "logs.json")
	writeFile(t, logs, `[{"sessionId":"s1","messageId":0,"type":"user","message":"no time here"}]`)
	_, mtime, err := fsread.Stat(logs)
	if err != nil {
		t.Fatal(err)
	}
	freezeNow(t, mtime.Add(time.Hour))
	res, err := New().Read(context.Background(), env, adapters.Source{Adapter: "gemini", Path: logs, Files: []string{logs}})
	if err != nil || len(res.Sessions) != 1 {
		t.Fatalf("%v, %d sessions", err, len(res.Sessions))
	}
	s := res.Sessions[0]
	if !s.StartedAt.Equal(mtime) || !s.UpdatedAt.Equal(mtime) || s.StartedAt.Location() != time.UTC {
		t.Errorf("times = %v .. %v, want the file time %v in UTC", s.StartedAt, s.UpdatedAt, mtime)
	}
}

func TestCorruptWholeFileIsAnError(t *testing.T) {
	freezeNow(t, time.Now().Add(time.Hour))
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	logs := filepath.Join(env.Home, ".gemini", "tmp", "torn", "logs.json")
	writeFile(t, logs, `[{"sessionId":"s1","messageId":0,"type":"user","mess`)
	if _, err := New().Read(context.Background(), env, adapters.Source{Adapter: "gemini", Path: logs, Files: []string{logs}}); err == nil {
		t.Error("a torn logs.json must fail the source (it is re-read next sync)")
	}
}

func TestReadDoesNotModifyFixtures(t *testing.T) {
	freezeNow(t, time.Now().Add(time.Hour))
	before := snapshot(t, "testdata")
	alt, _ := filepath.Abs(filepath.Join("testdata", "althome"))
	adapterstest.ReadAll(t, New(), fixtureEnv(t, map[string]string{"GEMINI_CLI_HOME": alt}))
	after := snapshot(t, "testdata")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("fixture tree changed:\nbefore %v\nafter  %v", before, after)
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fmt.Sprintf("%v|%d", info.ModTime(), info.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
