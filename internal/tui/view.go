package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/AbuDergham/Athar/internal/archive"
)

// View renders the current screen.
func (u *ui) View() tea.View {
	var content string
	switch {
	case u.width <= 0 || u.height <= 0:
		content = "Athar"
	case u.screen == screenSources:
		content = u.viewSources()
	case u.screen == screenNotes:
		content = u.viewNotes()
	default:
		content = u.viewMain()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "Athar"
	return v
}

// panes returns the widths of the list and the preview (0 = no preview on
// narrow terminals). One cell between them holds the divider.
func (u *ui) panes() (int, int) {
	if u.width < 70 {
		return u.width, 0
	}
	list := u.width * 11 / 20
	return list, u.width - list - 1
}

func (u *ui) chips() string {
	agent := "All agents"
	if u.agentFilter != "" {
		agent = u.agentName(u.agentFilter)
	}
	a := "[" + agent + "]"
	if u.agentFilter != "" {
		a = styleKey.Render(a)
	}
	t := "[" + u.timeRange.String() + "]"
	if u.timeRange != rangeAny {
		t = styleKey.Render(t)
	}
	return a + " " + t
}

// layoutInput gives the search box the room left of the filter chips.
func (u *ui) layoutInput() {
	u.inputW = max(1, u.width-cellWidth(" Search: ")-cellWidth(u.chips())-3)
}

func (u *ui) viewMain() string {
	w := u.width
	listW, prevW := u.panes()
	h := u.listHeight()
	lines := make([]string, 0, u.height)
	lines = append(lines, u.titleLine(w), u.searchLine(w))
	if prevW > 0 {
		lines = append(lines, styleRule.Render(strings.Repeat("─", listW)+"┬"+strings.Repeat("─", prevW)))
	} else {
		lines = append(lines, styleRule.Render(strings.Repeat("─", w)))
	}
	var left, right []string
	switch {
	case u.menu != nil && prevW == 0:
		left = u.menuLines(listW)
	default:
		left = u.listLines(listW, h)
	}
	if prevW > 0 {
		right = u.rightPane(prevW, h)
	}
	for i := 0; i < h; i++ {
		l := fit(at(left, i), listW)
		if prevW > 0 {
			l += styleRule.Render("│") + fit(at(right, i), prevW)
		}
		lines = append(lines, l)
	}
	if prevW > 0 {
		lines = append(lines, styleRule.Render(strings.Repeat("─", listW)+"┴"+strings.Repeat("─", prevW)))
	} else {
		lines = append(lines, styleRule.Render(strings.Repeat("─", w)))
	}
	lines = append(lines, u.statusLine(w), u.keysLine(w))
	return strings.Join(lines, "\n")
}

func at(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return ""
}

func (u *ui) titleLine(w int) string {
	total := 0
	for _, a := range u.agents {
		total += a.Sessions
	}
	right := fmt.Sprintf("%s · %s ", plural(total, "session", "sessions"), plural(len(u.agents), "agent", "agents"))
	return spread(styleBold.Render(" Athar"), styleDim.Render(right), w)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (u *ui) searchLine(w int) string {
	return spread(" Search: "+u.inputView(), " "+u.chips()+" ", w)
}

// inputView renders the search box.
func (u *ui) inputView() string {
	u.layoutInput()
	return u.input.view(u.inputW, u.mode, "type to search every conversation")
}

// agentColumn is the width of the agent column: the longest agent name, at most 14.
func (u *ui) agentColumn() int {
	w := 5
	for _, a := range u.agents {
		w = max(w, cellWidth(u.agentName(a.Agent)))
	}
	for _, r := range u.rows {
		if len(u.agents) == 0 {
			w = max(w, cellWidth(u.agentName(r.Agent)))
		}
	}
	return min(w, 14)
}

// listLines renders the visible part of the result list.
func (u *ui) listLines(width, height int) []string {
	if len(u.rows) == 0 {
		var out []string
		for _, l := range displayLines(u.emptyMessage(), max(1, width-2), u.mode, false) {
			out = append(out, " "+l)
		}
		return out
	}
	now := u.svc.now()
	agentW := u.agentColumn()
	search := u.applied.Query != ""
	// first entry that is (partly) visible
	e := sort.Search(len(u.entries), func(i int) bool { return u.entryLine[i+1] > u.top })
	var out []string
	skip := u.top - u.entryLine[min(e, len(u.entries))]
	for ; e < len(u.entries) && len(out) < height+skip; e++ {
		en := u.entries[e]
		if en.row < 0 {
			out = append(out, styleHeader.Render(fit(" "+en.header, width)))
			continue
		}
		r := u.rows[en.row]
		_, live := u.liveInfo(&r)
		l1, l2 := rowLines(r, rowView{agentName: u.agentName(r.Agent), agentW: agentW, now: now, live: live, search: search, mode: u.mode}, width-3)
		if en.row == u.cursor {
			out = append(out, styleSelected.Render(fit(" > "+l1, width)), styleSelected.Render(fit("   "+l2, width)))
		} else {
			out = append(out, fit("   "+l1, width), styleDim.Render(fit("   "+l2, width)))
		}
	}
	if skip > 0 && skip <= len(out) {
		out = out[skip:]
	}
	if u.more && len(out) < height {
		out = append(out, styleDim.Render(" Loading more…"))
	}
	return out
}

// emptyMessage is shown in the list area when there are no rows (§2.1, §2.8).
func (u *ui) emptyMessage() string {
	if !u.loaded {
		return "Loading…"
	}
	if u.searchErr != nil {
		return "Search failed: " + u.searchErr.Error()
	}
	f := u.applied
	var desc, withAll, keys []string
	if f.Agent != "" {
		desc = append(desc, u.agentName(f.Agent))
		withAll = append(withAll, "all agents")
		keys = append(keys, "Tab")
	}
	if !f.Since.IsZero() {
		desc = append(desc, u.timeRange.String())
		withAll = append(withAll, "any time")
		keys = append(keys, "Ctrl+T")
	}
	count := func(one, many string) string {
		if u.unfiltered >= 1000 {
			return "1000+ " + many
		}
		return plural(u.unfiltered, one, many)
	}
	if f.Query == "" {
		if len(desc) == 0 {
			switch {
			case u.syncing && u.app.FirstRun:
				return "First run: reading your agents' history (about 1–2 minutes). Results appear as each source finishes."
			case u.syncing:
				return "Reading your agents' history…"
			default:
				return "No agent history found. F2 shows where Athar looked."
			}
		}
		s := "No sessions in " + strings.Join(desc, " · ") + "."
		if u.unfiltered > 0 {
			s += " " + count("session", "sessions") + " with " + strings.Join(withAll, " and ") + " — press " + strings.Join(keys, " / ") + "."
		}
		return s
	}
	if len(desc) > 0 && u.unfiltered > 0 {
		return "No matches in " + strings.Join(desc, " · ") + ". " + count("match", "matches") +
			" with " + strings.Join(withAll, " and ") + " — press " + strings.Join(keys, " / ") + "."
	}
	return "No matches. Try fewer or shorter words."
}

// rightPane is the preview, or the action menu under the preview header.
func (u *ui) rightPane(width, height int) []string {
	r := u.selected()
	if r == nil {
		return nil
	}
	d := u.previewFor(r)
	if u.menu == nil {
		return previewLines(d, width, height)
	}
	top := previewTop(d, width)
	if len(top) > 4 {
		top = top[:4]
	}
	return append(append(top, ""), u.menuLines(width)...)
}

func (u *ui) previewFor(r *archive.Row) previewData {
	li, live := u.liveInfo(r)
	msgs, loaded, err := u.messagesFor(r.SessionID)
	return previewData{
		row: *r, agentName: u.agentName(r.Agent), msgs: msgs, loaded: loaded, err: err,
		search: u.applied.Query != "", now: u.svc.now(),
		thisPC: r.Machine == "" || r.Machine == u.app.Machine,
		live:   live, interactive: li.Interactive, mode: u.mode,
	}
}

// menuLines renders the open action menu.
func (u *ui) menuLines(width int) []string {
	m := u.menu
	out := []string{styleBold.Render(fit(" Actions", width))}
	for i, e := range m.entries {
		line := fit(fmt.Sprintf(" %s  %s  %s", map[bool]string{true: ">", false: " "}[i == m.sel], e.key, e.label), width)
		if i == m.sel {
			line = styleSelected.Render(line)
		}
		out = append(out, line)
	}
	add := func(text string, st func(...string) string) {
		for _, l := range displayLines(text, max(1, width-2), u.mode, false) {
			out = append(out, st(" "+l))
		}
	}
	if m.warning != "" && m.entries[m.sel].id == itemResume {
		add("⚠ "+m.warning, styleWarn.Render)
	} else if m.warning != "" {
		add(m.warning, styleDim.Render)
	}
	if m.err != "" {
		add(m.err, styleErr.Render)
	}
	switch {
	case m.busy != "":
		add(m.busy, styleDim.Render)
	case m.confirm:
		add("Really skip all permission prompts? y/N", styleWarn.Render)
	default:
		add("↑↓ Enter, or press a key · Esc closes", styleDim.Render)
	}
	return out
}

func (u *ui) statusLine(w int) string {
	var parts []string
	now := u.svc.now()
	if u.status != "" && now.Sub(u.statusAt) < statusFor {
		if u.statusErr {
			parts = append(parts, styleErr.Render(displayText(u.status, u.mode)))
		} else {
			parts = append(parts, styleOK.Render(displayText(u.status, u.mode)))
		}
	}
	switch {
	case u.syncing && u.progress.Total > 0:
		parts = append(parts, styleDim.Render(fmt.Sprintf("Syncing %d/%d…", u.progress.Done, u.progress.Total)))
	case u.syncing:
		parts = append(parts, styleDim.Render("Syncing…"))
	case u.lockedNote:
		parts = append(parts, styleDim.Render("Another Athar window is syncing — results will refresh."))
	case u.syncErr != "":
		parts = append(parts, styleErr.Render(displayText(u.syncErr, u.mode)))
	}
	if u.staleNote != "" {
		parts = append(parts, styleWarn.Render(u.staleNote))
	}
	right := ""
	if u.release != nil {
		right = styleWarn.Render("Update "+u.release.Version+" available — see F2") + " "
	}
	return spread(" "+strings.Join(parts, styleDim.Render(" · ")), right, w)
}

func (u *ui) keysLine(w int) string {
	if u.menu != nil {
		return fit(" "+keyHelp("↑↓", "choose", "Enter", "run", "R F ! O C D", "keys", "Esc", "close"), w)
	}
	esc := "quit"
	if u.input.Value() != "" {
		esc = "clear"
	}
	return fit(" "+keyHelp("↑↓", "move", "Enter", "actions", "⇧Enter", "resume, no prompts", "Tab", "agent", "Ctrl+T", "time", "F2", "sources & help", "Esc", esc), w)
}
