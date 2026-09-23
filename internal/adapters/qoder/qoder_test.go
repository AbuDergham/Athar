package qoder

import (
	"context"
	"errors"
	"fmt"
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

const (
	s1 = "11111111-1111-4111-8111-111111111111"
	s2 = "22222222-2222-4222-8222-222222222222"
	s3 = "33333333-3333-4333-8333-333333333333"
	s4 = "44444444-4444-4444-8444-444444444444"
	s5 = "55555555-5555-4555-8555-555555555555"
)

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

func fixtureEnv(t *testing.T) adapters.Env {
	t.Helper()
	root, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	return adapterstest.FixtureEnv(t, root, nil)
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "qoder" || a.Name() != "Qoder CLI" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
	caps := a.Caps()
	if !reflect.DeepEqual(caps.Resume, []string{"qodercli", "--resume", "{id}"}) {
		t.Errorf("Resume = %q", caps.Resume)
	}
	if !reflect.DeepEqual(caps.SkipPermissions, []string{"--dangerously-skip-permissions"}) {
		t.Errorf("SkipPermissions = %q", caps.SkipPermissions)
	}
	if caps.Fork != nil || caps.App != nil || caps.RootEnv != "" {
		t.Errorf("unexpected caps: fork=%q app=%v rootEnv=%q", caps.Fork, caps.App, caps.RootEnv)
	}
	if len(caps.ProgramCandidates) == 0 || !strings.Contains(caps.ProgramCandidates[0], `%APPDATA%\npm\qodercli.cmd`) {
		t.Errorf("ProgramCandidates = %q", caps.ProgramCandidates)
	}
}

func TestSources(t *testing.T) {
	env := fixtureEnv(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(env.Home, ".qoder", "projects")
	alpha := filepath.Join(proj, "C--work-alpha")
	beta := filepath.Join(proj, "D--projects-beta-dir")
	want := []string{
		filepath.Join(alpha, s1+".jsonl"),
		filepath.Join(alpha, s2+".jsonl"),
		filepath.Join(beta, s3+".jsonl"),
		filepath.Join(beta, s4+".jsonl"),
		filepath.Join(beta, s5+".jsonl"),
	}
	var got []string
	for _, s := range srcs {
		got = append(got, s.Path)
		if s.Adapter != "qoder" || s.Root != "" {
			t.Errorf("source %s: adapter=%q root=%q", s.Path, s.Adapter, s.Root)
		}
		meta := strings.TrimSuffix(s.Path, ".jsonl") + "-session.json"
		if !reflect.DeepEqual(s.Files, []string{s.Path, meta}) {
			t.Errorf("source %s: Files = %q", s.Path, s.Files)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sources:\n got %q\nwant %q", got, want)
	}
}

func TestSourcesMissingRoot(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on empty home = %v, %v; want none, nil", srcs, err)
	}
}

func TestReadFixtures(t *testing.T) {
	env := fixtureEnv(t)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the broken line)", skipped)
	}
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.NativeID)
	}
	// s4 is a child session (parent_session_id) and s5 holds only meta records.
	if !reflect.DeepEqual(ids, []string{s1, s2, s3}) {
		t.Fatalf("sessions = %q, want %q", ids, []string{s1, s2, s3})
	}
	for _, s := range sessions {
		if s.Agent != "qoder" || s.Coverage != model.CoverageFull || s.Origin != model.OriginLive || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: agent=%q coverage=%q origin=%q root=%q machine=%q", s.NativeID, s.Agent, s.Coverage, s.Origin, s.Root, s.Machine)
		}
	}

	t.Run("full session", func(t *testing.T) {
		s := sessions[0]
		if s.Cwd != `C:\work\alpha` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		if s.Title != "Alpha Planning" || s.TitleSource != model.TitleAgent {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1761127199990)) || !s.UpdatedAt.Equal(ms(1761127500000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		if s.StartedAt.Location() != time.UTC || s.UpdatedAt.Location() != time.UTC {
			t.Errorf("times not UTC")
		}
		want := []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: ms(1761127201000), Text: "Plan the alpha release\nwith   two steps"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1761127205000), Text: "I will read the file."},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ms(1761127205000), Text: `Read: C:\work\alpha\README.md`},
			{Role: model.RoleTool, Kind: model.KindToolOutput, At: ms(1761127211000), Text: "# Alpha\nsynthetic readme line"},
			{Role: model.RoleUser, Kind: model.KindText, At: ms(1761127300000), Text: "Now run the tests\n\n[image]"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1761127305000), Text: "Running tests."},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: ms(1761127305000), Text: "Bash: go test ./..."},
			{Role: model.RoleTool, Kind: model.KindToolOutput, At: ms(1761127400000), Text: "ok  \tall synthetic tests passed"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1761127450000), Text: "All tests pass."},
		}
		assertMessages(t, s.Messages, want)
	})

	t.Run("default title falls back to first real prompt", func(t *testing.T) {
		s := sessions[1]
		if s.Cwd != `D:\projects\beta dir` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		wantTitle := "\u062e\u0637\u0629 \u0627\u0644\u0645\u0634\u0631\u0648\u0639 \u0627\u0644\u062c\u062f\u064a\u062f"
		if s.Title != wantTitle || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s), want %q (prompt)", s.Title, s.TitleSource, wantTitle)
		}
		if !s.StartedAt.Equal(ms(1761130000000)) || !s.UpdatedAt.Equal(ms(1761130100000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		want := []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: ms(1761130010000), Text: "\u062e\u0637\u0629   \u0627\u0644\u0645\u0634\u0631\u0648\u0639\n\u0627\u0644\u062c\u062f\u064a\u062f"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1761130090000), Text: "\u062a\u0645."},
		}
		assertMessages(t, s.Messages, want)
	})

	t.Run("no metadata file", func(t *testing.T) {
		s := sessions[2]
		if s.Cwd != "" {
			t.Errorf("Cwd = %q; the folder name must never be decoded", s.Cwd)
		}
		long := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho sigma tau upsilon phi chi psi omega end"
		if s.TitleSource != model.TitlePrompt || s.Title != string([]rune(long)[:100]) {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ms(1761200000000)) || !s.UpdatedAt.Equal(ms(1761200006000)) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		want := []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: ms(1761200000000), Text: long},
			{Role: model.RoleAssistant, Kind: model.KindText, At: ms(1761200005000), Text: "Noted.\n\nSecond paragraph."},
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
		if got[i].Role != want[i].Role || got[i].Kind != want[i].Kind || !got[i].At.Equal(want[i].At) || got[i].Text != want[i].Text {
			t.Errorf("message %d:\n got %s/%s %v %q\nwant %s/%s %v %q", i,
				got[i].Role, got[i].Kind, got[i].At, got[i].Text,
				want[i].Role, want[i].Kind, want[i].At, want[i].Text)
		}
	}
}

// TestLargeRecordAndTruncation builds a tree in a temp dir: one 2 MB
// single-line record (delivered whole) and a tool output that is cut to
// model.MaxToolOutput bytes on a rune boundary.
func TestLargeRecordAndTruncation(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	dir := filepath.Join(env.Home, ".qoder", "projects", "C--big")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "77777777-7777-4777-8777-777777777777"
	big := strings.Repeat("0123456789abcdef", 2<<20/16) // 2 MiB
	out := "a" + strings.Repeat("\u0628", 1100)         // 2201 bytes; byte 2048 splits a rune
	lines := `{"id":"g1","session_id":"` + id + `","role":"user","parts":[{"type":"text","data":{"text":"` + big + `","type":"text"}}],"created_at":1761400000000,"is_meta":false}` + "\n" +
		`{"id":"g2","session_id":"` + id + `","role":"assistant","parts":[{"type":"tool_call","data":{"id":"c1","name":"Grep","input":"{\"pattern\":\"needle\",\"path\":\"src\"}","type":"function","finished":true}}],"created_at":1761400001000}` + "\n" +
		`{"id":"g3","session_id":"` + id + `","role":"tool","parts":[{"type":"tool_result","data":{"tool_use_id":"c1","content":"` + out + `","metadata":"{}"}}],"created_at":1761400002000}` + "\n" +
		`{"id":"g4","session_id":"` + id + `","role":"assistant","parts":[{"type":"tool_call","data":{"id":"c2","name":"Custom","input":"not json at all","type":"function","finished":true}}],"created_at":1761400003000}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 0 || len(sessions) != 1 {
		t.Fatalf("sessions=%d skipped=%d", len(sessions), skipped)
	}
	msgs := sessions[0].Messages
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}
	if msgs[0].Text != big {
		t.Errorf("2 MB record: got %d bytes, want %d", len(msgs[0].Text), len(big))
	}
	if msgs[1].Kind != model.KindToolCall || msgs[1].Text != "Grep: needle" {
		t.Errorf("tool call = %s %q", msgs[1].Kind, msgs[1].Text)
	}
	if msgs[2].Kind != model.KindToolOutput || len(msgs[2].Text) > model.MaxToolOutput || !utf8.ValidString(msgs[2].Text) || len(msgs[2].Text) != 2047 {
		t.Errorf("tool output: %d bytes, valid=%v", len(msgs[2].Text), utf8.ValidString(msgs[2].Text))
	}
	if msgs[3].Text != "Custom: not json at all" {
		t.Errorf("tool call with non-JSON input = %q", msgs[3].Text)
	}
	if len([]rune(sessions[0].Title)) != 100 || sessions[0].TitleSource != model.TitlePrompt {
		t.Errorf("title: %d runes (%s)", len([]rune(sessions[0].Title)), sessions[0].TitleSource)
	}
}

// TestBrokenMetadata: an old unparsable metadata file is ignored with a
// warning; a freshly modified one fails the source so it is retried.
func TestBrokenMetadata(t *testing.T) {
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	dir := filepath.Join(env.Home, ".qoder", "projects", "C--x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "88888888-8888-4888-8888-888888888888"
	transcript := filepath.Join(dir, id+".jsonl")
	line := `{"id":"h1","session_id":"` + id + `","role":"user","parts":[{"type":"text","data":{"text":"hello there","type":"text"}}],"created_at":1761500000000}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := filepath.Join(dir, id+"-session.json")
	if err := os.WriteFile(meta, []byte(`{"id":"`+id+`","working_dir":"C:\\x`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := adapters.Source{Adapter: "qoder", Path: transcript, Files: []string{transcript, meta}}

	if _, err := New().Read(context.Background(), env, src); !errors.Is(err, adapters.ErrRetryLater) {
		t.Fatalf("fresh half-written metadata: err = %v, want adapters.ErrRetryLater", err)
	}

	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(meta, old, old); err != nil {
		t.Fatal(err)
	}
	res, err := New().Read(context.Background(), env, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || len(res.Sessions) != 1 {
		t.Fatalf("warnings=%q sessions=%d", res.Warnings, len(res.Sessions))
	}
	s := res.Sessions[0]
	if s.Cwd != "" || s.Title != "hello there" || s.TitleSource != model.TitlePrompt || !s.StartedAt.Equal(ms(1761500000000)) {
		t.Errorf("session = cwd %q title %q (%s) started %v", s.Cwd, s.Title, s.TitleSource, s.StartedAt)
	}
}

func TestEpoch(t *testing.T) {
	cases := []struct {
		in   float64
		want time.Time
	}{
		{0, time.Time{}},
		{-5, time.Time{}},
		{1761127200000, time.Date(2025, 10, 22, 10, 0, 0, 0, time.UTC)},
		{1761127200, time.Date(2025, 10, 22, 10, 0, 0, 0, time.UTC)}, // seconds
		{1761127200123.9, time.Date(2025, 10, 22, 10, 0, 0, 123e6, time.UTC)},
	}
	for _, c := range cases {
		if got := epoch(c.in); !got.Equal(c.want) || (!got.IsZero() && got.Location() != time.UTC) {
			t.Errorf("epoch(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestToolCallText(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"Bash", `{"command":"go  test\n./...","description":"d"}`, "Bash: go test ./..."},
		{"Read", `{"file_path":"C:\\a b\\c.go","offset":3}`, `Read: C:\a b\c.go`},
		{"Todo", `{"todos":[{"content":"x"}]}`, `Todo: {"todos":[{"content":"x"}]}`},
		{"Empty", `{}`, "Empty"},
		{"", ``, "tool"},
		{"Raw", "plain text", "Raw: plain text"},
	}
	for _, c := range cases {
		if got := toolCallText(c.name, c.input); got != c.want {
			t.Errorf("toolCallText(%q, %q) = %q, want %q", c.name, c.input, got, c.want)
		}
	}
}

func TestReadCancelled(t *testing.T) {
	env := fixtureEnv(t)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) == 0 {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Read(ctx, env, srcs[0]); err == nil {
		t.Fatal("Read with a cancelled context succeeded")
	}
}

// TestReadOnly checks that reading the fixture tree changes nothing in it.
func TestReadOnly(t *testing.T) {
	env := fixtureEnv(t)
	before := snapshot(t, env.Home)
	adapterstest.ReadAll(t, New(), env)
	if after := snapshot(t, env.Home); !reflect.DeepEqual(before, after) {
		t.Fatalf("fixture tree changed:\nbefore %v\nafter  %v", before, after)
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() { // directory mtimes change lazily on Windows; compare names only
			out[p] = "dir"
			return nil
		}
		out[p] = fmt.Sprintf("%d|%v|%d", info.ModTime().UnixNano(), info.IsDir(), info.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
