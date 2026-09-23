package syncer

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/adapters/claude"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

type fileInfo struct {
	size  int64
	mtime time.Time
}

// snapshot records size and mtime of every entry under dir. It uses os.Stat:
// on Windows the directory listing of a file held open by a writer is stale.
func snapshot(t *testing.T, dir string) map[string]fileInfo {
	t.Helper()
	out := map[string]fileInfo{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		out[p] = fileInfo{info.Size(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSnapshot(t *testing.T, before, after map[string]fileInfo) {
	t.Helper()
	for p, b := range before {
		a, ok := after[p]
		switch {
		case !ok:
			t.Errorf("agent file %s disappeared", p)
		case a.size != b.size || !a.mtime.Equal(b.mtime):
			t.Errorf("agent file %s changed: %+v -> %+v", p, b, a)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("file %s was created in an agent folder", p)
		}
	}
}

// TestSyncLeavesClaudeFixturesUntouched syncs the Claude adapter's fixture
// tree twice: nothing in it may change or appear (spec §11).
func TestSyncLeavesClaudeFixturesUntouched(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "adapters", "claude", "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, root)
	h := newHarness(t)
	h.env = adapterstest.FixtureEnv(t, root, nil)

	first := h.run(t, claude.New())
	if first.Failed != 0 || first.Read == 0 || first.Read+first.RetryLater != first.Sources {
		t.Fatalf("first sync %+v", first)
	}
	if n := len(h.rows(t)); n == 0 {
		t.Fatal("no sessions archived from the fixtures")
	}
	second := h.run(t, claude.New())
	if second.Failed != 0 || second.Read != first.RetryLater || second.Unchanged != first.Read {
		t.Errorf("second sync %+v after %+v", second, first)
	}
	sameSnapshot(t, before, snapshot(t, root))
}

// sqliteFake reads a WAL-mode database through fsread.CopySQLite, like the
// SQLite adapters, and deliberately forgets its cleanup: the syncer removes
// each source's temp folder anyway.
type sqliteFake struct{ db string }

func (sqliteFake) ID() string          { return "sqlitefake" }
func (sqliteFake) Name() string        { return "SQLite fake" }
func (sqliteFake) Version() int        { return 1 }
func (sqliteFake) Caps() adapters.Caps { return adapters.Caps{} }

func (f sqliteFake) Sources(adapters.Env) ([]adapters.Source, error) {
	return []adapters.Source{{Adapter: "sqlitefake", Path: f.db, Files: fsread.SQLiteSidecars(f.db)}}, nil
}

func (sqliteFake) Read(_ context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	copyPath, _, err := fsread.CopySQLite(src.Path, env.TempDir) // cleanup "forgotten"
	if err != nil {
		return adapters.ReadResult{}, err
	}
	db, err := fsread.OpenSQLiteCopy(copyPath)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT body FROM msgs ORDER BY id`)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	defer rows.Close()
	at := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	s := model.Session{Agent: "sqlitefake", NativeID: "s1", StartedAt: at, UpdatedAt: at,
		Coverage: model.CoverageFull, Origin: model.OriginLive}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return adapters.ReadResult{}, err
		}
		s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: body})
	}
	return adapters.ReadResult{Sessions: []model.Session{s}}, rows.Err()
}

// TestSyncLeavesWALDatabaseUntouched syncs a WAL-mode store that an "agent"
// keeps open with un-checkpointed rows.
func TestSyncLeavesWALDatabaseUntouched(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agentDir, "store.db")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	defer writer.Close()
	for _, s := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE msgs(id INTEGER PRIMARY KEY, body TEXT)",
	} {
		if _, err := writer.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	for i := 0; i < 50; i++ {
		if _, err := writer.Exec(`INSERT INTO msgs(body) VALUES (?)`, fmt.Sprintf("synthetic message %d مرحبا", i)); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("fixture has no un-checkpointed WAL (%v)", err)
	}

	before := snapshot(t, agentDir)
	h := newHarness(t)
	rep := h.run(t, sqliteFake{db: path})
	if rep.Read != 1 || rep.Failed != 0 {
		t.Fatalf("sync %+v", rep)
	}
	if got := h.rows(t)["s1"].MessageCount; got != 50 {
		t.Errorf("%d messages archived, want 50", got)
	}
	sameSnapshot(t, before, snapshot(t, agentDir))

	left, err := os.ReadDir(h.env.TempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("temp copies left behind: %v", left)
	}
}
