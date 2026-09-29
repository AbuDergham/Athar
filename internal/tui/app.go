package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/launch"
	"github.com/AbuDergham/Athar/internal/model"
	"github.com/AbuDergham/Athar/internal/syncer"
	"github.com/AbuDergham/Athar/internal/update"
)

const (
	pageSize     = 200 // rows fetched per Search call
	prefetchRows = 50  // fetch the next page when the cursor is this close to the end
	debounce     = 120 * time.Millisecond
	syncEvery    = 60 * time.Second // background sync while Athar is open (§4.3)
	refreshEvery = 2 * time.Second  // refresh results at most this often during a sync
	staleAfter   = 20 * 24 * time.Hour
	updateEvery  = 24 * time.Hour
	statusFor    = 10 * time.Second // transient status messages
	cacheSize    = 16               // sessions whose messages are kept for the preview
)

// Meta keys read or written by the UI (the archive's meta table).
const (
	metaLastSync      = "last_sync_at"         // written by the syncer (RFC 3339)
	metaUpdateCheck   = "last_update_check_at" // RFC 3339
	metaUpdateRelease = "update_release"       // JSON of the newer update.Release, "" = none
)

type screen int

const (
	screenMain screen = iota
	screenSources
	screenNotes
)

// ui is the Bubble Tea model. It is used through a pointer: Update mutates it.
type ui struct {
	app   App
	st    store
	svc   services
	ctx   context.Context
	bg    *bgGroup
	mode  string // Arabic display mode
	names map[string]string
	caps  map[string]adapters.Caps
	roots []string // Claude roots for live-session detection

	width, height int
	screen        screen
	input         searchBox
	inputW        int // cells available to the search box

	// filters
	agents      []archive.AgentCount
	agentFilter string // agent id, "" = all
	timeRange   timeRange

	// results
	gen        int // bumped on every query/filter change (debounce)
	req        int // bumped on every first-page load; later pages must match it
	applied    archive.Filter
	rows       []archive.Row
	entries    []entry
	rowEntry   []int // row index → entry index
	entryLine  []int // entry index → first list line
	more       bool  // further pages may exist
	loaded     bool  // the first results arrived
	fetching   bool  // a further page is being fetched
	wantEnd    bool  // End pressed while pages were missing
	cursor     int
	top        int // first visible list line
	unfiltered int // matches without filters when the filtered list is empty; -1 unknown
	searchErr  error
	live       map[string]launch.LiveInfo

	// preview
	cache      map[int64][]model.Message
	cacheErr   map[int64]error
	cacheStale map[int64]bool // shown until reloaded (a sync may have changed them)
	cacheOrder []int64

	menu *menuState

	// sync
	syncing     bool
	progress    syncer.Progress
	lastRefresh time.Time
	lockedNote  bool
	staleNote   string
	syncErr     string
	synced      bool // a sync finished in this session

	// update
	updateDue  bool
	release    *update.Release
	installing bool

	status    string
	statusErr bool
	statusAt  time.Time

	src      *sourcesData
	srcErr   error
	srcTop   int
	notesTop int

	quitErr error
}

func newUI(ctx context.Context, app App, st store, svc services) *ui {
	u := &ui{
		app: app, st: st, svc: svc, ctx: ctx, bg: &bgGroup{},
		mode:       app.Config.Display.Arabic,
		names:      map[string]string{},
		caps:       map[string]adapters.Caps{},
		cache:      map[int64][]model.Message{},
		cacheErr:   map[int64]error{},
		cacheStale: map[int64]bool{},
		unfiltered: -1,
	}
	if u.mode == "" {
		u.mode = defaultArabic
	}
	for _, a := range app.Adapters {
		u.names[a.ID()] = a.Name()
		u.caps[a.ID()] = a.Caps()
	}
	u.roots = claudeRoots(app.Env)
	return u
}

// claudeRoots lists Claude Code data roots for live-session detection (§6.5):
// %USERPROFILE%\.claude, CLAUDE_CONFIG_DIR and every %USERPROFILE%\.claude* folder.
// Only directory names are listed; nothing is opened here.
func claudeRoots(env adapters.Env) []string {
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		if p == "" {
			return
		}
		k := strings.ToLower(filepath.Clean(p))
		if !seen[k] {
			seen[k] = true
			roots = append(roots, p)
		}
	}
	if env.Home != "" {
		add(filepath.Join(env.Home, ".claude"))
		if des, err := os.ReadDir(env.Home); err == nil {
			for _, de := range des {
				if de.IsDir() && strings.HasPrefix(strings.ToLower(de.Name()), ".claude") {
					add(filepath.Join(env.Home, de.Name()))
				}
			}
		}
	}
	if env.Getenv != nil {
		add(env.Getenv("CLAUDE_CONFIG_DIR"))
	}
	return roots
}

// loadStartupMeta reads the stale-sync and update state before the UI starts.
func (u *ui) loadStartupMeta() {
	now := u.svc.now()
	if v, err := u.st.Meta(metaLastSync); err == nil && v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil && now.Sub(t) > staleAfter {
			days := int(now.Sub(t) / (24 * time.Hour))
			u.staleNote = "Last sync " + itoa(days) + " days ago — Claude Code and Gemini delete sessions after 30 days"
		}
	}
	if !u.updatesEnabled() {
		return
	}
	u.updateDue = true
	if v, err := u.st.Meta(metaUpdateCheck); err == nil && v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil && now.Sub(t) < updateEvery && !t.After(now) {
			u.updateDue = false
		}
	}
	if !u.updateDue {
		if v, err := u.st.Meta(metaUpdateRelease); err == nil && v != "" {
			var r update.Release
			if json.Unmarshal([]byte(v), &r) == nil && update.Newer(r.Version, u.app.Version) {
				u.release = &r
			}
		}
	}
}

func (u *ui) updatesEnabled() bool {
	v := u.app.Version
	return !u.app.NoUpdate && u.app.Config.UpdateCheckEnabled() && v != "" && v != "dev" && u.svc.checkUpdate != nil
}

// ---- messages ----

type debounceMsg struct{ gen int }

type resultsMsg struct {
	req        int
	filter     archive.Filter
	offset     int
	limit      int
	rows       []archive.Row
	err        error
	keepID     int64
	live       map[string]launch.LiveInfo
	agents     []archive.AgentCount
	unfiltered int
}

type previewMsg struct {
	id   int64
	msgs []model.Message
	err  error
}

// syncProgressMsg reports one finished source; ch delivers the next message.
type syncProgressMsg struct {
	p  syncer.Progress
	ch chan tea.Msg
}

type syncDoneMsg struct {
	rep syncer.Report
	err error
}

type syncTickMsg struct{}

type updateMsg struct {
	rel *update.Release
	err error
}

// actionMsg reports the end of a menu action.
type actionMsg struct {
	status    string
	err       error
	offerRead bool // the error comes from the pre-flight check: offer O instead
	synced    bool // start a sync (the main screen regains control)
}

type sourcesMsg struct {
	data *sourcesData
	err  error
}

type installMsg struct{ err error }

// statusExpiredMsg redraws the screen when a status message times out.
type statusExpiredMsg struct{}

// ---- Bubble Tea ----

func (u *ui) Init() tea.Cmd {
	cmds := []tea.Cmd{u.load(false), u.startSync(), tickSync()}
	if u.updateDue {
		cmds = append(cmds, u.updateCheckCmd())
	}
	return tea.Batch(cmds...)
}

func tickSync() tea.Cmd {
	return tea.Tick(syncEvery, func(time.Time) tea.Msg { return syncTickMsg{} })
}

func (u *ui) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		u.width, u.height = msg.Width, msg.Height
		u.layoutInput()
		u.ensureVisible()
		return u, nil

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return u, tea.Quit
		}
		switch {
		case u.screen == screenSources:
			return u, u.keySources(msg)
		case u.screen == screenNotes:
			return u, u.keyNotes(msg)
		case u.menu != nil:
			return u, u.keyMenu(msg)
		default:
			return u, u.keyMain(msg)
		}

	case tea.PasteMsg:
		if u.screen == screenMain && u.menu == nil {
			before := u.input.Value()
			u.input.paste(msg.Content)
			return u, u.inputChanged(before)
		}
		return u, nil

	case debounceMsg:
		if msg.gen == u.gen {
			return u, u.load(false)
		}
		return u, nil

	case resultsMsg:
		return u, u.onResults(msg)

	case previewMsg:
		u.remember(msg.id, msg.msgs, msg.err)
		return u, nil

	case syncProgressMsg:
		u.progress = msg.p
		cmds := []tea.Cmd{waitSync(msg.ch)}
		if u.svc.now().Sub(u.lastRefresh) >= refreshEvery {
			cmds = append(cmds, u.refresh())
		}
		return u, tea.Batch(cmds...)

	case syncDoneMsg:
		return u, u.onSyncDone(msg)

	case syncTickMsg:
		cmds := []tea.Cmd{tickSync(), u.startSync()}
		if u.lockedNote {
			cmds = append(cmds, u.refresh())
		}
		return u, tea.Batch(cmds...)

	case updateMsg:
		if msg.err == nil && msg.rel != nil {
			u.release = msg.rel
		}
		return u, nil

	case actionMsg:
		return u, u.onAction(msg)

	case sourcesMsg:
		u.src, u.srcErr = msg.data, msg.err
		return u, nil

	case statusExpiredMsg:
		return u, nil // View drops statuses older than statusFor

	case installMsg:
		u.installing = false
		if msg.err != nil {
			return u, u.setStatus("Update failed: "+msg.err.Error(), true)
		}
		u.quitErr = ErrRestart
		return u, tea.Quit
	}
	return u, nil
}

// setStatus shows a transient message in the status line; the returned
// command redraws the screen once it has expired.
func (u *ui) setStatus(s string, isErr bool) tea.Cmd {
	u.status, u.statusErr, u.statusAt = s, isErr, u.svc.now()
	return tea.Tick(statusFor+100*time.Millisecond, func(time.Time) tea.Msg { return statusExpiredMsg{} })
}

// ---- main screen keys ----

func (u *ui) keyMain(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up":
		return u.move(-1)
	case "down":
		return u.move(1)
	case "pgup":
		return u.move(-u.pageRows())
	case "pgdown":
		return u.move(u.pageRows())
	case "home":
		u.wantEnd = false
		u.cursor, u.top = 0, 0
		return u.previewCmd()
	case "end":
		return u.toEnd()
	case "ctrl+up":
		return u.jumpHeader(-1)
	case "ctrl+down":
		return u.jumpHeader(1)
	case "enter":
		u.openMenu()
		return nil
	case "shift+enter":
		return u.quickSkip()
	case "tab":
		u.cycleAgent(1)
		return u.load(false)
	case "shift+tab":
		u.cycleAgent(-1)
		return u.load(false)
	case "ctrl+t":
		u.timeRange = u.timeRange.next()
		return u.load(false)
	case "f2":
		u.screen, u.srcTop = screenSources, 0
		return u.sourcesCmd()
	case "esc":
		if u.input.Value() != "" {
			u.input.SetValue("")
			return u.load(false)
		}
		return tea.Quit
	}
	return u.editInput(msg)
}

// editInput passes a key to the search box.
func (u *ui) editInput(msg tea.KeyPressMsg) tea.Cmd {
	before := u.input.Value()
	if !u.input.key(msg) {
		return nil
	}
	return u.inputChanged(before)
}

// inputChanged schedules a search (debounced) when the query text changed.
func (u *ui) inputChanged(before string) tea.Cmd {
	if u.input.Value() == before {
		return nil
	}
	u.gen++
	gen := u.gen
	return tea.Tick(debounce, func(time.Time) tea.Msg { return debounceMsg{gen} })
}

func (u *ui) cycleAgent(dir int) {
	ids := []string{""}
	for _, a := range u.agents {
		ids = append(ids, a.Agent)
	}
	i := 0
	for k, id := range ids {
		if id == u.agentFilter {
			i = k
		}
	}
	u.agentFilter = ids[(i+dir+len(ids))%len(ids)]
}

func (u *ui) filter() archive.Filter {
	return archive.Filter{
		Query: strings.TrimSpace(u.input.Value()),
		Agent: u.agentFilter,
		Since: u.timeRange.since(u.svc.now()),
	}
}

// load fetches the first page for the current query and filters. keep keeps
// the selected session (a refresh); otherwise the cursor goes to the top.
func (u *ui) load(keep bool) tea.Cmd {
	u.req++
	u.gen++
	u.fetching = false
	u.lastRefresh = u.svc.now()
	limit := pageSize
	var keepID int64
	if keep {
		if r := u.selected(); r != nil {
			keepID = r.SessionID
		}
		limit = max(pageSize, len(u.rows))
	}
	return u.fetchCmd(u.req, u.filter(), 0, limit, keepID)
}

// refresh reloads the current results keeping the selection.
func (u *ui) refresh() tea.Cmd { return u.load(true) }

func (u *ui) fetchCmd(req int, f archive.Filter, offset, limit int, keepID int64) tea.Cmd {
	st, live, roots := u.st, u.svc.live, u.roots
	return func() tea.Msg {
		rows, err := st.Search(f, limit, offset)
		m := resultsMsg{req: req, filter: f, offset: offset, limit: limit, rows: rows, err: err, keepID: keepID, unfiltered: -1}
		if offset > 0 {
			return m
		}
		if live != nil {
			m.live = live(roots)
		}
		m.agents, _ = st.Agents()
		if err == nil && len(rows) == 0 && (f.Agent != "" || !f.Since.IsZero()) {
			if all, err := st.Search(archive.Filter{Query: f.Query}, 1000, 0); err == nil {
				m.unfiltered = len(all)
			}
		}
		return m
	}
}

func (u *ui) onResults(m resultsMsg) tea.Cmd {
	if m.req != u.req {
		return nil
	}
	u.fetching = false
	if m.offset == 0 {
		u.loaded = true
		u.searchErr = m.err
		u.applied = m.filter
		u.rows = m.rows
		u.unfiltered = m.unfiltered
		if m.live != nil {
			u.live = m.live
		}
		if m.agents != nil {
			u.agents = m.agents
		}
		u.cursor = 0
		if m.keepID != 0 {
			for i, r := range u.rows {
				if r.SessionID == m.keepID {
					u.cursor = i
					break
				}
			}
		} else {
			u.top = 0
		}
	} else if m.err == nil {
		u.rows = append(u.rows, m.rows...)
	}
	u.more = m.err == nil && len(m.rows) == m.limit
	u.rebuild()
	if u.wantEnd {
		if u.more {
			return u.fetchMore()
		}
		u.wantEnd = false
		u.cursor = max(0, len(u.rows)-1)
	}
	u.ensureVisible()
	return u.previewCmd()
}

// rebuild recomputes list entries and line offsets after rows changed.
func (u *ui) rebuild() {
	u.entries = buildEntries(u.rows, u.applied.Query == "", u.svc.now(), u.more)
	u.rowEntry = make([]int, len(u.rows))
	u.entryLine = make([]int, len(u.entries)+1)
	line := 0
	for i, e := range u.entries {
		u.entryLine[i] = line
		line += e.height()
		if e.row >= 0 {
			u.rowEntry[e.row] = i
		}
	}
	u.entryLine[len(u.entries)] = line
	if u.cursor >= len(u.rows) {
		u.cursor = max(0, len(u.rows)-1)
	}
}

func (u *ui) fetchMore() tea.Cmd {
	if u.fetching || !u.more {
		return nil
	}
	u.fetching = true
	return u.fetchCmd(u.req, u.applied, len(u.rows), pageSize, 0)
}

func (u *ui) selected() *archive.Row {
	if u.cursor < 0 || u.cursor >= len(u.rows) {
		return nil
	}
	return &u.rows[u.cursor]
}

// listHeight is the number of lines of the result list.
func (u *ui) listHeight() int { return max(1, u.height-6) }

func (u *ui) pageRows() int { return max(1, u.listHeight()/2-1) }

func (u *ui) move(delta int) tea.Cmd {
	if len(u.rows) == 0 {
		return nil
	}
	u.wantEnd = false
	u.cursor = min(max(u.cursor+delta, 0), len(u.rows)-1)
	u.ensureVisible()
	var cmds []tea.Cmd
	if len(u.rows)-u.cursor <= prefetchRows {
		cmds = append(cmds, u.fetchMore())
	}
	cmds = append(cmds, u.previewCmd())
	return tea.Batch(cmds...)
}

func (u *ui) toEnd() tea.Cmd {
	if u.more {
		u.wantEnd = true
		return u.fetchMore()
	}
	return u.move(len(u.rows))
}

// jumpHeader moves to the first row after the previous (dir<0) or next
// (dir>0) header (day headers, partial-match separators).
func (u *ui) jumpHeader(dir int) tea.Cmd {
	if len(u.rows) == 0 {
		return nil
	}
	e := u.rowEntry[u.cursor]
	target := -1
	if dir > 0 {
		for i := e + 1; i < len(u.entries); i++ {
			if u.entries[i].row < 0 && i+1 < len(u.entries) {
				target = u.entries[i+1].row
				break
			}
		}
		if target < 0 {
			if u.more {
				return u.fetchMore()
			}
			return nil
		}
	} else {
		// the header of the current group; if we are its first row, the one before
		h := -1
		for i := e - 1; i >= 0; i-- {
			if u.entries[i].row < 0 {
				h = i
				break
			}
		}
		if h == e-1 {
			for i := h - 1; i >= 0; i-- {
				if u.entries[i].row < 0 {
					target = u.entries[i+1].row
					break
				}
			}
			if target < 0 {
				target = 0
			}
		} else if h >= 0 {
			target = u.entries[h+1].row
		} else {
			target = 0
		}
	}
	return u.move(target - u.cursor)
}

// ensureVisible scrolls the list so the selected row (and the header right
// above it) is on screen.
func (u *ui) ensureVisible() {
	if len(u.rows) == 0 || len(u.rowEntry) != len(u.rows) {
		u.top = 0
		return
	}
	h := u.listHeight()
	e := u.rowEntry[u.cursor]
	first := u.entryLine[e]
	last := first + 1
	if e > 0 && u.entries[e-1].row < 0 {
		first--
	}
	if first < u.top {
		u.top = first
	}
	if last >= u.top+h {
		u.top = last - h + 1
	}
	if u.top < 0 {
		u.top = 0
	}
}

// ---- preview ----

// previewCmd loads the messages of the selected session unless cached.
func (u *ui) previewCmd() tea.Cmd {
	r := u.selected()
	if r == nil {
		return nil
	}
	id := r.SessionID
	if _, ok := u.cache[id]; ok && !u.cacheStale[id] {
		return nil
	}
	if _, ok := u.cacheErr[id]; ok {
		return nil
	}
	st := u.st
	return func() tea.Msg {
		msgs, err := st.Messages(id)
		return previewMsg{id: id, msgs: msgs, err: err}
	}
}

func (u *ui) remember(id int64, msgs []model.Message, err error) {
	delete(u.cacheStale, id)
	if err != nil {
		delete(u.cache, id)
		u.cacheErr[id] = err
		return
	}
	delete(u.cacheErr, id)
	if msgs == nil {
		msgs = []model.Message{}
	}
	if _, ok := u.cache[id]; !ok {
		u.cacheOrder = append(u.cacheOrder, id)
	}
	u.cache[id] = msgs
	for len(u.cacheOrder) > cacheSize {
		delete(u.cache, u.cacheOrder[0])
		delete(u.cacheStale, u.cacheOrder[0])
		u.cacheOrder = u.cacheOrder[1:]
	}
}

// markStale flags every cached preview as outdated after a sync: it stays on
// screen (no "Loading…" flicker every minute) until previewCmd reloads it.
func (u *ui) markStale() {
	for id := range u.cache {
		u.cacheStale[id] = true
	}
	u.cacheErr = map[int64]error{}
}

// freshMessages returns cached messages that no sync has outdated, else nil.
func (u *ui) freshMessages(id int64) []model.Message {
	if u.cacheStale[id] {
		return nil
	}
	return u.cache[id]
}

// ---- sync ----

// startSync runs one sync in the background unless one is running. Progress
// and the result come back as messages through a channel.
func (u *ui) startSync() tea.Cmd {
	if u.syncing || u.svc.sync == nil {
		return nil
	}
	u.syncing = true
	u.progress = syncer.Progress{}
	ch := make(chan tea.Msg, 16)
	run, ctx, bg := u.svc.sync, u.ctx, u.bg
	bg.Add(1)
	go func() {
		defer bg.Done()
		rep, err := func() (rep syncer.Report, err error) {
			defer func() {
				if p := recover(); p != nil {
					err = errors.New("sync crashed")
				}
			}()
			return run(ctx, func(p syncer.Progress) {
				select {
				case ch <- syncProgressMsg{p: p, ch: ch}:
				default: // the UI is busy; the next progress will do
				}
			})
		}()
		select {
		case ch <- syncDoneMsg{rep: rep, err: err}:
		case <-ctx.Done():
		}
	}()
	return waitSync(ch)
}

// waitSync delivers the next message of a running sync. Update re-arms it
// after every progress message until the sync is done.
func waitSync(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (u *ui) onSyncDone(m syncDoneMsg) tea.Cmd {
	u.syncing = false
	u.staleNote = ""
	u.lockedNote = errors.Is(m.err, syncer.ErrLocked)
	u.syncErr = ""
	if m.err != nil && !u.lockedNote && !errors.Is(m.err, context.Canceled) {
		u.syncErr = "Sync failed: " + m.err.Error()
	}
	if m.err == nil {
		u.synced = true
	}
	u.markStale()
	cmds := []tea.Cmd{u.refresh()}
	if u.screen == screenSources {
		cmds = append(cmds, u.sourcesCmd())
	}
	return tea.Batch(cmds...)
}

// ---- update check ----

func (u *ui) updateCheckCmd() tea.Cmd {
	check, st, ctx, version, now := u.svc.checkUpdate, u.st, u.ctx, u.app.Version, u.svc.now
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		rel, err := check(c, version)
		if err == nil {
			_ = st.SetMeta(metaUpdateCheck, now().UTC().Format(time.RFC3339))
			v := ""
			if rel != nil {
				if b, err := json.Marshal(rel); err == nil {
					v = string(b)
				}
			}
			_ = st.SetMeta(metaUpdateRelease, v)
		}
		return updateMsg{rel: rel, err: err}
	}
}

// ---- helpers ----

func (u *ui) agentName(id string) string {
	if n, ok := u.names[id]; ok && n != "" {
		return n
	}
	if id == "" {
		return "?"
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

func (u *ui) liveInfo(r *archive.Row) (launch.LiveInfo, bool) {
	if r == nil || r.Agent != "claude" || u.live == nil {
		return launch.LiveInfo{}, false
	}
	li, ok := u.live[r.NativeID]
	return li, ok
}

// local reports whether this machine has a live source for the session (§4.6):
// the agent's own copy, not only a prompt log or an imported index row
// (those are kept precisely because the agent no longer has the session).
func (u *ui) local(r *archive.Row) bool {
	return !r.Missing && (r.Origin == "" || r.Origin == model.OriginLive) &&
		(r.Machine == "" || r.Machine == u.app.Machine) && u.knownRoot(r)
}

// knownRoot reports whether a Claude session's data root is one this PC's
// Claude adapter discovers; any other root is never passed to CLAUDE_CONFIG_DIR.
func (u *ui) knownRoot(r *archive.Row) bool {
	if r.Root == "" || r.Agent != "claude" {
		return true
	}
	for _, k := range u.roots {
		if strings.EqualFold(filepath.Clean(k), filepath.Clean(r.Root)) {
			return true
		}
	}
	return false
}

func itoa(n int) string { return strconv.Itoa(n) }
