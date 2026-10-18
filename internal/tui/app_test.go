package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/config"
	"github.com/AbuDergham/Athar/internal/launch"
	"github.com/AbuDergham/Athar/internal/model"
	"github.com/AbuDergham/Athar/internal/render"
	"github.com/AbuDergham/Athar/internal/syncer"
	"github.com/AbuDergham/Athar/internal/update"
)

// ---- fakes ----

type fakeStore struct {
	mu      sync.Mutex
	rows    []archive.Row
	msgs    map[int64][]model.Message
	hits    map[int64][]int
	sources []archive.SourceStatus
	meta    map[string]string
	filters []archive.Filter
}

func (f *fakeStore) Search(fl archive.Filter, limit, offset int) ([]archive.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filters = append(f.filters, fl)
	var out []archive.Row
	for _, r := range f.rows {
		if fl.Agent != "" && r.Agent != fl.Agent {
			continue
		}
		if !fl.Since.IsZero() && r.UpdatedAt.Before(fl.Since) {
			continue
		}
		r.HitSeq, r.MatchedTerms, r.TotalTerms = -1, 0, 0
		if fl.Query != "" {
			i := -1
			for k, m := range f.msgs[r.SessionID] {
				if strings.Contains(strings.ToLower(m.Text), strings.ToLower(fl.Query)) {
					i = k
					break
				}
			}
			if i < 0 && !strings.Contains(strings.ToLower(r.Title), strings.ToLower(fl.Query)) {
				continue
			}
			r.HitSeq, r.MatchedTerms, r.TotalTerms = i, 1, 1
			if i >= 0 {
				r.Snippet = "…" + f.msgs[r.SessionID][i].Text + "…"
			}
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if offset >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) Messages(id int64) ([]model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.msgs[id], nil
}

func (f *fakeStore) HitSeqs(id int64, q string) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[id], nil
}

func (f *fakeStore) Agents() ([]archive.AgentCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := map[string]int{}
	for _, r := range f.rows {
		n[r.Agent]++
	}
	var out []archive.AgentCount
	for a, c := range n {
		out = append(out, archive.AgentCount{Agent: a, Sessions: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Agent < out[j].Agent
	})
	return out, nil
}

func (f *fakeStore) Sources(string) ([]archive.SourceStatus, error) { return f.sources, nil }

func (f *fakeStore) Meta(k string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.meta[k], nil
}

func (f *fakeStore) SetMeta(k, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meta[k] = v
	return nil
}

func (f *fakeStore) Path() string { return `D:\Tools\Athar\athar-data\archive.db` }

func (f *fakeStore) lastFilter() archive.Filter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.filters[len(f.filters)-1]
}

type fakeAdapter struct {
	id, name string
	caps     adapters.Caps
}

func (a fakeAdapter) ID() string                                      { return a.id }
func (a fakeAdapter) Name() string                                    { return a.name }
func (a fakeAdapter) Version() int                                    { return 1 }
func (a fakeAdapter) Sources(adapters.Env) ([]adapters.Source, error) { return nil, nil }
func (a fakeAdapter) Read(context.Context, adapters.Env, adapters.Source) (adapters.ReadResult, error) {
	return adapters.ReadResult{}, nil
}
func (a fakeAdapter) Caps() adapters.Caps { return a.caps }

type calls struct {
	mu        sync.Mutex
	built     []launch.Target
	skip      []bool
	actions   []launch.Action
	started   []launch.Command
	copied    []string
	folders   []string
	opened    []string
	pages     []render.Page
	syncs     int
	installed int
}

func (c *calls) lock() func() { c.mu.Lock(); return c.mu.Unlock }

type harness struct {
	t        *testing.T
	u        *ui
	st       *fakeStore
	c        *calls
	quit     bool
	preErr   error
	syncErr  error
	progress []syncer.Progress
	now      time.Time
	wait     time.Duration // how long run waits for a command (default 300 ms)
}

func (h *harness) waitFor() time.Duration {
	if h.wait > 0 {
		return h.wait
	}
	return 300 * time.Millisecond
}

var testNow = time.Date(2026, 9, 23, 18, 0, 0, 0, time.Local)

func newHarness(t *testing.T, rows []archive.Row, msgs map[int64][]model.Message) *harness {
	st := &fakeStore{rows: rows, msgs: msgs, hits: map[int64][]int{}, meta: map[string]string{}}
	h := newHarnessWith(t, st, nil)
	h.st = st
	return h
}

// newHarnessWith builds the UI over any store; customize may change the App
// and the services before the UI starts.
func newHarnessWith(t *testing.T, st store, customize func(*App, *services)) *harness {
	h := &harness{t: t, c: &calls{}, now: testNow}
	app := App{
		Adapters: []adapters.Adapter{
			fakeAdapter{"claude", "Claude Code", adapters.Caps{
				Resume: []string{"claude", "--resume", "{id}"}, Fork: []string{"--fork-session"},
				SkipPermissions: []string{"--dangerously-skip-permissions"}, RootEnv: "CLAUDE_CONFIG_DIR"}},
			fakeAdapter{"codex", "Codex", adapters.Caps{Resume: []string{"codex", "resume", "{id}"}}},
			fakeAdapter{"cursor", "Cursor", adapters.Caps{App: &adapters.AppCommand{Name: "Cursor", Program: "cursor", Args: []string{"{cwd}"}}}},
		},
		Machine: "M1", Hostname: "PC", DataDir: t.TempDir(), Version: "v1.0.0",
	}
	svc := services{
		now: func() time.Time { return h.now },
		build: func(tg launch.Target, caps adapters.Caps, a launch.Action, skip bool, ov []string) (launch.Command, error) {
			defer h.c.lock()()
			if caps.Resume == nil && caps.App == nil && ov == nil {
				return launch.Command{}, launch.ErrUnsupported
			}
			h.c.built = append(h.c.built, tg)
			h.c.skip = append(h.c.skip, skip)
			h.c.actions = append(h.c.actions, a)
			cmd := launch.Command{Title: tg.Title, Dir: tg.Cwd, Program: "prog", PowerShell: "prog " + tg.NativeID}
			if caps.Resume == nil && caps.App != nil {
				cmd.IsApp, cmd.AppName = true, caps.App.Name
			}
			return cmd, nil
		},
		preflight: func(launch.Command) error { return h.preErr },
		start: func(c launch.Command) error {
			defer h.c.lock()()
			h.c.started = append(h.c.started, c)
			return nil
		},
		copy: func(s string) error {
			defer h.c.lock()()
			h.c.copied = append(h.c.copied, s)
			return nil
		},
		openFolder: func(p string) error {
			defer h.c.lock()()
			h.c.folders = append(h.c.folders, p)
			return nil
		},
		openFile: func(p string) error {
			defer h.c.lock()()
			h.c.opened = append(h.c.opened, p)
			return nil
		},
		live: func([]string) map[string]launch.LiveInfo { return map[string]launch.LiveInfo{} },
		render: func(p render.Page, dir string) (string, error) {
			defer h.c.lock()()
			h.c.pages = append(h.c.pages, p)
			return dir + `\page.html`, nil
		},
		readDir: `C:\Temp\athar\read`,
		sync: func(ctx context.Context, progress func(syncer.Progress)) (syncer.Report, error) {
			func() { defer h.c.lock()(); h.c.syncs++ }()
			for _, p := range h.progress {
				progress(p)
			}
			return syncer.Report{}, h.syncErr
		},
		checkUpdate: func(context.Context, string) (*update.Release, error) { return nil, nil },
		install: func(context.Context, *update.Release) error {
			defer h.c.lock()()
			h.c.installed++
			return nil
		},
		dirExists: func(p string) bool { return p != "" && !strings.Contains(p, "gone") },
		fileSize:  func(string) int64 { return 3 << 20 },
	}
	if customize != nil {
		customize(&app, &svc)
	}
	h.u = newUI(context.Background(), app, st, svc)
	h.u.loadStartupMeta()
	h.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.run(h.u.Init())
	return h
}

// run executes a command and feeds its messages back into Update until
// nothing is left. Commands that block (the 60 s sync tick) are abandoned.
func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		ch := make(chan tea.Msg, 1)
		go func() { ch <- c() }()
		var msg tea.Msg
		select {
		case msg = <-ch:
		case <-time.After(h.waitFor()):
			continue
		}
		switch m := msg.(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, m...)
		case tea.QuitMsg:
			h.quit = true
		default:
			_, next := h.u.Update(m)
			queue = append(queue, next)
		}
	}
}

func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.u.Update(msg)
	h.run(cmd)
}

func (h *harness) key(s string) {
	h.t.Helper()
	var k tea.KeyPressMsg
	switch s {
	case "enter":
		k.Code = tea.KeyEnter
	case "shift+enter":
		k.Code, k.Mod = tea.KeyEnter, tea.ModShift
	case "esc":
		k.Code = tea.KeyEscape
	case "tab":
		k.Code = tea.KeyTab
	case "up":
		k.Code = tea.KeyUp
	case "down":
		k.Code = tea.KeyDown
	case "end":
		k.Code = tea.KeyEnd
	case "home":
		k.Code = tea.KeyHome
	case "f2":
		k.Code = tea.KeyF2
	case "ctrl+t":
		k.Code, k.Mod = 't', tea.ModCtrl
	case "ctrl+down":
		k.Code, k.Mod = tea.KeyDown, tea.ModCtrl
	case "ctrl+up":
		k.Code, k.Mod = tea.KeyUp, tea.ModCtrl
	default:
		r := []rune(s)
		k.Code, k.Text = r[0], s
	}
	h.send(k)
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.key(string(r))
	}
}

func (h *harness) view() string { return ansi.Strip(h.u.View().Content) }

// ---- fixtures ----

func fixture() ([]archive.Row, map[int64][]model.Message) {
	day := func(d, hour int) time.Time { return time.Date(2026, 9, d, hour, 0, 0, 0, time.Local) }
	rows := []archive.Row{
		{SessionID: 1, Agent: "claude", NativeID: "aaaa-1", Title: "Download telegram videos", ProjectName: "TelegramVideos",
			ProjectPath: `D:\Projects\TelegramVideos`, Cwd: `D:\Projects\TelegramVideos`, Machine: "M1",
			Coverage: model.CoverageFull, StartedAt: day(23, 9), UpdatedAt: day(23, 17), MessageCount: 3},
		{SessionID: 2, Agent: "codex", NativeID: "bbbb-2", Title: "Bot uploads", ProjectName: "Bot",
			ProjectPath: `D:\Projects\gone\Bot`, Cwd: `D:\Projects\gone\Bot`, Machine: "M1",
			Coverage: model.CoverageFull, StartedAt: day(22, 9), UpdatedAt: day(22, 10), MessageCount: 2},
		{SessionID: 3, Agent: "cursor", NativeID: "cccc-3", Title: "Scraper plan", ProjectName: "Scraper",
			ProjectPath: `D:\Projects\Scraper`, Cwd: `D:\Projects\Scraper`, Machine: "M1",
			Coverage: model.CoverageTitles, StartedAt: day(21, 9), UpdatedAt: day(21, 10), MessageCount: 0},
		{SessionID: 4, Agent: "claude", NativeID: "dddd-4", Title: "مشروع قديم", ProjectName: "Old",
			ProjectPath: `C:\Old`, Cwd: `C:\Old`, Machine: "OTHER", Missing: true,
			Coverage: model.CoverageFull, StartedAt: day(1, 9), UpdatedAt: day(1, 10), MessageCount: 1},
	}
	msgs := map[int64][]model.Message{
		1: {
			{Role: model.RoleUser, Kind: model.KindText, Text: "please download every video from the channel"},
			{Role: model.RoleAssistant, Kind: model.KindText, Text: "I will use the telegram API"},
			{Role: model.RoleTool, Kind: model.KindToolOutput, Text: "done"},
		},
		2: {
			{Role: model.RoleUser, Kind: model.KindText, Text: "telegram bot: upload the video"},
			{Role: model.RoleAssistant, Kind: model.KindText, Text: "ok"},
		},
		4: {{Role: model.RoleUser, Kind: model.KindText, Text: "أهلاً بالعالم"}},
	}
	return rows, msgs
}

// ---- tests ----

func TestTimelineAndPreview(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	v := h.view()
	for _, want := range []string{
		"4 sessions · 3 agents", "[All agents] [Any time]",
		"Today · 1", "Yesterday · 1", "Mon 21 Sep 2026 · 1",
		"Claude Code", "TelegramVideos", "Download telegram videos",
		`Claude Code · D:\Projects\TelegramVideos · started`, "last active 2026-09-23 17:00",
		"please download every video", "Scraper plan (titles)",
	} {
		if !strings.Contains(v, want) && !strings.Contains(strings.ReplaceAll(v, "\n", ""), want) {
			t.Errorf("view lacks %q\n%s", want, v)
		}
	}
	if h.c.syncs != 1 {
		t.Errorf("background sync at start: %d runs, want 1", h.c.syncs)
	}
	for _, line := range strings.Split(h.u.View().Content, "\n") {
		if w := cellWidth(line); w > 120 {
			t.Errorf("line wider than the terminal (%d): %q", w, ansi.Strip(line))
		}
	}
	if n := len(strings.Split(h.u.View().Content, "\n")); n != 30 {
		t.Errorf("view has %d lines, want 30", n)
	}
}

func TestSearchAndFilters(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.typeText("upload")
	// the debounce tick has fired inside run(); the query was searched
	if f := h.st.lastFilter(); f.Query != "upload" {
		t.Fatalf("last search = %+v, want query upload", f)
	}
	if len(h.u.rows) != 1 || h.u.rows[0].SessionID != 2 {
		t.Fatalf("rows = %+v", h.u.rows)
	}
	v := h.view()
	if !strings.Contains(v, `"…telegram bot: upload the video…"`) {
		t.Errorf("snippet row missing:\n%s", v)
	}
	// sandwich: the hit (message 0) and the next one
	if !strings.Contains(v, "telegram bot: upload the video") || !strings.Contains(v, "Codex:") {
		t.Errorf("preview sandwich missing:\n%s", v)
	}

	// Tab: agent filter (claude has the most sessions → first after All)
	h.key("tab")
	if f := h.u.applied; f.Agent != "claude" || f.Query != "upload" {
		t.Fatalf("agent filter = %+v, want claude", f)
	}
	if got := h.u.emptyMessage(); got != "No matches in Claude Code. 1 match with all agents — press Tab." {
		t.Errorf("filtered empty message = %q", got)
	}
	if !strings.Contains(h.view(), "No matches in Claude Code.") {
		t.Errorf("filtered empty message not shown:\n%s", h.view())
	}
	// Ctrl+T: time filter
	h.key("ctrl+t")
	if f := h.u.applied; f.Since.IsZero() {
		t.Error("Ctrl+T did not set Since")
	}
	if got := h.u.emptyMessage(); got != "No matches in Claude Code · Today. 1 match with all agents and any time — press Tab / Ctrl+T." {
		t.Errorf("two-filter empty message = %q", got)
	}
	// Esc clears the query; Esc again quits.
	h.key("esc")
	if h.u.input.Value() != "" || h.quit {
		t.Fatalf("first Esc: value %q quit %v", h.u.input.Value(), h.quit)
	}
	h.key("esc")
	if !h.quit {
		t.Error("Esc with an empty query must quit")
	}
}

func TestNoMatches(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.typeText("zzzz")
	if !strings.Contains(h.view(), "No matches. Try fewer or shorter words.") {
		t.Errorf("no-match message missing:\n%s", h.view())
	}
}

func TestEmptyArchive(t *testing.T) {
	h := newHarness(t, nil, nil)
	if !strings.Contains(h.view(), "No agent history found. F2 shows where Athar looked.") {
		t.Errorf("empty archive message missing:\n%s", h.view())
	}
	h.u.app.FirstRun, h.u.syncing = true, true
	if !strings.Contains(strings.ReplaceAll(h.view(), "\n", " "), "First run: reading your agents") {
		t.Errorf("first-run message missing:\n%s", h.view())
	}
}

func TestResumeFromMenu(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.key("enter")
	if h.u.menu == nil {
		t.Fatal("Enter did not open the menu")
	}
	if got := menuKeys(h.u.menu.entries); got != "R!OCD" {
		t.Fatalf("menu keys = %q", got)
	}
	syncsBefore := h.c.syncs
	h.key("ق") // Arabic-layout R
	if h.u.menu != nil {
		t.Fatalf("menu still open: %+v", h.u.menu)
	}
	if len(h.c.started) != 1 || h.c.built[len(h.c.built)-1].NativeID != "aaaa-1" {
		t.Fatalf("started = %+v built = %+v", h.c.started, h.c.built)
	}
	if h.c.actions[len(h.c.actions)-1] != launch.ActionResume || h.c.skip[len(h.c.skip)-1] {
		t.Errorf("wrong action/skip: %v %v", h.c.actions, h.c.skip)
	}
	if !strings.Contains(h.view(), "Opened in new tab") {
		t.Errorf("status missing:\n%s", h.view())
	}
	if h.c.syncs != syncsBefore+1 {
		t.Errorf("no sync after the action (syncs %d → %d)", syncsBefore, h.c.syncs)
	}
}

// "!" launches at once, without a y/N question (owner's choice, v1.2).
func TestSkipPermissionsNoConfirm(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.key("enter")
	h.key("!")
	if strings.Contains(h.view(), "y/N") {
		t.Fatalf("still asks for confirmation:\n%s", h.view())
	}
	if h.u.menu != nil || len(h.c.started) != 1 || !h.c.skip[len(h.c.skip)-1] {
		t.Fatalf("skip launch: started %d skip %v", len(h.c.started), h.c.skip)
	}
}

func TestPreflightErrorOffersRead(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.preErr = errors.New(`Folder no longer exists: D:\Projects\TelegramVideos`)
	h.key("enter")
	h.key("r")
	if h.u.menu == nil || !strings.Contains(h.u.menu.err, "Folder no longer exists") || !strings.Contains(h.u.menu.err, "press O") {
		t.Fatalf("menu error = %+v", h.u.menu)
	}
	if h.u.menu.entries[h.u.menu.sel].id != itemRead {
		t.Error("O is not highlighted after a failed pre-flight")
	}
	if !strings.Contains(strings.ReplaceAll(h.view(), "\n", " "), "Folder no longer exists") {
		t.Errorf("error not shown:\n%s", h.view())
	}
}

func TestReadCopyFolderGUI(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.typeText("telegram")
	// row 1 (claude) matches message 1 ("telegram API"), row 2 matches message 0
	h.st.hits[1] = []int{1}
	h.key("enter")
	h.key("خ") // Arabic-layout O
	if len(h.c.pages) != 1 || len(h.c.opened) != 1 {
		t.Fatalf("pages %d opened %d", len(h.c.pages), len(h.c.opened))
	}
	p := h.c.pages[0]
	if p.AgentName != "Claude Code" || p.ScrollTo != 1 || fmt.Sprint(p.Hits) != "[1]" || len(p.Messages) != 3 {
		t.Errorf("page = %+v", p)
	}

	h.key("enter")
	h.key("c")
	if len(h.c.copied) != 1 || h.c.copied[0] != "prog aaaa-1" {
		t.Errorf("copied = %q", h.c.copied)
	}
	h.key("enter")
	h.key("d")
	if len(h.c.folders) != 1 || h.c.folders[0] != `D:\Projects\TelegramVideos` {
		t.Errorf("folders = %q", h.c.folders)
	}

	// GUI agent: R opens the app with the §2.3 message.
	h.key("esc") // clear query
	h.key("end")
	h.key("up") // cursor on the cursor-agent row (third of four)
	if r := h.u.selected(); r == nil || r.Agent != "cursor" {
		t.Fatalf("selected = %+v", r)
	}
	h.key("enter")
	if got := menuKeys(h.u.menu.entries); got != "ROCD" {
		t.Fatalf("GUI menu keys = %q", got)
	}
	h.key("r")
	if !strings.Contains(h.view(), "Opened Scraper in Cursor. Pick «Scraper plan» from its chat history.") {
		t.Errorf("GUI status missing:\n%s", h.view())
	}
}

func TestOtherMachineIsReadOnly(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.key("end")
	if r := h.u.selected(); r == nil || r.SessionID != 4 {
		t.Fatalf("selected = %+v", r)
	}
	v := h.view()
	if !strings.Contains(v, "From another PC — read only") || !strings.Contains(v, "Archive only") {
		t.Errorf("read-only notes missing:\n%s", v)
	}
	h.key("enter")
	if got := menuKeys(h.u.menu.entries); got != "OC" {
		t.Errorf("menu for another machine = %q, want OC", got)
	}
}

func TestHeaderJumps(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.key("ctrl+down")
	if h.u.cursor != 1 {
		t.Fatalf("ctrl+down → cursor %d, want 1", h.u.cursor)
	}
	h.key("ctrl+down")
	h.key("ctrl+down")
	if h.u.cursor != 3 {
		t.Fatalf("third ctrl+down → cursor %d, want 3", h.u.cursor)
	}
	h.key("ctrl+up")
	if h.u.cursor != 2 {
		t.Fatalf("ctrl+up → cursor %d, want 2", h.u.cursor)
	}
	h.key("home")
	if h.u.cursor != 0 {
		t.Fatalf("home → cursor %d", h.u.cursor)
	}
}

func TestSyncProgressLockedAndStale(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.u.syncing, h.u.progress = true, syncer.Progress{Done: 5, Total: 27}
	if !strings.Contains(h.view(), "Syncing 5/27…") {
		t.Errorf("progress missing:\n%s", h.view())
	}
	h.u.syncing = false
	h.syncErr = syncer.ErrLocked
	h.run(h.u.startSync())
	if !strings.Contains(h.view(), "Another Athar window is syncing — results will refresh.") {
		t.Errorf("locked note missing:\n%s", h.view())
	}

	h.st.meta[metaLastSync] = testNow.Add(-23 * 24 * time.Hour).Format(time.RFC3339)
	h.u.loadStartupMeta()
	if !strings.Contains(h.view(), "Last sync 23 days ago — Claude Code and Gemini delete") {
		t.Errorf("stale warning missing:\n%s", h.view())
	}
	h.syncErr = nil
	h.run(h.u.startSync())
	if strings.Contains(h.view(), "Last sync 23 days ago") {
		t.Error("stale warning must disappear after a sync")
	}
}

func TestSourcesScreenAndUpdate(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.st.sources = []archive.SourceStatus{
		{Adapter: "claude", Path: `C:\Users\user\.claude\projects\p\a.jsonl`, Sessions: 3, LastOK: testNow.Add(-2 * time.Minute)},
		{Adapter: "codex", Path: `C:\Users\user\.codex\sessions\r.jsonl`, Sessions: 1, LastOK: testNow.Add(-time.Hour), Skipped: 4,
			Warnings: []string{"source shrank, archived copy kept"}},
		{Adapter: "cursor", Path: `C:\Users\user\AppData\Roaming\Cursor\state.vscdb`, LastError: "torn copy"},
	}
	h.u.release = &update.Release{Version: "v1.1.0", Notes: "Faster search.", URL: "https://example.invalid/r"}
	h.key("f2")
	v := h.view()
	for _, want := range []string{
		"Sources & help", "Claude Code", "2 sessions", "resume ✓", "last read 2 min ago",
		"resume via app", "1 error", "4 records skipped", "source shrank, archived copy kept", "error: torn copy",
		`Archive: D:\Tools\Athar\athar-data\archive.db (3.0 MB)`,
		"Update v1.1.0 available — press U for what's new", `"exact phrase"`,
	} {
		if !strings.Contains(v, want) {
			t.Errorf("sources screen lacks %q\n%s", want, v)
		}
	}
	h.key("ع") // Arabic-layout U
	if h.u.screen != screenNotes || !strings.Contains(h.view(), "Faster search.") {
		t.Fatalf("notes screen:\n%s", h.view())
	}
	h.key("i")
	if h.c.installed != 1 || !errors.Is(h.u.quitErr, ErrRestart) || !h.quit {
		t.Errorf("install: calls %d quitErr %v quit %v", h.c.installed, h.u.quitErr, h.quit)
	}
}

func TestUpdateCheckOncePerDay(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	if h.st.meta[metaUpdateCheck] == "" {
		t.Fatal("update check time not stored")
	}
	// A newer release found earlier today is shown without a new check.
	h.st.meta[metaUpdateRelease] = `{"Version":"v1.2.0","Notes":"n","URL":"u"}`
	h.u.release = nil
	h.u.loadStartupMeta()
	if h.u.updateDue || h.u.release == nil || h.u.release.Version != "v1.2.0" {
		t.Fatalf("due=%v release=%+v", h.u.updateDue, h.u.release)
	}
	if !strings.Contains(h.view(), "Update v1.2.0 available — see F2") {
		t.Errorf("footer update notice missing:\n%s", h.view())
	}
	// Disabled by config or flag.
	off := false
	h.u.app.Config = config.Config{}
	h.u.app.Config.Update.Check = &off
	h.u.release = nil
	h.u.loadStartupMeta()
	if h.u.updateDue || h.u.release != nil {
		t.Error("update check must be off with [update] check = false")
	}
}

func TestSmallTerminalsDoNotPanic(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	for _, size := range [][2]int{{20, 5}, {40, 8}, {69, 20}, {70, 20}, {200, 60}, {1, 1}} {
		h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v := h.u.View().Content
		for _, l := range strings.Split(v, "\n") {
			if cellWidth(l) > size[0] && size[0] >= 20 {
				t.Errorf("%dx%d: line too wide (%d): %q", size[0], size[1], cellWidth(l), ansi.Strip(l))
			}
		}
		h.key("enter")
		_ = h.u.View()
		h.key("esc")
	}
}

func manyRows(n int) []archive.Row {
	rows := make([]archive.Row, n)
	for i := range rows {
		rows[i] = archive.Row{
			SessionID: int64(i + 1), Agent: "codex", NativeID: fmt.Sprintf("id-%d", i),
			Title: fmt.Sprintf("session %d", i), ProjectName: "p", Machine: "M1", Coverage: model.CoverageFull,
			UpdatedAt: testNow.Add(-time.Duration(i) * time.Hour), StartedAt: testNow.Add(-time.Duration(i) * time.Hour),
		}
	}
	return rows
}

func TestPagingAndEnd(t *testing.T) {
	h := newHarness(t, manyRows(450), nil)
	if len(h.u.rows) != pageSize || !h.u.more {
		t.Fatalf("first page: %d rows, more=%v", len(h.u.rows), h.u.more)
	}
	// moving close to the end of the loaded rows fetches the next page
	for i := 0; i < pageSize-prefetchRows; i++ {
		h.key("down")
	}
	if len(h.u.rows) != 2*pageSize {
		t.Fatalf("after scrolling: %d rows loaded, want %d", len(h.u.rows), 2*pageSize)
	}
	h.key("end")
	if len(h.u.rows) != 450 || h.u.more || h.u.cursor != 449 {
		t.Fatalf("End: rows %d more %v cursor %d", len(h.u.rows), h.u.more, h.u.cursor)
	}
	if !strings.Contains(h.view(), "session 449") {
		t.Errorf("last row not visible after End:\n%s", h.view())
	}
	h.key("home")
	if h.u.cursor != 0 || h.u.top != 0 || !strings.Contains(h.view(), "session 0") {
		t.Errorf("Home: cursor %d top %d", h.u.cursor, h.u.top)
	}
}

func TestRefreshKeepsSelection(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.key("down")
	h.key("down")
	sel := h.u.selected().SessionID
	// a new session arrives at the top during a sync
	h.st.mu.Lock()
	h.st.rows = append(h.st.rows, archive.Row{SessionID: 99, Agent: "codex", NativeID: "new", Title: "brand new",
		Machine: "M1", Coverage: model.CoverageFull, UpdatedAt: testNow, StartedAt: testNow})
	h.st.mu.Unlock()
	h.run(h.u.startSync())
	if got := h.u.selected().SessionID; got != sel {
		t.Errorf("selection moved from session %d to %d after a refresh", sel, got)
	}
	if !strings.Contains(h.view(), "brand new") {
		t.Errorf("new session not shown after the sync:\n%s", h.view())
	}
}

func TestPartialMatchesHeader(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.u.rows = []archive.Row{rows[0], rows[1]}
	h.u.rows[0].MatchedTerms, h.u.rows[0].TotalTerms = 2, 2
	h.u.rows[1].MatchedTerms, h.u.rows[1].TotalTerms = 1, 2
	h.u.applied.Query = "telegram video"
	h.u.rebuild()
	if !strings.Contains(h.view(), "Partial matches (1 of 2 words)") {
		t.Errorf("partial header missing:\n%s", h.view())
	}
}

func TestPreviewKeptWhileReloadingAfterSync(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	if !strings.Contains(h.view(), "please download every video") {
		t.Fatalf("preview not loaded:\n%s", h.view())
	}
	// The sync appended a message to the selected session.
	h.st.mu.Lock()
	h.st.msgs[1] = append(append([]model.Message(nil), h.st.msgs[1]...),
		model.Message{Role: model.RoleUser, Kind: model.KindText, Text: "one more thing"})
	h.st.mu.Unlock()
	cmd := h.u.onSyncDone(syncDoneMsg{rep: syncer.Report{Read: 1}})
	// Until the reload arrives the previous messages stay on screen.
	if v := h.view(); strings.Contains(v, "Loading…") || !strings.Contains(v, "please download every video") {
		t.Errorf("preview blanked during the reload:\n%s", v)
	}
	h.run(cmd)
	if !strings.Contains(h.view(), "one more thing") {
		t.Errorf("preview not reloaded after the sync:\n%s", h.view())
	}
	// The reading page uses the fresh messages too.
	h.key("enter")
	h.key("o")
	if n := len(h.c.pages); n != 1 || len(h.c.pages[0].Messages) != 4 {
		t.Errorf("reading page messages: %+v", h.c.pages)
	}
}

func TestWaitBackground(t *testing.T) {
	var bg bgGroup
	var out strings.Builder
	waitBackground(&bg, &out, time.Second) // nothing running: returns at once
	if out.Len() != 0 {
		t.Errorf("idle wait printed %q", out.String())
	}
	bg.Add(1)
	go func() { time.Sleep(100 * time.Millisecond); bg.Done() }()
	waitBackground(&bg, &out, 10*time.Millisecond)
	if !strings.Contains(out.String(), "finishing the current sync step") {
		t.Errorf("slow wait printed %q", out.String())
	}
}

func TestWithDefaults(t *testing.T) {
	got := withDefaults(App{Env: adapters.Env{Home: `C:\Users\u`}}, `D:\Tools\Athar\athar-data\archive.db`)
	if got.DataDir != `D:\Tools\Athar\athar-data` {
		t.Errorf("DataDir = %q", got.DataDir)
	}
	if got.Env.Home != `C:\Users\u` || got.Env.TempDir == "" || got.Env.Getenv == nil {
		t.Errorf("Env = %+v", got.Env)
	}
	if got.Adapters == nil || got.Machine == "" && syncer.MachineID() != "" {
		t.Errorf("adapters %v machine %q", got.Adapters, got.Machine)
	}
	// Values given by the caller are kept.
	keep := App{Adapters: []adapters.Adapter{}, Machine: "M", Hostname: "H", DataDir: `X:\d`,
		Env: adapters.Env{Home: "h", TempDir: "t", Getenv: func(string) string { return "" }}}
	got = withDefaults(keep, `D:\a\archive.db`)
	if got.Machine != "M" || got.Hostname != "H" || got.DataDir != `X:\d` || got.Env.TempDir != "t" || len(got.Adapters) != 0 {
		t.Errorf("caller values replaced: %+v", got)
	}
}

func TestArabicInUITextIsShaped(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	// status line: the GUI message embeds a conversation title
	h.u.setStatus("Opened Old in Cursor. Pick «مشروع قديم» from its chat history.", false)
	v := h.view()
	if !strings.Contains(v, displayText("Pick «مشروع قديم» from", arabicShaped)) || strings.Contains(v, "«مشروع قديم»") {
		t.Errorf("status line not shaped:\n%s", v)
	}
	// menu error: pre-flight messages embed project paths
	h.preErr = errors.New(`Folder no longer exists: D:\مشاريع\قديم`)
	h.key("enter")
	h.key("r")
	v = h.view()
	if !strings.Contains(v, displayText(`D:\مشاريع\قديم`, arabicShaped)) || strings.Contains(v, `D:\مشاريع\قديم`) {
		t.Errorf("menu error not shaped:\n%s", v)
	}
	h.key("esc")
	// sources screen: help text and source paths
	h.st.sources = []archive.SourceStatus{{Adapter: "codex", Path: `C:\مجلد\r.jsonl`, LastError: "bad"}}
	h.key("f2")
	v = h.view()
	if !strings.Contains(v, displayText("without ال, hamza", arabicShaped)) {
		t.Errorf("help text not shaped:\n%s", v)
	}
	if !strings.Contains(v, displayText(`C:\مجلد\r.jsonl`, arabicShaped)) {
		t.Errorf("source path not shaped:\n%s", v)
	}
	// raw mode leaves everything as typed
	h.u.mode = arabicRaw
	if v := h.view(); !strings.Contains(v, "without ال, hamza") {
		t.Errorf("raw mode changed the help text:\n%s", v)
	}
}

func TestStatusExpires(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	cmd := h.u.onAction(actionMsg{status: "Copied: prog aaaa-1"})
	if cmd == nil {
		t.Fatal("no redraw scheduled for the status message")
	}
	if !strings.Contains(h.view(), "Copied: prog aaaa-1") {
		t.Fatalf("status missing:\n%s", h.view())
	}
	h.now = h.now.Add(statusFor + time.Second)
	h.send(statusExpiredMsg{})
	if strings.Contains(h.view(), "Copied: prog aaaa-1") {
		t.Errorf("status still shown after %v:\n%s", statusFor, h.view())
	}
}

func TestSourcesScreenWrapsLongLines(t *testing.T) {
	long := `D:\` + strings.Repeat(`very long folder name\`, 8) + "archive.db"
	v := sourcesView{
		now: testNow, mode: arabicShaped,
		data: sourcesData{path: long, lastSync: testNow.Add(-time.Hour),
			sources: []archive.SourceStatus{{Adapter: "codex", Path: long, LastError: strings.Repeat("broken record ", 20)}}},
	}
	lines := sourcesLines(v, 80)
	text := strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, "\n"))), " ") // wrapped lines
	for _, want := range []string{"last sync 2026-09-23 17:00", "archive.db", "Problems", "error: broken record"} {
		if !strings.Contains(text, want) {
			t.Errorf("sources screen lacks %q:\n%s", want, text)
		}
	}
	for _, l := range lines {
		if w := cellWidth(l); w > 80 {
			t.Errorf("line wider than 80 (%d): %q", w, ansi.Strip(l))
		}
	}
}

func TestShiftEnterResumesWithoutPrompts(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.key("shift+enter")
	if len(h.c.started) != 1 || h.c.built[len(h.c.built)-1].NativeID != "aaaa-1" {
		t.Fatalf("started = %+v built = %+v", h.c.started, h.c.built)
	}
	if h.c.actions[len(h.c.actions)-1] != launch.ActionResume || !h.c.skip[len(h.c.skip)-1] {
		t.Errorf("want resume with skip, got %v %v", h.c.actions, h.c.skip)
	}
	if h.u.menu != nil {
		t.Errorf("menu left open: %+v", h.u.menu)
	}
	if !strings.Contains(h.view(), "Opened in new tab") {
		t.Errorf("status missing:\n%s", h.view())
	}
}

func TestShiftEnterExplainsWhenUnavailable(t *testing.T) {
	rows, msgs := fixture()
	h := newHarness(t, rows, msgs)
	h.key("down") // codex: resumable, but this harness gives it no skip-permissions flag
	h.key("shift+enter")
	if len(h.c.started) != 0 {
		t.Fatalf("must not launch: %+v", h.c.started)
	}
	if h.u.menu == nil || !strings.Contains(h.u.menu.err, "no mode without permission prompts") {
		t.Fatalf("menu error = %+v", h.u.menu)
	}
}

func TestShiftEnterKeyName(t *testing.T) {
	k := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	if k.String() != "shift+enter" {
		t.Fatalf("Shift+Enter is reported as %q", k.String())
	}
}
