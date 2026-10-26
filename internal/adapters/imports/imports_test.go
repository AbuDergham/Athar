package imports

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

const minimalZedID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"

// fixtureEnv builds the whole synthetic tree and returns its Env and paths.
func fixtureEnv(t *testing.T) (adapters.Env, fixturePaths) {
	t.Helper()
	old := hostname
	hostname = func() (string, error) { return "TESTHOST", nil }
	t.Cleanup(func() { hostname = old })

	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	p := newFixturePaths(root)

	createDB(t, filepath.Join(env.Home, ".agentsview", "sessions.db"), avSchema, true, fillAgentsView(t, p))
	createDB(t, filepath.Join(env.Home, ".pi", "agent", "sessions", "sessions.db"), psmSchema, false, fillPSM(t, p))

	backup := filepath.Join(env.Documents, backupDir, "2026-09-23")
	createDB(t, filepath.Join(backup, "agentsview-sessions.db"), avSchema, false, fillAgentsView(t, p))
	createDB(t, filepath.Join(backup, "pi-session-manager-sessions.db"), psmSchema, false, fillPSM(t, p))
	old2 := filepath.Join(env.Documents, backupDir, "old")
	createDB(t, filepath.Join(old2, "neutral.db"), avMinimalSchema, false, func(db *sql.DB) {
		zed := filepath.Join(env.LocalAppData, "Zed", "threads", "threads.db") + "#" + minimalZedID
		mustExec(t, db, `INSERT INTO sessions (id, agent, file_path) VALUES (?,?,?)`, "zed:"+minimalZedID, "zed", zed)
		mustExec(t, db, `INSERT INTO messages (session_id, ordinal, role, content) VALUES (?,?,?,?)`, "zed:"+minimalZedID, 0, "user", "hi zed")
	})
	createDB(t, filepath.Join(old2, "unrelated.db"), unrelatedSchema, false, nil)
	if err := os.WriteFile(filepath.Join(old2, "readme.txt"), []byte("not a db"), 0o644); err != nil {
		t.Fatal(err)
	}
	return env, p
}

func sourcePaths(t *testing.T, a adapters.Adapter, env adapters.Env) []string {
	t.Helper()
	srcs, err := a.Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range srcs {
		if s.Adapter != a.ID() {
			t.Errorf("source %s has adapter %q, want %q", s.Path, s.Adapter, a.ID())
		}
		if len(s.Files) == 0 || s.Files[0] != s.Path {
			t.Errorf("source %s Files = %v", s.Path, s.Files)
		}
		rel, _ := filepath.Rel(filepath.Dir(env.Home), s.Path)
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

func TestSources(t *testing.T) {
	env, _ := fixtureEnv(t)
	cases := []struct {
		a    adapters.Adapter
		want []string
	}{
		{NewAgentsView(), []string{
			"home/.agentsview/sessions.db",
			"documents/AI-History-Backup/2026-09-23/agentsview-sessions.db",
			"documents/AI-History-Backup/old/neutral.db",
			"documents/AI-History-Backup/old/unrelated.db",
		}},
		{NewPSM(), []string{
			"home/.pi/agent/sessions/sessions.db",
			"documents/AI-History-Backup/2026-09-23/pi-session-manager-sessions.db",
			"documents/AI-History-Backup/old/neutral.db",
			"documents/AI-History-Backup/old/unrelated.db",
		}},
	}
	for _, c := range cases {
		got := sourcePaths(t, c.a, env)
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s sources:\n got %v\nwant %v", c.a.ID(), got, c.want)
		}
	}

	// The WAL-mode primary lists its -wal (and -shm) for the fingerprint.
	srcs, _ := NewAgentsView().Sources(env)
	if len(srcs[0].Files) < 2 || !strings.HasSuffix(srcs[0].Files[1], "-wal") {
		t.Errorf("primary Files = %v, want db + -wal", srcs[0].Files)
	}

	// Environment overrides.
	alt := t.TempDir()
	createDB(t, filepath.Join(alt, "av", "sessions.db"), avMinimalSchema, false, nil)
	createDB(t, filepath.Join(alt, "pi", "sessions", "sessions.db"), psmSchema, false, nil)
	env2 := env
	env2.Getenv = func(k string) string {
		switch k {
		case "AGENTSVIEW_DATA_DIR":
			return filepath.Join(alt, "av")
		case "PI_CODING_AGENT_DIR":
			return filepath.Join(alt, "pi")
		}
		return ""
	}
	for _, a := range []adapters.Adapter{NewAgentsView(), NewPSM()} {
		srcs, _ := a.Sources(env2)
		if len(srcs) != 5 || !strings.HasPrefix(srcs[0].Path, alt) {
			t.Errorf("%s with env override: %d sources, first %v", a.ID(), len(srcs), srcs[0].Path)
		}
	}

	// Nothing installed: no sources, no error.
	empty := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	for _, a := range []adapters.Adapter{NewAgentsView(), NewPSM()} {
		if srcs, err := a.Sources(empty); err != nil || len(srcs) != 0 {
			t.Errorf("%s on empty env: %v, %v", a.ID(), srcs, err)
		}
	}
}

func readOne(t *testing.T, a adapters.Adapter, env adapters.Env, path string) adapters.ReadResult {
	t.Helper()
	res, err := a.Read(context.Background(), env, adapters.Source{Adapter: a.ID(), Path: path, Files: []string{path}})
	if err != nil {
		t.Fatalf("%s Read %s: %v", a.ID(), path, err)
	}
	return res
}

func byKey(ss []model.Session) map[string]model.Session {
	m := map[string]model.Session{}
	for _, s := range ss {
		m[s.Agent+"/"+s.NativeID] = s
	}
	return m
}

func keys(m map[string]model.Session) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestAgentsViewRead(t *testing.T) {
	env, _ := fixtureEnv(t)
	primary := filepath.Join(env.Home, ".agentsview", "sessions.db")
	if fi, err := os.Stat(primary + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("fixture should be in WAL mode with un-checkpointed rows: %v", err)
	}
	res := readOne(t, NewAgentsView(), env, primary)
	if len(res.Warnings) != 0 || res.Skipped != 0 {
		t.Errorf("warnings %v skipped %d", res.Warnings, res.Skipped)
	}
	got := byKey(res.Sessions)
	want := []string{
		"agy/" + idAgy,
		"amp/T-0001",
		"claude/" + idAlpha,
		"claude/" + idGamma,
		"claude/" + idMissing,
		"codex/" + idCodex,
		"copilot/" + idCopilot,
		"qoder/" + idQoder,
	}
	if strings.Join(keys(got), " ") != strings.Join(want, " ") {
		t.Fatalf("sessions:\n got %v\nwant %v", keys(got), want)
	}
	for _, s := range res.Sessions {
		if s.Origin != model.OriginAgentsView {
			t.Errorf("%s origin %q", s.NativeID, s.Origin)
		}
	}

	a := got["claude/"+idAlpha]
	if a.Root != "" || a.Cwd != `C:\work\alpha` || a.Machine != "" || a.Coverage != model.CoverageFull {
		t.Errorf("alpha: root %q cwd %q machine %q coverage %q", a.Root, a.Cwd, a.Machine, a.Coverage)
	}
	if a.Title != "Fix the build please" || a.TitleSource != model.TitlePrompt {
		t.Errorf("alpha title %q (%s)", a.Title, a.TitleSource)
	}
	if !a.StartedAt.Equal(utc("2026-09-01T08:00:00.5Z")) || !a.UpdatedAt.Equal(utc("2026-09-01T08:10:00.123456789Z")) {
		t.Errorf("alpha times %v %v", a.StartedAt, a.UpdatedAt)
	}
	wantMsgs := []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, At: utc("2026-09-01T08:00:01Z"), Text: "Fix   the build\nplease"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: utc("2026-09-01T08:00:02.1Z"), Text: "I will run the build.\n[Bash]\n$ go build ./..."},
		{Role: model.RoleTool, Kind: model.KindToolOutput, At: utc("2026-09-01T08:00:02.1Z"), Text: "build ok"},
		{Role: model.RoleUser, Kind: model.KindText, At: utc("2026-09-01T08:06:00Z"), Text: "شكراً، هل تم إصلاح المشروع؟"},
		{Role: model.RoleUser, Kind: model.KindText, At: utc("2026-09-01T08:08:00Z"), Text: "!git status"},
	}
	checkMsgs(t, "alpha", a.Messages, wantMsgs)
	for _, m := range a.Messages {
		if strings.Contains(m.Text, "reasoning") || strings.Contains(m.Text, "Thinking") || strings.Contains(m.Text, "compacted") {
			t.Errorf("alpha kept thinking/system text: %q", m.Text)
		}
	}

	g := got["claude/"+idGamma]
	if g.Root != filepath.Join(env.Home, ".claude-local") || g.Title != "Renamed by user" || g.TitleSource != model.TitleUser {
		t.Errorf("gamma: root %q title %q (%s)", g.Root, g.Title, g.TitleSource)
	}

	c := got["codex/"+idCodex]
	if len(c.Messages) != 6 {
		t.Fatalf("codex messages: %+v", c.Messages)
	}
	// the rendered call is not repeated as a tool_call message
	checkMsgs(t, "codex rendered call", c.Messages[4:], []model.Message{
		{Role: model.RoleAssistant, Kind: model.KindText, At: utc("2026-09-01T10:00:09Z"), Text: "[Bash]\n$ pwd"},
		{Role: model.RoleTool, Kind: model.KindToolOutput, At: utc("2026-09-01T10:00:09Z"), Text: `D:\proj\codex`},
	})
	if c.Messages[1].Kind != model.KindToolCall || c.Messages[1].Text != "shell: bash -lc ls -la" {
		t.Errorf("codex tool call %+v", c.Messages[1])
	}
	out := c.Messages[2]
	if out.Kind != model.KindToolOutput || len(out.Text) > model.MaxToolOutput || len(out.Text) < model.MaxToolOutput-2 || !strings.HasPrefix(out.Text, "xxx") {
		t.Errorf("codex tool output: kind %s len %d", out.Kind, len(out.Text))
	}
	if c.Messages[3].Kind != model.KindToolCall || !strings.HasPrefix(c.Messages[3].Text, `update_plan: {"plan"`) {
		t.Errorf("codex second call %+v", c.Messages[3])
	}

	cp := got["copilot/"+idCopilot]
	if cp.Machine != "OTHER-PC" || cp.Cwd != "" || len(cp.Messages) != 2 {
		t.Errorf("copilot: machine %q cwd %q msgs %d", cp.Machine, cp.Cwd, len(cp.Messages))
	}

	ag := got["agy/"+idAgy]
	if !ag.StartedAt.Equal(utc("2026-06-17T08:04:33.6326837Z")) || !ag.UpdatedAt.Equal(utc("2026-06-17T08:04:40.7310381Z")) {
		t.Errorf("agy times from messages: %v %v", ag.StartedAt, ag.UpdatedAt)
	}
	if ag.Title != "hello agy" || ag.TitleSource != model.TitlePrompt {
		t.Errorf("agy title %q (%s)", ag.Title, ag.TitleSource)
	}

	q := got["qoder/"+idQoder]
	if q.Coverage != model.CoverageTitles || q.Title != "Qoder session name" || q.TitleSource != model.TitleAgent || len(q.Messages) != 0 {
		t.Errorf("qoder: coverage %q title %q (%s)", q.Coverage, q.Title, q.TitleSource)
	}
}

func checkMsgs(t *testing.T, name string, got, want []model.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d messages, want %d: %+v", name, len(got), len(want), got)
		return
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Kind != want[i].Kind || !got[i].At.Equal(want[i].At) || got[i].Text != want[i].Text {
			t.Errorf("%s message %d:\n got %+v\nwant %+v", name, i, got[i], want[i])
		}
	}
}

func TestPSMRead(t *testing.T) {
	env, _ := fixtureEnv(t)
	res := readOne(t, NewPSM(), env, filepath.Join(env.Home, ".pi", "agent", "sessions", "sessions.db"))
	if len(res.Warnings) != 0 || res.Skipped != 0 {
		t.Errorf("warnings %v skipped %d", res.Warnings, res.Skipped)
	}
	got := byKey(res.Sessions)
	want := []string{"claude/" + idAlpha, "claude/" + idGamma, "codex/" + idCodex, "pi/pi0001"}
	if strings.Join(keys(got), " ") != strings.Join(want, " ") {
		t.Fatalf("sessions:\n got %v\nwant %v", keys(got), want)
	}
	a := got["claude/"+idAlpha]
	if a.Origin != model.OriginPSM || a.Root != "" || a.Cwd != `C:\work\alpha` || a.Coverage != model.CoverageFull || a.Machine != "" {
		t.Errorf("alpha: %+v", a)
	}
	if a.Title != "Fix the build..." || a.TitleSource != model.TitlePrompt {
		t.Errorf("alpha title %q (%s)", a.Title, a.TitleSource)
	}
	if !a.StartedAt.Equal(utc("2026-09-01T08:00:00.5Z")) || !a.UpdatedAt.Equal(utc("2026-09-01T08:10:00.1234567Z")) {
		t.Errorf("alpha times %v %v", a.StartedAt, a.UpdatedAt)
	}
	checkMsgs(t, "alpha", a.Messages, []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, At: utc("2026-09-01T08:00:01Z"), Text: "Fix the build please"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: utc("2026-09-01T08:00:03Z"), Text: "Running the build now."},
		{Role: model.RoleAssistant, Kind: model.KindText, At: utc("2026-09-01T08:00:04Z"), Text: "Done, the build passes."},
	})

	g := got["claude/"+idGamma]
	if g.Root != filepath.Join(env.Home, ".claude-local") || g.Title != "Gamma work title" || g.TitleSource != model.TitleAgent {
		t.Errorf("gamma: root %q title %q (%s)", g.Root, g.Title, g.TitleSource)
	}
	c := got["codex/"+idCodex]
	if c.Title != "Codex thread title" || c.TitleSource != model.TitleAgent || len(c.Messages) != 2 {
		t.Errorf("codex: title %q (%s) msgs %d", c.Title, c.TitleSource, len(c.Messages))
	}
	pi := got["pi/pi0001"]
	if pi.Title != "pi prompt" || pi.TitleSource != model.TitlePrompt || pi.Cwd != `C:\work\pi` {
		t.Errorf("pi: %+v", pi)
	}
}

// TestAllSources reads every listed source: backups import the same sessions
// (the archive dedupes by agent+native id), neutral backups go to the importer
// whose schema matches, and unrelated databases give nothing.
func TestAllSources(t *testing.T) {
	env, _ := fixtureEnv(t)
	av, skipped := adapterstest.ReadAll(t, NewAgentsView(), env)
	if skipped != 0 {
		t.Errorf("agentsview skipped %d", skipped)
	}
	count := map[string]int{}
	for _, s := range av {
		count[s.Agent+"/"+s.NativeID]++
	}
	if count["claude/"+idAlpha] != 2 {
		t.Errorf("alpha from primary + backup: %d", count["claude/"+idAlpha])
	}
	z := count["zed/"+minimalZedID]
	if z != 1 {
		t.Fatalf("minimal-schema zed session: %d", z)
	}
	for _, s := range av {
		if s.Agent == "zed" && (len(s.Messages) != 1 || s.Coverage != model.CoverageFull || s.Title != "hi zed" || !s.StartedAt.IsZero()) {
			t.Errorf("zed: %+v", s)
		}
	}
	if len(av) != 17 {
		t.Errorf("agentsview total %d, want 17 (8 + 8 + 1)", len(av))
	}

	psm, _ := adapterstest.ReadAll(t, NewPSM(), env)
	if len(psm) != 8 {
		t.Errorf("psm total %d, want 8 (4 + 4)", len(psm))
	}

	// No warning for a neutral backup of the other tool; a warning for a
	// primary location that holds something else.
	for _, a := range []adapters.Adapter{NewAgentsView(), NewPSM()} {
		srcs, _ := a.Sources(env)
		for _, s := range srcs {
			res, err := a.Read(context.Background(), env, s)
			if err != nil || len(res.Warnings) != 0 {
				t.Errorf("%s %s: %v %v", a.ID(), s.Path, err, res.Warnings)
			}
		}
	}
	bad := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	createDB(t, filepath.Join(bad.Home, ".agentsview", "sessions.db"), unrelatedSchema, false, nil)
	srcs, _ := NewAgentsView().Sources(bad)
	res, err := NewAgentsView().Read(context.Background(), bad, srcs[0])
	if err != nil || len(res.Sessions) != 0 || len(res.Warnings) != 1 {
		t.Errorf("unrelated primary: %v %+v", err, res)
	}
}

// TestReadOnly: reading leaves the source folders exactly as they were.
func TestReadOnly(t *testing.T) {
	env, _ := fixtureEnv(t)
	dirs := []string{
		filepath.Join(env.Home, ".agentsview"),
		filepath.Join(env.Home, ".pi", "agent", "sessions"),
		filepath.Join(env.Documents, backupDir),
	}
	snap := func() string {
		var b strings.Builder
		for _, d := range dirs {
			filepath.Walk(d, func(p string, fi os.FileInfo, err error) error {
				if err == nil {
					b.WriteString(p + "|" + fi.ModTime().String())
					if !fi.IsDir() {
						b.WriteString("|" + strconv.FormatInt(fi.Size(), 10))
					}
					b.WriteString("\n")
				}
				return nil
			})
		}
		return b.String()
	}
	before := snap()
	adapterstest.ReadAll(t, NewAgentsView(), env)
	adapterstest.ReadAll(t, NewPSM(), env)
	if after := snap(); after != before {
		t.Errorf("source folders changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestIDsAndNames(t *testing.T) {
	for _, c := range []struct {
		a        adapters.Adapter
		id, name string
	}{
		{NewAgentsView(), "agentsview", "AgentsView index"},
		{NewPSM(), "psm", "Pi Session Manager index"},
	} {
		if c.a.ID() != c.id || c.a.Name() != c.name || c.a.Version() < 1 {
			t.Errorf("%s: %q %q %d", c.id, c.a.ID(), c.a.Name(), c.a.Version())
		}
		if caps := c.a.Caps(); caps.Resume != nil || caps.App != nil {
			t.Errorf("%s caps should be empty: %+v", c.id, caps)
		}
	}
}

func TestRealData(t *testing.T) {
	t.Run("agentsview", func(t *testing.T) { adapterstest.RealData(t, NewAgentsView()) })
	t.Run("psm", func(t *testing.T) { adapterstest.RealData(t, NewPSM()) })
}
