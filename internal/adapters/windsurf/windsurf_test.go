package windsurf

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "windsurf" || a.Name() != "Windsurf" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if c.Resume != nil || c.ProgramCandidates != nil || c.Fork != nil ||
		c.SkipPermissions != nil || c.App != nil || c.RootEnv != "" {
		t.Fatalf("Windsurf sessions cannot be resumed or opened: %+v", c)
	}
}

func TestSources(t *testing.T) {
	env := adapterstest.FixtureEnv(t, "testdata", nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	codeium := filepath.Join(env.Home, ".codeium")
	want := []string{
		filepath.Join(codeium, "windsurf", "cascade", "11111111-aaaa-4bbb-8ccc-000000000001.pb"),
		filepath.Join(codeium, "windsurf", "cascade", "22222222-aaaa-4bbb-8ccc-000000000002.PB"),
		filepath.Join(codeium, "windsurf-next", "cascade", "44444444-aaaa-4bbb-8ccc-000000000004.pb"),
	}
	if len(srcs) != len(want) {
		t.Fatalf("got %d sources, want %d: %+v", len(srcs), len(want), srcs)
	}
	for i, s := range srcs {
		if s.Path != want[i] {
			t.Errorf("source %d = %q, want %q", i, s.Path, want[i])
		}
		if s.Adapter != "windsurf" || s.Root != "" || len(s.Files) != 1 || s.Files[0] != s.Path {
			t.Errorf("source %d malformed: %+v", i, s)
		}
	}
}

func TestSourcesMissing(t *testing.T) {
	for name, env := range map[string]adapters.Env{
		"no home":      {},
		"empty home":   {Home: t.TempDir()},
		"missing home": {Home: filepath.Join(t.TempDir(), "gone")},
	} {
		srcs, err := New().Sources(env)
		if err != nil || len(srcs) != 0 {
			t.Errorf("%s: Sources = %v, %v; want nothing, no error", name, srcs, err)
		}
	}
}

// writeConv creates a conversation file in a temp Windsurf home and sets its
// modification time.
func writeConv(t *testing.T, home, dir, name string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(home, ".codeium", dir, "cascade", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte{0x8f, 0x13, 0xd2, 0x00, 0x7e}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRead(t *testing.T) {
	home := t.TempDir()
	mtime := time.Date(2025, 2, 23, 10, 30, 0, 0, time.UTC)
	p := writeConv(t, home, "windsurf", "0a1b2c3d-aaaa-4bbb-8ccc-000000000009.pb", mtime)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	env := adapters.Env{Home: home}
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 0 {
		t.Errorf("skipped = %d", skipped)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	s := sessions[0]
	if s.Agent != "windsurf" || s.NativeID != "0a1b2c3d-aaaa-4bbb-8ccc-000000000009" {
		t.Errorf("identity = %q %q", s.Agent, s.NativeID)
	}
	if s.Title != "Windsurf conversation 0a1b2c3d" || s.TitleSource != model.TitleNone {
		t.Errorf("title = %q (%s)", s.Title, s.TitleSource)
	}
	if s.Coverage != model.CoverageMeta || s.Origin != model.OriginLive {
		t.Errorf("coverage/origin = %s %s", s.Coverage, s.Origin)
	}
	if s.Cwd != "" || s.Root != "" || s.Machine != "" || len(s.Messages) != 0 {
		t.Errorf("unexpected fields: %+v", s)
	}
	if !s.UpdatedAt.Equal(mtime) || s.UpdatedAt.Location() != time.UTC {
		t.Errorf("UpdatedAt = %v, want %v (UTC)", s.UpdatedAt, mtime)
	}
	// The file was created now and back-dated, so the start is the older mtime.
	if !s.StartedAt.Equal(mtime) || s.StartedAt.Location() != time.UTC {
		t.Errorf("StartedAt = %v, want %v (UTC)", s.StartedAt, mtime)
	}

	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("Read changed the source file")
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.ModTime().Equal(mtime) {
		t.Errorf("source mtime changed: %v %v", fi.ModTime(), err)
	}
}

func TestReadShortStemAndCase(t *testing.T) {
	home := t.TempDir()
	mtime := time.Date(2025, 3, 1, 8, 0, 0, 0, time.UTC)
	writeConv(t, home, "windsurf", "abc.PB", mtime)
	sessions, _ := adapterstest.ReadAll(t, New(), adapters.Env{Home: home})
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions", len(sessions))
	}
	if s := sessions[0]; s.NativeID != "abc" || s.Title != "Windsurf conversation abc" {
		t.Errorf("session = %q %q", s.NativeID, s.Title)
	}
}

func TestSourcesSkipsEmptyStem(t *testing.T) {
	home := t.TempDir()
	writeConv(t, home, "windsurf", ".pb", time.Now())
	srcs, err := New().Sources(adapters.Env{Home: home})
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources = %v, %v; want none", srcs, err)
	}
}

func TestReadMissingFile(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, ".codeium", "windsurf", "cascade", "gone.pb")
	src := adapters.Source{Adapter: "windsurf", Path: p, Files: []string{p}}
	_, err := New().Read(context.Background(), adapters.Env{Home: home}, src)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

func TestReadCanceled(t *testing.T) {
	home := t.TempDir()
	p := writeConv(t, home, "windsurf", "abc.pb", time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := adapters.Source{Adapter: "windsurf", Path: p, Files: []string{p}}
	if _, err := New().Read(ctx, adapters.Env{Home: home}, src); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestReadNotAConversation(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := adapters.Source{Adapter: "windsurf", Path: p, Files: []string{p}}
	if _, err := New().Read(context.Background(), adapters.Env{Home: home}, src); err == nil {
		t.Fatal("Read of a non-.pb file succeeded")
	}
}

func TestStartedAtUsesCreationTime(t *testing.T) {
	// A file whose creation time is older than its modification time (the
	// normal case for a conversation that kept growing) starts at creation.
	home := t.TempDir()
	created := time.Date(2025, 2, 20, 9, 0, 0, 0, time.UTC)
	mtime := time.Date(2025, 2, 23, 18, 0, 0, 0, time.UTC)
	p := writeConv(t, home, "windsurf", "abc.pb", mtime)
	if !setCreationTime(t, p, created) {
		t.Skip("creation time cannot be set on this platform")
	}
	sessions, _ := adapterstest.ReadAll(t, New(), adapters.Env{Home: home})
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions", len(sessions))
	}
	if s := sessions[0]; !s.StartedAt.Equal(created) || !s.UpdatedAt.Equal(mtime) {
		t.Errorf("StartedAt/UpdatedAt = %v / %v, want %v / %v", s.StartedAt, s.UpdatedAt, created, mtime)
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
