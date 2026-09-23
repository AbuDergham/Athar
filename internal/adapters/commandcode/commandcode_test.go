package commandcode

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

const (
	idAlpha  = "11111111-1111-4111-8111-111111111111"
	idBeta   = "22222222-2222-4222-8222-222222222222"
	idGamma  = "33333333-3333-4333-8333-333333333333"
	idEmpty  = "44444444-4444-4444-8444-444444444444"
	idV2     = "55555555-5555-4555-8555-555555555555"
	idLegacy = "66666666-6666-4666-8666-666666666666"
	idRename = "77777777-7777-4777-8777-777777777777"
)

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func fixtureEnv(t *testing.T) adapters.Env {
	t.Helper()
	return adapterstest.FixtureEnv(t, fixtureRoot(t), nil)
}

func ts(s string) time.Time {
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return v.UTC()
}

// msg is a compact golden form of a model.Message.
func msg(m model.Message) string {
	return string(m.Role) + "/" + string(m.Kind) + ": " + m.Text
}

func msgs(ms []model.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = msg(m)
	}
	return out
}

func byID(t *testing.T, sessions []model.Session, id string) model.Session {
	t.Helper()
	for _, s := range sessions {
		if s.NativeID == id {
			return s
		}
	}
	t.Fatalf("session %s not found", id)
	return model.Session{}
}

func TestIdentityAndCaps(t *testing.T) {
	a := New()
	if a.ID() != "commandcode" || a.Name() != "Command Code" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
	c := a.Caps()
	if !reflect.DeepEqual(c.Resume, []string{"cmdc", "--resume", "{id}"}) {
		t.Errorf("Resume = %q", c.Resume)
	}
	if !reflect.DeepEqual(c.Fork, []string{"--fork-session"}) {
		t.Errorf("Fork = %q", c.Fork)
	}
	if !reflect.DeepEqual(c.SkipPermissions, []string{"--dangerously-skip-permissions"}) {
		t.Errorf("SkipPermissions = %q", c.SkipPermissions)
	}
	if !reflect.DeepEqual(c.ProgramCandidates, []string{`%APPDATA%\npm\cmdc.cmd`}) {
		t.Errorf("ProgramCandidates = %q", c.ProgramCandidates)
	}
	if c.App != nil || c.RootEnv != "" {
		t.Errorf("unexpected caps: app=%v rootEnv=%q", c.App, c.RootEnv)
	}
}

func TestSources(t *testing.T) {
	env := fixtureEnv(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(env.Home, ".commandcode", "projects")
	var got []string
	for _, s := range srcs {
		rel, err := filepath.Rel(projects, s.Path)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, filepath.ToSlash(rel))
		if s.Adapter != "commandcode" || s.Root != "" {
			t.Errorf("%s: adapter=%q root=%q", rel, s.Adapter, s.Root)
		}
		wantFiles := []string{s.Path, strings.TrimSuffix(s.Path, ".jsonl") + ".meta.json"}
		if !reflect.DeepEqual(s.Files, wantFiles) {
			t.Errorf("%s: Files = %q, want %q", rel, s.Files, wantFiles)
		}
	}
	want := []string{
		"C-work-alpha/" + idAlpha + ".jsonl",
		"C-work-beta/" + idBeta + ".jsonl",
		"C-work-delta/" + idLegacy + ".jsonl",
		"C-work-delta/" + idRename + ".jsonl",
		"C-work-gamma/" + idGamma + ".jsonl",
		"C-work-gamma/" + idEmpty + ".jsonl",
		"C-work-gamma/" + idV2 + ".jsonl",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sources:\n got %q\nwant %q", got, want)
	}
}

func TestSourcesMissingRoot(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on an empty home = %v, %v; want none, nil", srcs, err)
	}
}

func TestReadFixtures(t *testing.T) {
	env := fixtureEnv(t)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)

	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.NativeID)
		if s.Agent != "commandcode" || s.Origin != model.OriginLive || s.Coverage != model.CoverageFull || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: agent=%q origin=%q coverage=%q root=%q machine=%q", s.NativeID, s.Agent, s.Origin, s.Coverage, s.Root, s.Machine)
		}
	}
	// idEmpty has a header but no message: it is not a session.
	wantIDs := []string{idAlpha, idBeta, idGamma, idV2, idLegacy, idRename}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("sessions = %q, want %q", ids, wantIDs)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the non-JSON line of %s)", skipped, idAlpha)
	}

	t.Run("v3 full session", func(t *testing.T) {
		s := byID(t, sessions, idAlpha)
		if s.Cwd != `C:\work\alpha` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		// The latest session_info name is the user's own name and beats the generated meta title.
		if s.Title != "Alpha file listing" || s.TitleSource != model.TitleUser {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ts("2026-08-01T10:00:00Z")) || !s.UpdatedAt.Equal(ts("2026-08-01T10:09:00Z")) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		want := []string{
			"user/text: Please list the files in the alpha project",
			"assistant/text: I will list them.",
			"assistant/tool_call: shell_command: dir /b",
			"tool/tool_output: main.go\nREADME.md",
			`assistant/tool_call: read_file: C:\work\alpha\main.go`,
			"tool/tool_output: package main",
			"assistant/text: There are two files: main.go and README.md.",
			"user/text: Now summarise README.md",
			"system/text: Error: Model request failed (synthetic)",
		}
		if got := msgs(s.Messages); !reflect.DeepEqual(got, want) {
			t.Errorf("messages:\n got %q\nwant %q", got, want)
		}
		if !s.Messages[0].At.Equal(ts("2026-08-01T10:00:05Z")) || !s.Messages[3].At.Equal(ts("2026-08-01T10:00:11Z")) {
			t.Errorf("message times = %v, %v", s.Messages[0].At, s.Messages[3].At)
		}
		for _, m := range s.Messages {
			if m.At.Location() != time.UTC {
				t.Errorf("message time not UTC: %v", m.At)
			}
			if strings.Contains(m.Text, "private reasoning") || strings.Contains(m.Text, "hidden context") ||
				strings.Contains(m.Text, "Summary of the earlier") || strings.Contains(m.Text, "Goal reminder") {
				t.Errorf("thinking/meta/compaction/mod text leaked: %q", m.Text)
			}
		}
	})

	t.Run("sources, branches, generated title", func(t *testing.T) {
		s := byID(t, sessions, idBeta)
		if s.Cwd != `D:\code\beta project` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		// The last session_info has no name (the user cleared it): the generated title applies.
		if s.Title != "Generated beta title" || s.TitleSource != model.TitleAgent {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		if !s.StartedAt.Equal(ts("2026-08-02T09:00:00Z")) || !s.UpdatedAt.Equal(ts("2026-08-02T09:02:00Z")) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
		want := []string{
			"user/text: Fix the failing build",
			"assistant/text: First attempt at a fix.",
			"assistant/text: Second attempt on another branch.",
			"system/text: Stop hook: run the tests too",
			"assistant/text: Tests pass.",
			"user/text: also update the changelog",
			"user/text: and keep it short",
			"system/text: Reviewer mod note",
			"system/text: Insufficient credits",
		}
		if got := msgs(s.Messages); !reflect.DeepEqual(got, want) {
			t.Errorf("messages:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("prompt title, Arabic, images, unterminated tail", func(t *testing.T) {
		s := byID(t, sessions, idGamma)
		if s.Cwd != `C:\work\gamma` {
			t.Errorf("Cwd = %q", s.Cwd)
		}
		full := "أريد مساعدة في سكربت النشر deployment script: it fails on the second step when the environment variable is missing and I do not know why"
		wantTitle := strings.TrimSpace(string([]rune(full)[:100]))
		if s.Title != wantTitle || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s), want %q", s.Title, s.TitleSource, wantTitle)
		}
		if n := utf8.RuneCountInString(s.Title); n > 100 {
			t.Errorf("title has %d runes", n)
		}
		want := []string{
			"user/text: أريد   مساعدة\nفي سكربت النشر\n[image]\ndeployment script: it fails on the second step when the environment variable is missing and I do not know why",
			"assistant/text: First part.\nSecond part.",
			"system/text: Interrupted by user",
			"user/text: string content prompt",
		}
		if got := msgs(s.Messages); !reflect.DeepEqual(got, want) {
			t.Errorf("messages:\n got %q\nwant %q", got, want)
		}
		// The half-written last line is not delivered, so it does not move UpdatedAt.
		if !s.UpdatedAt.Equal(ts("2026-08-03T08:02:00Z")) {
			t.Errorf("UpdatedAt = %v", s.UpdatedAt)
		}
	})

	t.Run("v2 storage format", func(t *testing.T) {
		s := byID(t, sessions, idV2)
		if s.Cwd != "" {
			t.Errorf("Cwd = %q; v2 files carry no cwd and folder names are never decoded", s.Cwd)
		}
		if s.Title != "Explain the v2 storage format" || s.TitleSource != model.TitlePrompt {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
		want := []string{
			"user/text: Explain the v2 storage format",
			"assistant/text: It stores one message per line.",
			"assistant/tool_call: grep: version",
			"tool/tool_output: src/store.ts: version 2",
			`tool/tool_output: {"size":42}`,
			"assistant/text: Plain string answer in v2.",
			"user/text: Thanks\n[image]",
		}
		if got := msgs(s.Messages); !reflect.DeepEqual(got, want) {
			t.Errorf("messages:\n got %q\nwant %q", got, want)
		}
		if !s.StartedAt.Equal(ts("2026-07-10T12:00:00Z")) || !s.UpdatedAt.Equal(ts("2026-07-10T12:00:10Z")) {
			t.Errorf("times = %v .. %v", s.StartedAt, s.UpdatedAt)
		}
	})

	t.Run("legacy v1 storage format", func(t *testing.T) {
		s := byID(t, sessions, idLegacy)
		if s.Cwd != "" || s.Title != "Legacy prompt about the delta module" || s.TitleSource != model.TitlePrompt {
			t.Errorf("cwd=%q title=%q (%s)", s.Cwd, s.Title, s.TitleSource)
		}
		want := []string{
			"user/text: Legacy prompt about the delta module",
			"assistant/text: Looking at it.",
			"assistant/tool_call: edit_file: delta.go",
			"tool/tool_output: edited",
			"assistant/text: All done.",
		}
		if got := msgs(s.Messages); !reflect.DeepEqual(got, want) {
			t.Errorf("messages:\n got %q\nwant %q", got, want)
		}
		// The last record has no timestamp; meta createdAt (ms) is used instead.
		last := time.UnixMilli(1780297205000).UTC()
		if !s.Messages[4].At.Equal(last) || !s.UpdatedAt.Equal(last) {
			t.Errorf("createdAt fallback: message %v, UpdatedAt %v, want %v", s.Messages[4].At, s.UpdatedAt, last)
		}
		if !s.StartedAt.Equal(ts("2026-06-01T07:00:00Z")) {
			t.Errorf("StartedAt = %v", s.StartedAt)
		}
	})

	t.Run("user rename in meta, first cwd wins", func(t *testing.T) {
		s := byID(t, sessions, idRename)
		if s.Cwd != `C:\work\delta` {
			t.Errorf("Cwd = %q, want the first header's cwd", s.Cwd)
		}
		if s.Title != "My renamed session" || s.TitleSource != model.TitleUser {
			t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
		}
	})
}

// writeTree creates files (relative path → content) under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const tinySession = `{"type":"session","version":3,"id":"ID","timestamp":"2026-08-05T10:00:00.000Z","cwd":"C:\\w"}
{"type":"message","id":"x1","parentId":null,"timestamp":"2026-08-05T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"hello there"}],"meta":{"source":"user"}}}
`

// TestHomeVariable covers Command Code's own rule: it uses %HOME% before
// %USERPROFILE%, so both roots are listed, without duplicates.
func TestHomeVariable(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"home/.commandcode/projects/p/" + idAlpha + ".jsonl": strings.ReplaceAll(tinySession, "ID", idAlpha),
		"other/.commandcode/projects/q/" + idBeta + ".jsonl": strings.ReplaceAll(tinySession, "ID", idBeta),
	})
	other := filepath.Join(root, "other")

	env := adapterstest.FixtureEnv(t, root, map[string]string{"HOME": other})
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 2 {
		t.Fatalf("sources with HOME set elsewhere = %d, want 2: %+v", len(srcs), srcs)
	}

	sames := []string{env.Home, env.Home + string(os.PathSeparator)}
	if runtime.GOOS == "windows" {
		sames = append(sames, strings.ToUpper(env.Home)) // Windows paths are case-insensitive
	}
	for _, same := range sames {
		env := adapterstest.FixtureEnv(t, root, map[string]string{"HOME": same})
		srcs, err := New().Sources(env)
		if err != nil || len(srcs) != 1 {
			t.Errorf("HOME=%q: sources = %d (%v), want 1", same, len(srcs), err)
		}
	}

	// A HOME that is not an absolute path (e.g. an MSYS "/c/Users/x") is ignored.
	env = adapterstest.FixtureEnv(t, root, map[string]string{"HOME": "relative/dir"})
	if srcs, _ := New().Sources(env); len(srcs) != 1 {
		t.Errorf("relative HOME: sources = %d, want 1", len(srcs))
	}
}

// TestLargeRecord proves a single 2 MB JSONL record is read whole and that
// tool outputs are cut to model.MaxToolOutput bytes on a rune boundary.
func TestLargeRecord(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("large prompt ", 2<<20/13+1)
	output := "x" + strings.Repeat("ب", 2<<20/2) // odd prefix: a byte cut would split a rune
	lines := strings.ReplaceAll(tinySession, "ID", idAlpha) +
		`{"type":"message","id":"x2","parentId":"x1","timestamp":"2026-08-05T10:00:02.000Z","message":{"role":"user","content":[{"type":"text","text":"` + big + `"}],"meta":{"source":"user"}}}` + "\n" +
		`{"type":"message","id":"x3","parentId":"x2","timestamp":"2026-08-05T10:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"` + output + `"}]}],"meta":{"source":"tool"}}}` + "\n"
	writeTree(t, root, map[string]string{"home/.commandcode/projects/p/" + idAlpha + ".jsonl": lines})
	if len(lines) < 4<<20 {
		t.Fatalf("fixture too small: %d bytes", len(lines))
	}

	env := adapterstest.FixtureEnv(t, root, nil)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if len(sessions) != 1 || skipped != 0 {
		t.Fatalf("sessions=%d skipped=%d", len(sessions), skipped)
	}
	ms := sessions[0].Messages
	if len(ms) != 3 {
		t.Fatalf("messages = %d, want 3", len(ms))
	}
	if ms[1].Text != big {
		t.Errorf("the 2 MB prompt was not kept whole (len %d, want %d)", len(ms[1].Text), len(big))
	}
	out := ms[2].Text
	if ms[2].Kind != model.KindToolOutput || len(out) > model.MaxToolOutput || len(out) < model.MaxToolOutput-4 || !utf8.ValidString(out) || !strings.HasPrefix(out, "xب") {
		t.Errorf("tool output: kind=%s len=%d valid=%v", ms[2].Kind, len(out), utf8.ValidString(out))
	}
}

// TestBrokenMeta: a torn or unreadable .meta.json only loses the title it
// would give; the transcript is still read.
func TestBrokenMeta(t *testing.T) {
	root := t.TempDir()
	base := "home/.commandcode/projects/p/" + idAlpha
	writeTree(t, root, map[string]string{
		base + ".jsonl":     strings.ReplaceAll(tinySession, "ID", idAlpha),
		base + ".meta.json": `{"title":"half`,
	})
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(base+".meta.json")), old, old); err != nil {
		t.Fatal(err)
	}
	env := adapterstest.FixtureEnv(t, root, nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("sources = %v, %v", srcs, err)
	}
	res, err := New().Read(context.Background(), env, srcs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || len(res.Warnings) != 1 {
		t.Fatalf("sessions=%d warnings=%q", len(res.Sessions), res.Warnings)
	}
	if s := res.Sessions[0]; s.Title != "hello there" || s.TitleSource != model.TitlePrompt {
		t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
	}
}

// TestMetaBeingWritten: a torn .meta.json written in the last seconds is
// probably being rewritten, so the source is retried on the next sync
// (adapters.ErrRetryLater) instead of losing its title.
func TestMetaBeingWritten(t *testing.T) {
	root := t.TempDir()
	base := "home/.commandcode/projects/p/" + idAlpha
	writeTree(t, root, map[string]string{
		base + ".jsonl":     strings.ReplaceAll(tinySession, "ID", idAlpha),
		base + ".meta.json": `{"title":"half`,
	})
	env := adapterstest.FixtureEnv(t, root, nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("sources = %v, %v", srcs, err)
	}
	res, err := New().Read(context.Background(), env, srcs[0])
	if !errors.Is(err, adapters.ErrRetryLater) {
		t.Fatalf("Read with a fresh torn meta: err = %v, want ErrRetryLater", err)
	}
	if len(res.Sessions) != 0 {
		t.Errorf("sessions = %d, want none with a retry error", len(res.Sessions))
	}

	// A fresh but valid meta.json is read at once.
	writeTree(t, root, map[string]string{base + ".meta.json": `{"title":"Fresh title"}`})
	res, err = New().Read(context.Background(), env, srcs[0])
	if err != nil || len(res.Sessions) != 1 || res.Sessions[0].Title != "Fresh title" {
		t.Fatalf("fresh valid meta: err=%v sessions=%+v", err, res.Sessions)
	}
}

func TestReadMissingFile(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	p := filepath.Join(env.Home, ".commandcode", "projects", "p", idAlpha+".jsonl")
	_, err := New().Read(context.Background(), env, adapters.Source{Adapter: "commandcode", Path: p, Files: []string{p}})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read of a vanished file: err = %v, want fs.ErrNotExist", err)
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
	if _, err := New().Read(ctx, env, srcs[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestReadOnly: reading the fixture tree changes nothing in it and creates nothing.
func TestReadOnly(t *testing.T) {
	root := fixtureRoot(t)
	snap := func() map[string]string {
		m := map[string]string{}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			m[p] = fmt.Sprintf("%d|%d", info.Size(), info.ModTime().UnixNano())
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	before := snap()
	adapterstest.ReadAll(t, New(), fixtureEnv(t))
	if after := snap(); !reflect.DeepEqual(before, after) {
		t.Errorf("fixture tree changed by a read:\nbefore %v\nafter  %v", before, after)
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
