package dsh

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

const (
	idAlpha   = "session-11111111-1111-4111-8111-111111111111"
	idBeta    = "session-22222222-2222-4222-8222-222222222222"
	idSub     = "session-33333333-3333-4333-8333-333333333333"
	idCustom  = "session-44444444-4444-4444-8444-444444444444"
	idGamma   = "session-55555555-5555-4555-8555-555555555555"
	idNoTitle = "session-66666666-6666-4666-8666-666666666666"
	idEmpty   = "session-77777777-7777-4777-8777-777777777777"
	idFork    = "session-88888888-8888-4888-8888-888888888888"
)

// fixture is a synthetic dsh tree built in a temp dir, laid out like dsh 0.1.x
// writes it: <root>\sessions\<project-key>\<session-id>\session[.vN].jsonl[.zstd],
// where each .zstd file is a concatenation of complete zstd frames (one per batch).
type fixture struct {
	root   string // FixtureEnv root (contains home\)
	dsh    string // <root>\home\.dsh
	custom string // a second root used through DSH_HOME
	big    string // text of the generated 2 MB assistant record
	tool   string // text of the generated large tool output
}

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

func readLines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// frames encodes lines as dsh does: the header in its own frame, then batches
// of perFrame lines, each batch one checksummed frame ending with "\n".
func frames(t *testing.T, lines []string, perFrame int) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	out := enc.EncodeAll([]byte(lines[0]+"\n"), nil)
	for i := 1; i < len(lines); i += perFrame {
		j := min(i+perFrame, len(lines))
		out = enc.EncodeAll([]byte(strings.Join(lines[i:j], "\n")+"\n"), out)
	}
	return out
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{
		root:   root,
		dsh:    filepath.Join(root, "home", ".dsh"),
		custom: filepath.Join(root, "custom-dsh"),
	}
	s := filepath.Join(f.dsh, "sessions")
	alphaDir := filepath.Join(s, "--C-work-alpha--", idAlpha)

	// Alpha: an old generation (v1) and the current one (v3) side by side.
	write(t, filepath.Join(alphaDir, "session.jsonl.zstd"), frames(t, readLines(t, "alpha.v1.jsonl"), 2))
	write(t, filepath.Join(alphaDir, "session.v3.jsonl.zstd"), frames(t, readLines(t, "alpha.v3.jsonl"), 3))
	// Decoys inside the session folder: a subagent log and non-canonical names.
	sub := frames(t, readLines(t, "subagent.v3.jsonl"), 5)
	write(t, filepath.Join(alphaDir, "subagents", idSub, "session.v3.jsonl.zstd"), sub)
	write(t, filepath.Join(alphaDir, "session.v3.jsonl.zstd.tmp-1234"), sub)
	write(t, filepath.Join(alphaDir, "session.v03.jsonl.zstd"), sub)
	write(t, filepath.Join(alphaDir, "session.v0.jsonl.zstd"), sub)

	// Gamma: v1 + v3 where the last v3 event is the migration marker.
	gammaDir := filepath.Join(s, "--C-work-alpha--", idGamma)
	write(t, filepath.Join(gammaDir, "session.jsonl.zstd"), frames(t, readLines(t, "gamma.v1.jsonl"), 1))
	write(t, filepath.Join(gammaDir, "session.v3.jsonl.zstd"), frames(t, readLines(t, "gamma.v3.jsonl"), 1))

	// A subagent session stored as its own session folder: listed, never emitted.
	write(t, filepath.Join(s, "--C-work-alpha--", idSub, "session.v3.jsonl.zstd"), sub)
	// A fork of alpha (parentSession, not a subagent): a session of its own.
	write(t, filepath.Join(s, "--C-work-alpha--", idFork, "session.v3.jsonl.zstd"), frames(t, readLines(t, "fork.v3.jsonl"), 2))

	// Beta: uncompressed JSONL, plus a single ~2 MB record and a large tool output.
	f.big = strings.Repeat("large answer block ", 110_000) // ~2.09 MB
	f.tool = "x" + strings.Repeat("ب", 3000)               // odd byte offsets: rune-boundary cut
	beta := readLines(t, "beta.v3.jsonl")
	beta = append(beta,
		`{"type":"assistant/message","seq":4,"time":1788900003000,"data":{"message":{"role":"assistant","content":[{"type":"text","text":"`+f.big+`"}],"id":"a-big"}}}`,
		`{"type":"tool/result","seq":5,"time":1788900004000,"data":{"message":{"content":[{"type":"tool-result","toolCallId":"call_2","content":[{"type":"text","text":"`+f.tool+`"}],"isError":false}],"role":"tool","id":"t-big"}}}`,
	)
	write(t, filepath.Join(s, "--D-proj-beta--", idBeta, "session.v3.jsonl"), []byte(strings.Join(beta, "\n")+"\n"))

	// No cwd (dsh files these under _no-cwd); the last frame is torn.
	nt := frames(t, readLines(t, "notitle.v3.jsonl"), 1)
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	torn := enc.EncodeAll([]byte(`{"type":"user/message","seq":2,"time":1788500009000,"data":{"content":[{"type":"text","text":"TORN TAIL SHOULD NOT APPEAR"}],"source":{"kind":"user"},"role":"user","id":"u-9"}}`+"\n"), nil)
	enc.Close()
	nt = append(nt, torn[:len(torn)/2]...)
	write(t, filepath.Join(s, "_no-cwd", idNoTitle, "session.v3.jsonl.zstd"), nt)

	// A session that was opened and never used.
	write(t, filepath.Join(s, "--C-work-empty--", idEmpty, "session.v3.jsonl.zstd"), frames(t, readLines(t, "empty.v3.jsonl"), 5))
	// Folders and files that are not sessions.
	if err := os.MkdirAll(filepath.Join(s, "--C-work-empty--", "not-a-session"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s, "stray.txt"), []byte("x"))
	write(t, filepath.Join(f.dsh, "storages", "workspace.json"), []byte(`{"unit":{"name":"workspace","version":2},"tables":{"workspaces":{}}}`))

	// A second data root selected with DSH_HOME.
	write(t, filepath.Join(f.custom, "sessions", "--E-custom--", idCustom, "session.v3.jsonl.zstd"), frames(t, readLines(t, "custom.v3.jsonl"), 1))
	return f
}

func (f fixture) env(t *testing.T, vars map[string]string) adapters.Env {
	return adapterstest.FixtureEnv(t, f.root, vars)
}

func byID(ss []model.Session) map[string]model.Session {
	m := map[string]model.Session{}
	for _, s := range ss {
		m[s.NativeID] = s
	}
	return m
}

func TestIdentityAndCaps(t *testing.T) {
	a := New()
	if a.ID() != "dsh" || a.Name() == "" || a.Version() < 1 {
		t.Fatalf("identity: %q %q %d", a.ID(), a.Name(), a.Version())
	}
	c := a.Caps()
	if c.Resume != nil || c.Fork != nil || c.SkipPermissions != nil || c.App != nil {
		t.Errorf("dsh resume is unconfirmed, caps must not offer it: %+v", c)
	}
	if c.RootEnv != "DSH_HOME" {
		t.Errorf("RootEnv = %q, want DSH_HOME", c.RootEnv)
	}
}

func TestSources(t *testing.T) {
	f := buildFixture(t)
	srcs, err := New().Sources(f.env(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	s := filepath.Join(f.dsh, "sessions")
	want := map[string][]string{
		filepath.Join(s, "--C-work-alpha--", idAlpha): {"session.jsonl.zstd", "session.v3.jsonl.zstd"},
		filepath.Join(s, "--C-work-alpha--", idGamma): {"session.jsonl.zstd", "session.v3.jsonl.zstd"},
		filepath.Join(s, "--C-work-alpha--", idSub):   {"session.v3.jsonl.zstd"},
		filepath.Join(s, "--C-work-alpha--", idFork):  {"session.v3.jsonl.zstd"},
		filepath.Join(s, "--D-proj-beta--", idBeta):   {"session.v3.jsonl"},
		filepath.Join(s, "_no-cwd", idNoTitle):        {"session.v3.jsonl.zstd"},
		filepath.Join(s, "--C-work-empty--", idEmpty): {"session.v3.jsonl.zstd"},
	}
	if len(srcs) != len(want) {
		for _, src := range srcs {
			t.Logf("source %s %v", src.Path, src.Files)
		}
		t.Fatalf("got %d sources, want %d", len(srcs), len(want))
	}
	for _, src := range srcs {
		files, ok := want[src.Path]
		if !ok {
			t.Errorf("unexpected source %s", src.Path)
			continue
		}
		if src.Adapter != "dsh" || src.Root != "" {
			t.Errorf("%s: adapter %q root %q", src.Path, src.Adapter, src.Root)
		}
		var got []string
		for _, p := range src.Files {
			if filepath.Dir(p) != src.Path {
				t.Errorf("%s: file %s outside the session folder", src.Path, p)
			}
			got = append(got, filepath.Base(p))
		}
		if strings.Join(got, ",") != strings.Join(files, ",") {
			t.Errorf("%s: files %v, want %v", src.Path, got, files)
		}
	}
}

func TestSourcesMissingRoot(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), map[string]string{"DSH_HOME": filepath.Join(t.TempDir(), "nope")})
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("missing roots: %v %v", srcs, err)
	}
}

func TestRead(t *testing.T) {
	f := buildFixture(t)
	sessions, skipped := adapterstest.ReadAll(t, New(), f.env(t, nil))
	got := byID(sessions)
	if len(sessions) != 5 {
		for _, s := range sessions {
			t.Logf("session %s", s.NativeID)
		}
		t.Fatalf("got %d sessions, want 5 (alpha, beta, gamma, no-title, fork)", len(sessions))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the broken line in alpha)", skipped)
	}
	for _, s := range sessions {
		if s.Agent != "dsh" || s.Coverage != model.CoverageFull || s.Origin != model.OriginLive || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: agent %q coverage %q origin %q root %q", s.NativeID, s.Agent, s.Coverage, s.Origin, s.Root)
		}
		for _, m := range s.Messages {
			for _, bad := range []string{"SHOULD NOT APPEAR", "V1 ONLY", "SECRET REASONING", "THINKING"} {
				if strings.Contains(m.Text, bad) {
					t.Errorf("%s: message leaked %q: %.80q", s.NativeID, bad, m.Text)
				}
			}
			if m.At.IsZero() || m.At.Location() != time.UTC {
				t.Errorf("%s: message time %v not UTC", s.NativeID, m.At)
			}
		}
	}
	if _, ok := got[idSub]; ok {
		t.Error("subagent session must be skipped")
	}
	if _, ok := got[idEmpty]; ok {
		t.Error("a session with no messages must be skipped")
	}

	type msg struct {
		role model.Role
		kind model.Kind
		at   int64
		text string
	}
	check := func(id string, want []msg) {
		t.Helper()
		s := got[id]
		if len(s.Messages) != len(want) {
			for i, m := range s.Messages {
				t.Logf("%s[%d] %s/%s %.60q", id, i, m.Role, m.Kind, m.Text)
			}
			t.Fatalf("%s: %d messages, want %d", id, len(s.Messages), len(want))
		}
		for i, w := range want {
			m := s.Messages[i]
			if m.Role != w.role || m.Kind != w.kind || m.Text != w.text || (w.at != 0 && !m.At.Equal(ms(w.at))) {
				t.Errorf("%s[%d] = %s/%s %v %.80q, want %s/%s %v %.80q", id, i, m.Role, m.Kind, m.At, m.Text, w.role, w.kind, ms(w.at), w.text)
			}
		}
	}

	// Alpha: the v3 generation wins, the inbox copy of a prompt is deduplicated,
	// a queued prompt that never became user/message is kept, the user's rename is the title.
	alpha := got[idAlpha]
	if alpha.Cwd != `C:\work\alpha` {
		t.Errorf("alpha cwd %q", alpha.Cwd)
	}
	if alpha.Title != "My telegram project" || alpha.TitleSource != model.TitleUser {
		t.Errorf("alpha title %q (%s)", alpha.Title, alpha.TitleSource)
	}
	if !alpha.StartedAt.Equal(ms(1788700000000)) || !alpha.UpdatedAt.Equal(ms(1788800002000)) {
		t.Errorf("alpha times %v .. %v", alpha.StartedAt, alpha.UpdatedAt)
	}
	check(idAlpha, []msg{
		{model.RoleUser, model.KindText, 1788700001004, "Download every telegram video"},
		{model.RoleAssistant, model.KindText, 1788700003000, "I will list the videos first."},
		{model.RoleAssistant, model.KindToolCall, 1788700003001, "pwsh: Get-ChildItem videos"},
		{model.RoleTool, model.KindToolOutput, 1788700004000, "video1.mp4\nvideo2.mp4"},
		{model.RoleAssistant, model.KindToolCall, 1788700004001, `read: C:\work\alpha\main.go`},
		{model.RoleTool, model.KindToolOutput, 1788700004002, "package main"},
		{model.RoleAssistant, model.KindText, 1788700005000, "Found 2 videos."},
		{model.RoleUser, model.KindText, 1788700007000, "also keep the subtitles"},
		{model.RoleUser, model.KindText, 1788800000000, "أريد مشروع تحميل الفيديو\n[image]\nsecond prompt, only in v3"},
		{model.RoleAssistant, model.KindText, 1788800001000, "Done."},
	})

	// Beta: plain JSONL, provider title, the 2 MB record kept whole, tool output capped.
	beta := got[idBeta]
	if beta.Cwd != `D:\proj\beta` || beta.Title != "Beta report summary" || beta.TitleSource != model.TitleAgent {
		t.Errorf("beta: cwd %q title %q (%s)", beta.Cwd, beta.Title, beta.TitleSource)
	}
	if !beta.StartedAt.Equal(ms(1788900000000)) || !beta.UpdatedAt.Equal(ms(1788900004000)) {
		t.Errorf("beta times %v .. %v", beta.StartedAt, beta.UpdatedAt)
	}
	if len(beta.Messages) == 5 {
		out := beta.Messages[4].Text
		if len(out) > model.MaxToolOutput || len(out) < model.MaxToolOutput-4 || !utf8.ValidString(out) || !strings.HasPrefix(f.tool, out) {
			t.Errorf("tool output: %d bytes, valid UTF-8 %v", len(out), utf8.ValidString(out))
		}
		beta.Messages[4].Text = "(checked)"
		got[idBeta] = beta
	}
	check(idBeta, []msg{
		{model.RoleUser, model.KindText, 1788900001000, "Summarize the beta report"},
		{model.RoleAssistant, model.KindToolCall, 1788900002000, `grep: TODO D:\proj\beta`},
		{model.RoleAssistant, model.KindToolCall, 1788900002001, `job_output: {"job_id":"job-7","wait":true}`},
		{model.RoleAssistant, model.KindText, 1788900003000, f.big},
		{model.RoleTool, model.KindToolOutput, 1788900004000, "(checked)"},
	})

	// Gamma: the v3 migration marker is not activity; the first prompt is the title.
	gamma := got[idGamma]
	if !gamma.UpdatedAt.Equal(ms(1788600002000)) {
		t.Errorf("gamma UpdatedAt %v, want the last real event (not session/end-seed)", gamma.UpdatedAt)
	}
	if gamma.Title != "gamma question" || gamma.TitleSource != model.TitlePrompt {
		t.Errorf("gamma title %q (%s)", gamma.Title, gamma.TitleSource)
	}
	check(idGamma, []msg{
		{model.RoleUser, model.KindText, 1788600001000, "gamma question"},
		{model.RoleAssistant, model.KindText, 1788600002000, "gamma answer"},
	})

	// A fork keeps its inherited messages and its own; the migration-style
	// end-seed marker does not count as activity.
	fork := got[idFork]
	if fork.Cwd != `C:\work\alpha` || !fork.StartedAt.Equal(ms(1788710000000)) || !fork.UpdatedAt.Equal(ms(1788710005000)) {
		t.Errorf("fork: cwd %q times %v .. %v", fork.Cwd, fork.StartedAt, fork.UpdatedAt)
	}
	check(idFork, []msg{
		{model.RoleUser, model.KindText, 1788700001004, "Download every telegram video"},
		{model.RoleAssistant, model.KindText, 1788700003000, "I will list the videos first."},
		{model.RoleUser, model.KindText, 1788710005000, "forked: try a different downloader"},
	})

	// No cwd; title = first prompt, whitespace collapsed, 100 runes.
	nt := got[idNoTitle]
	if nt.Cwd != "" {
		t.Errorf("no-cwd session cwd %q", nt.Cwd)
	}
	wantTitle := "Please write a long plan for the new mobile app with offline sync, push notifications, a settings sc"
	if nt.Title != wantTitle || nt.TitleSource != model.TitlePrompt {
		t.Errorf("no-title title %q (%s), want %q", nt.Title, nt.TitleSource, wantTitle)
	}
	check(idNoTitle, []msg{
		{model.RoleUser, model.KindText, 1788500001000, "   Please   write\n\na long plan for the new mobile app with offline sync, push notifications, a settings screen and dark mode support everywhere   "},
		{model.RoleAssistant, model.KindText, 1788500002000, "Here is the plan."},
	})
}

// TestGenerations: the newest generation this parser knows wins; a newer,
// unknown one is reported, not read; within a generation zstd wins over plain.
func TestGenerations(t *testing.T) {
	session := func(id, text string) []string {
		return []string{
			`{"type":"session","version":3,"id":"` + id + `","createdAt":1788000000000,"cwd":"C:\\g","delegationDepth":0}`,
			`{"type":"user/message","seq":0,"time":1788000001000,"data":{"content":[{"type":"text","text":"` + text + `"}],"source":{"kind":"user"},"role":"user","id":"u"}}`,
		}
	}
	plain := func(lines []string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }
	cases := []struct {
		name     string
		files    map[string][]byte
		want     string
		warnings int
	}{
		{"zstd beats plain in one generation", map[string][]byte{
			"session.v3.jsonl":      plain(session("s-1", "plain v3")),
			"session.v3.jsonl.zstd": frames(t, session("s-1", "zstd v3"), 1),
			"session.jsonl.zstd":    frames(t, session("s-1", "zstd v0"), 1),
		}, "zstd v3", 0},
		{"newest known generation", map[string][]byte{
			"session.jsonl.zstd":    frames(t, session("s-1", "v0"), 1),
			"session.v2.jsonl.zstd": frames(t, session("s-1", "v2"), 1),
			"session.v3.jsonl.zstd": frames(t, session("s-1", "v3"), 1),
		}, "v3", 0},
		{"unknown newer generation is reported, known one read", map[string][]byte{
			"session.v3.jsonl.zstd":  frames(t, session("s-1", "v3"), 1),
			"session.v12.jsonl.zstd": frames(t, session("s-1", "v12"), 1),
		}, "v3", 1},
		{"only an unknown generation: read it with a warning", map[string][]byte{
			"session.v4.jsonl.zstd": frames(t, session("s-1", "v4"), 1),
		}, "v4", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "session-x")
			var files []string
			for name, data := range c.files {
				write(t, filepath.Join(dir, name), data)
				files = append(files, filepath.Join(dir, name))
			}
			sort.Strings(files)
			res, err := New().Read(context.Background(), adapterstest.FixtureEnv(t, dir, nil), adapters.Source{Adapter: "dsh", Path: dir, Files: files})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Sessions) != 1 || len(res.Sessions[0].Messages) != 1 {
				t.Fatalf("sessions: %+v", res.Sessions)
			}
			if got := res.Sessions[0].Messages[0].Text; got != c.want {
				t.Errorf("read %q, want %q", got, c.want)
			}
			if len(res.Warnings) != c.warnings {
				t.Errorf("warnings %q, want %d", res.Warnings, c.warnings)
			}
		})
	}
}

func TestReadTornTailWarns(t *testing.T) {
	f := buildFixture(t)
	env := f.env(t, nil)
	src := adapters.Source{
		Adapter: "dsh",
		Path:    filepath.Join(f.dsh, "sessions", "_no-cwd", idNoTitle),
		Files:   []string{filepath.Join(f.dsh, "sessions", "_no-cwd", idNoTitle, "session.v3.jsonl.zstd")},
	}
	res, err := New().Read(context.Background(), env, src)
	if err != nil {
		t.Fatalf("a torn last frame must not fail the source: %v", err)
	}
	if len(res.Sessions) != 1 || len(res.Sessions[0].Messages) != 2 {
		t.Fatalf("sessions %d", len(res.Sessions))
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a warning about the torn tail")
	}
}

func TestReadGoneSourceFails(t *testing.T) {
	dir := t.TempDir()
	src := adapters.Source{Adapter: "dsh", Path: dir, Files: []string{filepath.Join(dir, "session.v3.jsonl.zstd")}}
	if _, err := New().Read(context.Background(), adapterstest.FixtureEnv(t, dir, nil), src); err == nil {
		t.Fatal("expected an error when no log file exists")
	}
}

func TestReadCanceled(t *testing.T) {
	f := buildFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := filepath.Join(f.dsh, "sessions", "--C-work-alpha--", idAlpha)
	src := adapters.Source{Adapter: "dsh", Path: dir, Files: []string{filepath.Join(dir, "session.v3.jsonl.zstd")}}
	if _, err := New().Read(ctx, f.env(t, nil), src); err == nil {
		t.Fatal("expected the context error")
	}
}

func TestDSHHome(t *testing.T) {
	f := buildFixture(t)

	// DSH_HOME points at a second root: both roots are read, the custom one carries Root.
	sessions, _ := adapterstest.ReadAll(t, New(), f.env(t, map[string]string{"DSH_HOME": f.custom}))
	got := byID(sessions)
	if len(sessions) != 6 {
		t.Fatalf("got %d sessions, want 6", len(sessions))
	}
	c := got[idCustom]
	if c.Root != f.custom || c.Cwd != `E:\custom` || c.Title != "custom root prompt" {
		t.Errorf("custom root session: root %q cwd %q title %q", c.Root, c.Cwd, c.Title)
	}
	if got[idAlpha].Root != "" {
		t.Errorf("default root session has Root %q", got[idAlpha].Root)
	}

	// DSH_HOME naming the default root (other case, trailing separator) is not read twice.
	same := strings.ToUpper(f.dsh) + string(os.PathSeparator)
	if os.PathSeparator != '\\' {
		same = f.dsh + "/"
	}
	srcs, err := New().Sources(f.env(t, map[string]string{"DSH_HOME": same}))
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 7 {
		t.Fatalf("DSH_HOME = default root: %d sources, want 7", len(srcs))
	}
	for _, s := range srcs {
		if s.Root != "" {
			t.Errorf("%s: Root %q, want default", s.Path, s.Root)
		}
	}
}

// TestReadOnly: listing and reading never create, change or remove anything in the agent tree.
func TestReadOnly(t *testing.T) {
	f := buildFixture(t)
	snap := func() string {
		var lines []string
		err := filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Directory times in NTFS parent listings update lazily; existence is what matters.
				lines = append(lines, p+"|dir")
				return nil
			}
			info, err := os.Stat(p)
			if err != nil {
				return err
			}
			lines = append(lines, fmt.Sprintf("%s|%s|%d|%d", p, info.Mode(), info.ModTime().UnixNano(), info.Size()))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(lines)
		return strings.Join(lines, "\n")
	}
	before := snap()
	adapterstest.ReadAll(t, New(), f.env(t, map[string]string{"DSH_HOME": f.custom}))
	if after := snap(); after != before {
		t.Fatalf("agent tree changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
