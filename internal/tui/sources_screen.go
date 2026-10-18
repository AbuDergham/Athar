package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/update"
)

// sourcesData is what the Sources & help screen shows (spec §2.5).
type sourcesData struct {
	sources  []archive.SourceStatus
	agents   []archive.AgentCount
	path     string
	size     int64
	lastSync time.Time
}

func (u *ui) sourcesCmd() tea.Cmd {
	st, machine, size := u.st, u.app.Machine, u.svc.fileSize
	return func() tea.Msg {
		d := &sourcesData{path: st.Path()}
		var err error
		d.sources, err = st.Sources(machine)
		d.agents, _ = st.Agents()
		if size != nil && d.path != "" {
			d.size = size(d.path)
		}
		if v, e := st.Meta(metaLastSync); e == nil && v != "" {
			d.lastSync, _ = time.Parse(time.RFC3339, v)
		}
		return sourcesMsg{data: d, err: err}
	}
}

func (u *ui) keySources(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "f2":
		u.screen = screenMain
		return nil
	case "up":
		u.srcTop--
	case "down":
		u.srcTop++
	case "pgup":
		u.srcTop -= max(1, u.height-3)
	case "pgdown":
		u.srcTop += max(1, u.height-3)
	case "home":
		u.srcTop = 0
	case "end":
		u.srcTop = 1 << 30
	default:
		if isLetterKey(msg.Text, 'u') && u.release != nil {
			u.screen, u.notesTop = screenNotes, 0
		}
	}
	return nil
}

func (u *ui) viewSources() string {
	var body []string
	switch {
	case u.src == nil && u.srcErr == nil:
		body = []string{" Loading…"}
	default:
		body = sourcesLines(u.sourcesView(), u.width)
	}
	title := " Sources & help"
	if u.app.Version != "" {
		title += " · Athar " + u.app.Version
	}
	return u.scrollPage(title, body, &u.srcTop,
		keyHelp("↑↓", "scroll", "Esc", "back")+u.updateKeyHint())
}

func (u *ui) updateKeyHint() string {
	if u.release == nil {
		return ""
	}
	return "  " + keyHelp("U", "what's new")
}

// scrollPage renders a titled, scrollable page with a key line at the bottom.
func (u *ui) scrollPage(title string, body []string, top *int, keys string) string {
	h := max(1, u.height-3)
	*top = min(max(*top, 0), max(0, len(body)-h))
	lines := []string{styleBold.Render(fit(title, u.width)), styleRule.Render(strings.Repeat("─", u.width))}
	for i := 0; i < h; i++ {
		lines = append(lines, fit(at(body, *top+i), u.width))
	}
	lines = append(lines, fit(" "+keys, u.width))
	return strings.Join(lines, "\n")
}

// sourcesView gathers everything sourcesLines needs.
func (u *ui) sourcesView() sourcesView {
	v := sourcesView{
		adapters: u.app.Adapters, now: u.svc.now(), home: u.app.Env.Home,
		release: u.release, mode: u.mode, err: u.srcErr,
		hostname: u.app.Hostname, machine: u.app.Machine,
	}
	if u.src != nil {
		v.data = *u.src
	}
	return v
}

type sourcesView struct {
	adapters []adapters.Adapter
	data     sourcesData
	err      error
	now      time.Time
	home     string
	release  *update.Release
	mode     string
	hostname string
	machine  string
}

// sourcesLines renders the Sources & help screen body:
//
//	Claude Code        36 sessions   2 sources    resume ✓         last read 2 min ago
//	Cursor              3 sessions   1 source     resume via app   last read 1 h ago    1 error
//	…
//	Problems (per source)
//	Archive: …\archive.db (312.0 MB) · last sync 2026-09-23 03:10
//	Update v1.1.0 available — press U for what's new
//	Keys & search help
func sourcesLines(v sourcesView, width int) []string {
	var out []string
	add := func(s string) { out = append(out, s) }
	if v.err != nil {
		add(styleErr.Render(" Cannot read the sources: " + displayText(v.err.Error(), v.mode)))
	}

	counts := map[string]int{}
	for _, a := range v.data.agents {
		counts[a.Agent] = a.Sessions
	}
	type agg struct {
		sources, sessions, errors, skipped, warnings int
		lastOK                                       time.Time
	}
	byAdapter := map[string]*agg{}
	for _, s := range v.data.sources {
		a := byAdapter[s.Adapter]
		if a == nil {
			a = &agg{}
			byAdapter[s.Adapter] = a
		}
		a.sources++
		a.sessions += s.Sessions
		if s.LastError != "" {
			a.errors++
		}
		a.skipped += s.Skipped
		a.warnings += len(s.Warnings)
		if s.LastOK.After(a.lastOK) {
			a.lastOK = s.LastOK
		}
	}

	nameW := 12
	for _, ad := range v.adapters {
		nameW = max(nameW, cellWidth(ad.Name()))
	}
	nameW = min(nameW, 22)
	known := map[string]bool{}
	for _, ad := range v.adapters {
		id := ad.ID()
		known[id] = true
		a := byAdapter[id]
		if a == nil {
			a = &agg{}
		}
		sessions := fmt.Sprintf("%5d %-8s", counts[id], pick(counts[id], "session", "sessions"))
		if counts[id] == 0 && a.sessions > 0 { // importers: their sessions belong to other agents
			sessions = fmt.Sprintf("%5d %-8s", a.sessions, "imported")
		}
		src := "not found"
		if a.sources > 0 {
			src = plural(a.sources, "source", "sources")
		}
		line := fmt.Sprintf(" %s %s   %-11s  %-15s", fit(ad.Name(), nameW), sessions, src, capsLabel(ad.Caps()))
		if a.sources > 0 {
			line += "  last read " + agoWords(v.now, a.lastOK)
		}
		var probs []string
		if a.errors > 0 {
			probs = append(probs, styleErr.Render(plural(a.errors, "error", "errors")))
		}
		if a.skipped > 0 {
			probs = append(probs, styleWarn.Render(plural(a.skipped, "record skipped", "records skipped")))
		}
		if a.warnings > 0 {
			probs = append(probs, styleWarn.Render(plural(a.warnings, "note", "notes")))
		}
		if len(probs) > 0 {
			line += "  " + strings.Join(probs, ", ")
		}
		add(line)
	}
	// agents in the archive without an adapter in this build (e.g. imported)
	var extra []string
	for id := range counts {
		if !known[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		add(fmt.Sprintf(" %s %5d %-8s   %s", fit(id, nameW), counts[id], pick(counts[id], "session", "sessions"), "archive only"))
	}
	if len(v.adapters) == 0 && len(extra) == 0 {
		add(styleDim.Render(" No agent readers in this build."))
	}

	// para adds text word-wrapped to the screen: first prefixes its first line,
	// cont the others; style (may be nil) is applied per line. Long paths and
	// messages wrap instead of hiding what follows them.
	para := func(first, cont, text string, style func(...string) string) {
		for i, l := range displayLines(text, max(1, width-max(cellWidth(first), cellWidth(cont))), v.mode, false) {
			if style != nil {
				l = style(l)
			}
			if i == 0 {
				add(first + l)
			} else {
				add(cont + l)
			}
		}
	}

	// per-source problems (§2.5): never pop-ups, always here
	header := false
	for _, s := range v.data.sources {
		type note struct {
			text  string
			style func(...string) string
		}
		var notes []note
		if s.LastError != "" {
			notes = append(notes, note{"error: " + s.LastError, styleErr.Render})
		}
		if s.Skipped > 0 {
			notes = append(notes, note{plural(s.Skipped, "record", "records") + " could not be read (skipped)", styleWarn.Render})
		}
		for _, w := range s.Warnings {
			notes = append(notes, note{w, styleWarn.Render})
		}
		if len(notes) == 0 {
			continue
		}
		if !header {
			header = true
			add("")
			add(styleBold.Render(" Problems"))
		}
		para("   ", "     ", s.Adapter+"  "+shortPath(s.Path, v.home), nil)
		for _, n := range notes {
			para("      ", "      ", n.text, n.style)
		}
	}

	add("")
	arch := "Archive: " + v.data.path
	if v.data.size > 0 {
		arch += fmt.Sprintf(" (%.1f MB)", float64(v.data.size)/(1<<20))
	}
	if v.data.lastSync.IsZero() {
		arch += " · not synced yet"
	} else {
		arch += " · last sync " + localDateTime(v.now, v.data.lastSync)
	}
	para(" ", "   ", arch, nil)
	if v.hostname != "" || v.machine != "" {
		para(" ", "   ", "This PC: "+strings.TrimSpace(v.hostname+" ("+v.machine+")")+" · sessions last seen on another PC are read only", nil)
	}
	if v.release != nil {
		para(" ", "   ", "Update "+v.release.Version+" available — press U for what's new", styleWarn.Render)
	}
	para(" ", "   ", "Arabic display: "+v.mode+` (set [display] arabic = "shaped" or "raw" in athar.toml)`, nil)
	add("")
	add(styleBold.Render(" Keys & search"))
	for _, help := range []string{
		"↑↓ PgUp PgDn Home End move · Ctrl+↑/↓ jump between days · Enter actions · Shift+Enter resume without permission prompts · Tab agent · Ctrl+T time · F2 sources & help · Esc clears the search, or quits when it is empty · Ctrl+C quits",
		`"exact phrase" · words may be in different messages · words under 3 letters match titles and projects only · Arabic works with or without ال, hamza forms and diacritics`,
		// two spaces after 🗄: its glyph spills into the next cell (§2.6 spike)
		"Markers: " + markLive + " open in another window · " + markMissing + "  archive only (the agent deleted its copy) · (titles) (prompts) … how much of the conversation Athar could read",
	} {
		para("   ", "   ", help, nil)
	}
	return out
}

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func capsLabel(c adapters.Caps) string {
	switch {
	case c.Resume != nil:
		return "resume ✓"
	case c.App != nil:
		return "resume via app"
	default:
		return "read only"
	}
}

// agoWords is a relative time for the Sources screen: "2 min ago".
func agoWords(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	default:
		return plural(int(d/(24*time.Hour)), "day", "days") + " ago"
	}
}

// shortPath replaces the home folder prefix with ~.
func shortPath(p, home string) string {
	if home != "" && len(p) >= len(home) && strings.EqualFold(p[:len(home)], home) {
		return "~" + p[len(home):]
	}
	return p
}

// ---- update notes (U) ----

func (u *ui) keyNotes(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		u.screen = screenSources
		return nil
	case "up":
		u.notesTop--
	case "down":
		u.notesTop++
	case "pgup":
		u.notesTop -= max(1, u.height-3)
	case "pgdown":
		u.notesTop += max(1, u.height-3)
	default:
		if isLetterKey(msg.Text, 'i') && u.release != nil && !u.installing &&
			update.Newer(u.release.Version, u.app.Version) { // never install an older release
			u.installing = true
			install, ctx, rel, bg := u.svc.install, u.ctx, u.release, u.bg
			bg.Add(1) // Run waits for it: never exit between Install's renames
			return func() tea.Msg {
				defer bg.Done()
				return installMsg{err: install(ctx, rel)} // Install bounds its own downloads
			}
		}
	}
	return nil
}

func (u *ui) viewNotes() string {
	r := u.release
	if r == nil {
		u.screen = screenSources
		return u.viewSources()
	}
	notes := strings.TrimSpace(r.Notes)
	if notes == "" {
		notes = "(no release notes)"
	}
	body := []string{""}
	for _, l := range displayLines(notes, max(1, u.width-2), u.mode, false) {
		body = append(body, " "+l)
	}
	if r.URL != "" {
		body = append(body, "", " "+r.URL)
	}
	keys := keyHelp("I", "install", "↑↓", "scroll", "Esc", "back")
	switch {
	case u.installing:
		keys = styleDim.Render("Installing " + r.Version + "…")
	case u.status != "" && u.statusErr:
		keys = styleErr.Render(displayText(u.status, u.mode)) + "  " + keyHelp("Esc", "back")
	}
	return u.scrollPage(" Athar "+r.Version+" — what's new", body, &u.notesTop, keys)
}
