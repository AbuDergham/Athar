package tui

// Integration with the real archive and syncer: a synthetic adapter is synced
// into a temporary archive through the UI's own sync wiring, then the UI runs
// over the real *archive.DB. Skipped while the archive is a stub; while only
// the syncer is a stub, the sessions are applied to the archive directly.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/model"
	"github.com/AbuDergham/Athar/internal/search"
	"github.com/AbuDergham/Athar/internal/syncer"
)

func skipIfStub(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "not implemented") {
		t.Skipf("dependency not implemented yet: %v", err)
	}
}

// memAdapter serves fixed sessions from one real (dummy) file.
type memAdapter struct {
	file     string
	sessions []model.Session
}

func (a memAdapter) ID() string   { return "claude" }
func (a memAdapter) Name() string { return "Claude Code" }
func (a memAdapter) Version() int { return 1 }
func (a memAdapter) Sources(adapters.Env) ([]adapters.Source, error) {
	return []adapters.Source{{Adapter: "claude", Path: a.file, Files: []string{a.file}}}, nil
}
func (a memAdapter) Read(context.Context, adapters.Env, adapters.Source) (adapters.ReadResult, error) {
	return adapters.ReadResult{Sessions: a.sessions}, nil
}
func (a memAdapter) Caps() adapters.Caps {
	return adapters.Caps{Resume: []string{"claude", "--resume", "{id}"}, Fork: []string{"--fork-session"},
		SkipPermissions: []string{"--dangerously-skip-permissions"}, RootEnv: "CLAUDE_CONFIG_DIR"}
}

func TestIntegrationArchiveAndSync(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "athar-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := archive.Open(filepath.Join(dir, "athar-data", "archive.db"))
	skipIfStub(t, err)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	src := filepath.Join(dir, "agent", "s.jsonl")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "work", "videos")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	msg := func(role model.Role, text string, k int) model.Message {
		return model.Message{Role: role, Kind: model.KindText, Text: text, At: now.Add(time.Duration(k) * time.Second)}
	}
	ad := memAdapter{file: src, sessions: []model.Session{
		{Agent: "claude", NativeID: "11111111-1111-1111-1111-111111111111", Cwd: proj,
			Title: "مشروع تحميل", TitleSource: model.TitleAgent, StartedAt: now, UpdatedAt: now.Add(3 * time.Second),
			Coverage: model.CoverageFull, Origin: model.OriginLive, Messages: []model.Message{
				msg(model.RoleUser, "please download every video from the channel", 0),
				msg(model.RoleAssistant, "سأحمّل الفيديوهات من القناة الآن", 1),
				msg(model.RoleUser, "thanks", 2),
			}},
		{Agent: "claude", NativeID: "22222222-2222-2222-2222-222222222222", Cwd: proj,
			Title: "Bot", TitleSource: model.TitleAgent, StartedAt: now, UpdatedAt: now,
			Coverage: model.CoverageFull, Origin: model.OriginLive, Messages: []model.Message{
				msg(model.RoleUser, "deploy the bot", 0),
				msg(model.RoleAssistant, "done deploying", 1),
			}},
	}}

	app := App{
		DB: db, Adapters: []adapters.Adapter{ad}, Machine: "M1", Hostname: "PC",
		DataDir: filepath.Join(dir, "athar-data"), Version: "v1.0.0",
		Env: adapters.Env{Home: filepath.Join(dir, "home"), TempDir: filepath.Join(dir, "tmp"), Getenv: func(string) string { return "" }},
	}
	// The UI's real sync wiring (syncer.Run with this machine and lock dir).
	var progress []syncer.Progress
	rep, err := realServices(app).sync(context.Background(), func(p syncer.Progress) { progress = append(progress, p) })
	switch {
	case err != nil && strings.Contains(err.Error(), "not implemented"):
		t.Logf("syncer is a stub (%v): applying the sessions directly", err)
		if _, err := db.ApplySource(archive.SourceRecord{Machine: app.Machine, Adapter: ad.ID(), Path: src,
			Fingerprint: "fp", AdapterVersion: ad.Version()}, ad.sessions); err != nil {
			t.Fatal(err)
		}
		if err := db.SetMeta(metaLastSync, time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	case err != nil:
		t.Fatalf("sync: %v", err)
	case rep.Read != 1 || len(progress) == 0:
		t.Fatalf("sync report %+v, progress %v", rep, progress)
	}

	h := newHarnessWith(t, db, func(a *App, svc *services) {
		*a = app
		svc.now = time.Now
		svc.sync = func(context.Context, func(syncer.Progress)) (syncer.Report, error) { return syncer.Report{}, nil }
	})
	h.wait = 2 * time.Second
	h.run(h.u.refresh()) // Init's queries may have outlasted the default wait
	h.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	v := h.view()
	for _, want := range []string{"2 sessions · 1 agent", "Bot", "Claude Code"} {
		if !strings.Contains(v, want) {
			t.Errorf("timeline lacks %q\n%s", want, v)
		}
	}

	// Arabic search: the hit message is the one the archive reports (seq → index).
	h.typeText("الفيديوهات")
	if len(h.u.rows) == 0 {
		t.Fatalf("no rows for an Arabic query\n%s", h.view())
	}
	r := h.u.rows[0]
	msgs, err := db.Messages(r.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	i := seqIndex(r.HitSeq, len(msgs))
	if i < 0 || !strings.Contains(search.Normalize(msgs[i].Text), search.Normalize("الفيديوهات")) {
		t.Fatalf("HitSeq %d does not point at the matching message (index %d of %d): the seq→index mapping is wrong", r.HitSeq, i, len(msgs))
	}
	if r.Snippet == "" || !strings.Contains(r.Snippet, "الفيديوهات") {
		t.Errorf("snippet %q lacks the original Arabic text", r.Snippet)
	}

	// Reading page: ScrollTo and Hits point at matching messages.
	h.key("enter")
	h.key("o")
	if len(h.c.pages) != 1 {
		t.Fatalf("reading page not written (menu %+v)", h.u.menu)
	}
	p := h.c.pages[0]
	if p.ScrollTo != i || len(p.Hits) == 0 {
		t.Errorf("page ScrollTo %d (want %d) hits %v", p.ScrollTo, i, p.Hits)
	}
	for _, hi := range p.Hits {
		if !strings.Contains(search.Normalize(p.Messages[hi].Text), search.Normalize("الفيديوهات")) {
			t.Errorf("hit %d does not match: %q", hi, p.Messages[hi].Text)
		}
	}

	// Per-session AND with a partial fallback: "channel deploying" matches each session once.
	h.key("esc")
	h.typeText("channel deploying")
	if !strings.Contains(h.view(), "Partial matches (1 of 2 words)") {
		t.Errorf("partial matches header missing\n%s", h.view())
	}

	// Sources screen reads the archive's source table and last sync time.
	h.key("f2")
	v = strings.Join(strings.Fields(h.view()), " ") // long temp paths wrap
	for _, want := range []string{"Claude Code", "1 source", "last sync"} {
		if !strings.Contains(v, want) {
			t.Errorf("sources screen lacks %q\n%s", want, v)
		}
	}
}
