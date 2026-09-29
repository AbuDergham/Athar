package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/launch"
	"github.com/AbuDergham/Athar/internal/model"
	"github.com/AbuDergham/Athar/internal/render"
)

// menuState is the open action menu (spec §2.3).
type menuState struct {
	row      archive.Row
	query    string // the query the row was found with
	in       menuInput
	entries  []menuEntry
	warning  string
	sel      int
	confirm  bool   // "Really skip all permission prompts? y/N"
	err      string // last error (pre-flight, build, …)
	busy     string // action in progress
	caps     adapters.Caps
	override []string
	folder   string
}

// capsFor returns the agent's caps with athar.toml overrides applied, and the
// resume override (nil = none).
func (u *ui) capsFor(agent string) (adapters.Caps, []string) {
	caps := u.caps[agent]
	r, ok := u.app.Config.Resume[strings.ToLower(agent)]
	if !ok {
		return caps, nil
	}
	if r.Fork != nil {
		caps.Fork = r.Fork
	}
	if r.SkipPermissions != nil {
		caps.SkipPermissions = r.SkipPermissions
	}
	if len(r.Resume) == 0 {
		return caps, nil
	}
	return caps, r.Resume
}

func projectFolder(r *archive.Row) string {
	if r.ProjectPath != "" {
		return r.ProjectPath
	}
	return r.Cwd
}

func target(r *archive.Row) launch.Target {
	title := r.Title
	if strings.TrimSpace(title) == "" {
		title = r.ProjectName
	}
	return launch.Target{Agent: r.Agent, NativeID: r.NativeID, Title: title, Cwd: r.Cwd, Root: r.Root}
}

// openMenu opens the action menu for the selected row.
func (u *ui) openMenu() {
	r := u.selected()
	if r == nil {
		return
	}
	if u.svc.live != nil { // fresh liveness: the menu decides between R and F
		u.live = u.svc.live(u.roots)
	}
	caps, override := u.capsFor(r.Agent)
	li, isLive := u.liveInfo(r)
	m := &menuState{row: *r, query: u.applied.Query, caps: caps, override: override, folder: projectFolder(r)}
	// Same rules as launch.Build: an override replaces the program of a CLI
	// agent, or of the app for an app-only agent; empty lists mean "none".
	in := menuInput{
		cliResume:   len(caps.Resume) > 0 || (len(override) > 0 && caps.App == nil),
		canFork:     len(caps.Fork) > 0,
		canSkip:     len(caps.SkipPermissions) > 0,
		local:       u.local(r),
		live:        isLive,
		interactive: li.Interactive,
	}
	if !in.cliResume && caps.App != nil {
		in.app = caps.App.Name
		if in.app == "" {
			in.app = "the app"
		}
	}
	in.folder = m.folder != "" && in.local && u.svc.dirExists != nil && u.svc.dirExists(m.folder)
	if in.cliResume || in.app != "" {
		_, err := u.svc.build(target(r), caps, launch.ActionResume, false, override)
		in.command = err == nil
	}
	m.in = in
	m.entries, m.warning = menuEntries(in)
	u.menu = m
}

// quickSkip (Shift+Enter on the list) resumes the selected session without
// permission prompts right away: the same command as the menu's "!" item
// (a fork when the session is open in another window), without the menu and
// without the confirmation. When "!" does not apply, the menu opens and says why.
func (u *ui) quickSkip() tea.Cmd {
	if u.selected() == nil {
		return nil
	}
	u.openMenu()
	m := u.menu
	if m == nil {
		return nil
	}
	for _, e := range m.entries {
		if e.id == itemSkip {
			return u.runLaunch(skipAction(m.in), true)
		}
	}
	m.err = "Shift+Enter: " + skipUnavailable(m.in) + " Pick an action below."
	return nil
}

// skipUnavailable explains why a session cannot be resumed without permission prompts.
func skipUnavailable(in menuInput) string {
	switch {
	case !in.local:
		return "this session has no live copy on this PC, so it cannot be resumed."
	case !in.cliResume && in.app != "":
		return "this agent opens in its app, not in a terminal."
	case !in.cliResume:
		return "this agent cannot resume a session from a terminal."
	case !in.canSkip:
		return "this agent has no mode without permission prompts."
	default:
		return "this session cannot be resumed without permission prompts right now."
	}
}

func (u *ui) keyMenu(msg tea.KeyPressMsg) tea.Cmd {
	m := u.menu
	if m.busy != "" {
		if msg.String() == "esc" {
			u.menu = nil
		}
		return nil
	}
	if m.confirm {
		m.confirm = false
		if isYes(msg.Text) {
			return u.runLaunch(skipAction(m.in), true)
		}
		return nil
	}
	switch msg.String() {
	case "esc":
		u.menu = nil
		return nil
	case "up":
		m.sel = (m.sel - 1 + len(m.entries)) % len(m.entries)
		return nil
	case "down":
		m.sel = (m.sel + 1) % len(m.entries)
		return nil
	case "enter":
		return u.runItem(m.entries[m.sel].id)
	}
	if id, ok := itemForKey(msg.Text); ok {
		for i, e := range m.entries {
			if e.id == id {
				m.sel = i
				return u.runItem(id)
			}
		}
	}
	return nil
}

func (u *ui) runItem(id menuItemID) tea.Cmd {
	m := u.menu
	m.err = ""
	switch id {
	case itemResume:
		return u.runLaunch(itemResume, false)
	case itemFork:
		return u.runLaunch(itemFork, false)
	case itemSkip:
		m.confirm = true
		return nil
	case itemRead:
		return u.runRead()
	case itemCopy:
		return u.runCopy()
	case itemFolder:
		return u.runFolder()
	}
	return nil
}

func (u *ui) selectItem(id menuItemID) {
	for i, e := range u.menu.entries {
		if e.id == id {
			u.menu.sel = i
		}
	}
}

// runLaunch resumes or forks the menu's session in a new tab (or opens its app).
func (u *ui) runLaunch(id menuItemID, skip bool) tea.Cmd {
	m := u.menu
	action := launch.ActionResume
	if id == itemFork {
		action = launch.ActionFork
	}
	cmd, err := u.svc.build(target(&m.row), m.caps, action, skip, m.override)
	if err != nil {
		m.err = "Cannot build the command: " + err.Error()
		u.selectItem(itemRead)
		return nil
	}
	m.busy = "Starting…"
	pre, start := u.svc.preflight, u.svc.start
	row := m.row
	return func() tea.Msg {
		if err := pre(cmd); err != nil {
			return actionMsg{err: err, offerRead: true}
		}
		if err := start(cmd); err != nil {
			return actionMsg{err: err}
		}
		status := "Opened in new tab"
		switch {
		case cmd.IsApp:
			status = "Opened " + row.ProjectName + " in " + cmd.AppName + ". Pick «" + shortTitle(row.Title) + "» from its chat history."
		case id == itemFork:
			status = "Opened a fork in a new tab"
		}
		return actionMsg{status: status, synced: true}
	}
}

func shortTitle(t string) string {
	t = strings.TrimSpace(cleanLine(t))
	if t == "" {
		return "the conversation"
	}
	return ansi.Truncate(t, 50, "…")
}

// runRead writes the reading page and opens it in the browser (§2.4).
func (u *ui) runRead() tea.Cmd {
	m := u.menu
	m.busy = "Opening the reading page…"
	st, rend, open, dir := u.st, u.svc.render, u.svc.openFile, u.svc.readDir
	row, query := m.row, m.query
	cached := u.freshMessages(row.SessionID)
	name := u.agentName(row.Agent)
	return func() tea.Msg {
		msgs := cached
		if msgs == nil {
			var err error
			if msgs, err = st.Messages(row.SessionID); err != nil {
				return actionMsg{err: err}
			}
		}
		page := render.Page{
			AgentName: name, Title: row.Title, ProjectPath: projectFolder(&row),
			StartedAt: row.StartedAt, UpdatedAt: row.UpdatedAt, Coverage: row.Coverage,
			Messages: msgs, ScrollTo: -1,
		}
		if query != "" {
			page.ScrollTo = seqIndex(row.HitSeq, len(msgs))
			if seqs, err := st.HitSeqs(row.SessionID, query); err == nil {
				for _, s := range seqs {
					if i := seqIndex(s, len(msgs)); i >= 0 {
						page.Hits = append(page.Hits, i)
					}
				}
			}
		}
		path, err := rend(page, dir)
		if err != nil {
			return actionMsg{err: err}
		}
		if err := open(path); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "Opened the conversation in your browser", synced: true}
	}
}

// seqIndex maps a message seq (as stored by the archive) to an index into
// DB.Messages; -1 when out of range. Seqs are 0-based positions in the session.
func seqIndex(seq, n int) int {
	if seq < 0 || seq >= n {
		return -1
	}
	return seq
}

// primaryCommand builds the command C copies: resume, or fork when resume is
// not offered (a live background session).
func (u *ui) primaryCommand() (launch.Command, error) {
	m := u.menu
	action := launch.ActionResume
	if m.in.showFork() && !m.in.showResume() {
		action = launch.ActionFork
	}
	return u.svc.build(target(&m.row), m.caps, action, false, m.override)
}

func (u *ui) runCopy() tea.Cmd {
	cmd, err := u.primaryCommand()
	if err != nil {
		u.menu.err = "Cannot build the command: " + err.Error()
		return nil
	}
	u.menu.busy = "Copying…"
	cp := u.svc.copy
	return func() tea.Msg {
		if err := cp(cmd.PowerShell); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "Copied: " + cmd.PowerShell}
	}
}

func (u *ui) runFolder() tea.Cmd {
	folder := u.menu.folder
	u.menu.busy = "Opening the folder…"
	open := u.svc.openFolder
	return func() tea.Msg {
		if err := open(folder); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "Opened " + folder, synced: true}
	}
}

// onAction handles the end of a menu action: errors stay in the menu (with O
// offered after a failed pre-flight), success closes it.
func (u *ui) onAction(a actionMsg) tea.Cmd {
	if a.err != nil {
		text := a.err.Error()
		if a.offerRead {
			text += " — press O to read it instead"
		}
		if u.menu != nil {
			u.menu.busy = ""
			u.menu.err = text
			if a.offerRead {
				u.selectItem(itemRead)
			}
		} else {
			return u.setStatus(text, true)
		}
		return nil
	}
	u.menu = nil
	redraw := u.setStatus(a.status, false)
	if a.synced {
		return tea.Batch(redraw, u.startSync())
	}
	return redraw
}

// messagesFor returns the cached messages of a session; loaded is false while
// they are being read.
func (u *ui) messagesFor(id int64) (msgs []model.Message, loaded bool, err error) {
	if err, ok := u.cacheErr[id]; ok {
		return nil, true, err
	}
	msgs, ok := u.cache[id]
	return msgs, ok, nil
}
