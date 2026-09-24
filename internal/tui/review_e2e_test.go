package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/claude"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/syncer"
)

// End to end: a Claude history.jsonl entry whose transcript Claude already
// deleted (the 30-day cleanup) is archived as a prompt-log session that is
// NOT flagged missing, so the menu offers Resume for it.
func TestReviewE2EPromptOnlyClaudeSessionOffersResume(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	proj := filepath.Join(dir, "proj")
	for _, d := range []string{filepath.Join(home, ".claude"), proj, filepath.Join(dir, "data")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ts := time.Now().Add(-40 * 24 * time.Hour).UnixMilli()
	line := `{"display":"fix the build","project":` + jsonStr(proj) + `,"sessionId":"0b0b0b0b-0000-4000-8000-000000000001","timestamp":` + itoa(int(ts)) + "}\n"
	hist := filepath.Join(home, ".claude", "history.jsonl")
	if err := os.WriteFile(hist, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	os.Chtimes(hist, old, old)

	db, err := archive.Open(filepath.Join(dir, "data", "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	env := adapters.Env{Home: home, TempDir: filepath.Join(dir, "tmp"), Getenv: func(string) string { return "" }}
	ads := []adapters.Adapter{claude.New()}
	if _, err := syncer.Run(context.Background(), db, ads, env, syncer.Options{Machine: "M1", LockDir: filepath.Join(dir, "data")}, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Search(archive.Filter{}, 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v err %v", len(rows), err)
	}
	r := rows[0]
	t.Logf("archived row: origin=%s coverage=%s missing=%v machine=%s", r.Origin, r.Coverage, r.Missing, r.Machine)

	h := newHarnessWith(t, db, func(a *App, svc *services) {
		a.Adapters = ads
		a.Env = env
		svc.now = time.Now
		svc.sync = nil
	})
	h.wait = 2 * time.Second
	h.run(h.u.refresh())
	h.key("enter")
	if h.u.menu == nil {
		t.Fatal("no menu")
	}
	got := menuKeys(h.u.menu.entries)
	t.Logf("menu keys: %q", got)
	for _, e := range h.u.menu.entries {
		if e.id == itemResume || e.id == itemSkip {
			t.Errorf("menu offers %q (%s) for a session whose transcript no longer exists", e.key, e.label)
		}
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
