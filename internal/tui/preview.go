package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/model"
)

// previewData is what the preview pane shows for one row (spec §2.2).
type previewData struct {
	row         archive.Row
	agentName   string
	msgs        []model.Message
	loaded      bool
	err         error
	search      bool // a query is active: show the hit between its neighbours
	now         time.Time
	thisPC      bool // the session was last seen live on this machine
	live        bool
	interactive bool
	mode        string
}

// previewHeader is the first line: agent · project · started · last active · n messages · coverage.
func previewHeader(d previewData) string {
	path := projectFolder(&d.row)
	if path == "" {
		path = "(no project)"
	}
	cov := string(d.row.Coverage)
	if cov == "" {
		cov = "?"
	}
	return fmt.Sprintf("%s · %s · started %s · last active %s · %d messages · %s",
		d.agentName, path, localDateTime(d.now, d.row.StartedAt), localDateTime(d.now, d.row.UpdatedAt),
		d.row.MessageCount, cov)
}

// previewTop returns the header and status lines of the preview.
func previewTop(d previewData, width int) []string {
	var lines []string
	for _, l := range displayLines(previewHeader(d), width, d.mode, false) {
		lines = append(lines, styleBold.Render(l))
	}
	if !d.thisPC {
		lines = append(lines, styleWarn.Render(fit("From another PC — read only", width)))
	}
	if d.row.Missing {
		// two spaces: the 🗄 glyph spills into the next cell (§2.6 spike)
		lines = append(lines, styleWarn.Render(fit(markMissing+"  Archive only: the agent deleted its copy", width)))
	}
	if d.live {
		s := markLive + " Open in another window"
		if !d.interactive {
			s += " (background session)"
		}
		lines = append(lines, styleOK.Render(fit(s, width)))
	}
	return lines
}

// previewLines renders the preview pane: header, then the matching message
// with the previous and next message dimmed ("sandwich"), or the start of the
// session when there is no query. At most height lines of at most width cells.
func previewLines(d previewData, width, height int) []string {
	lines := previewTop(d, width)
	lines = append(lines, "")
	room := height - len(lines)
	switch {
	case room <= 0:
		return lines[:max(0, height)]
	case !d.loaded:
		return append(lines, styleDim.Render("Loading…"))
	case d.err != nil:
		return append(lines, styleErr.Render(fit("Cannot read the messages: "+d.err.Error(), width)))
	case len(d.msgs) == 0:
		return append(lines, styleDim.Render("(no messages archived)"))
	}

	hit := -1
	if d.search {
		hit = seqIndex(d.row.HitSeq, len(d.msgs))
	}
	if hit < 0 {
		for i := 0; i < len(d.msgs) && room > 0; i++ {
			b := d.block(d.msgs[i], width, min(room, 8), false, "")
			lines = append(lines, b...)
			room -= len(b)
		}
		return lines
	}

	var prev, next []string
	if hit > 0 {
		prev = d.block(d.msgs[hit-1], width, 3, true, "")
	}
	if hit+1 < len(d.msgs) {
		next = d.block(d.msgs[hit+1], width, 3, true, "")
	}
	hitRoom := room - len(prev) - len(next)
	if hitRoom < 4 { // not enough space: drop the neighbours, previous first
		prev, hitRoom = nil, room-len(next)
		if hitRoom < 4 {
			next, hitRoom = nil, room
		}
	}
	lines = append(lines, prev...)
	lines = append(lines, d.block(d.msgs[hit], width, hitRoom, false, d.row.Snippet)...)
	lines = append(lines, next...)
	return lines
}

func (d previewData) roleLabel(m model.Message) string {
	switch {
	case m.Kind == model.KindToolCall:
		return "Tool call"
	case m.Kind == model.KindToolOutput || m.Role == model.RoleTool:
		return "Tool output"
	case m.Role == model.RoleUser:
		return "You"
	case m.Role == model.RoleAssistant:
		return d.agentName
	case m.Role == model.RoleSystem:
		return "System"
	default:
		return string(m.Role)
	}
}

// block renders one message: a label line, then its text indented by two
// cells, cut to maxLines lines in total. focus (a snippet) moves the start of
// a long text close to the match.
func (d previewData) block(m model.Message, width, maxLines int, dim bool, focus string) []string {
	if maxLines <= 0 {
		return nil
	}
	label := d.roleLabel(m) + ":"
	var out []string
	if dim {
		out = append(out, styleDim.Render(fit(label, width)))
	} else {
		out = append(out, styleHeader.Render(fit(label, width)))
	}
	text := m.Text
	if focus != "" {
		text = focusText(text, focus)
	}
	body := compactBlank(displayLines(text, max(1, width-2), d.mode, true))
	if len(out)+len(body) > maxLines {
		keep := max(0, maxLines-len(out)-1)
		body = append(body[:keep], "…")
	}
	for _, l := range body {
		l = "  " + l
		if dim {
			l = styleDim.Render(l)
		}
		out = append(out, l)
	}
	return out[:min(len(out), maxLines)]
}

// compactBlank drops leading/trailing blank lines and collapses runs of blank lines.
func compactBlank(lines []string) []string {
	out := lines[:0:0]
	blank := true
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, l)
		blank = false
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// focusText starts a long text shortly before the place the snippet comes
// from, so the preview shows the match.
func focusText(text, snippet string) string {
	key := ""
	for _, w := range strings.FieldsFunc(strings.Trim(snippet, "… "), unicode.IsSpace) {
		if utf8.RuneCountInString(w) >= 4 {
			key = w
			break
		}
	}
	if key == "" {
		return text
	}
	i := strings.Index(text, key)
	if i < 300 {
		return text
	}
	// back up about 120 runes, then to the start of a word
	start := i
	for n := 0; n < 120 && start > 0; n++ {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		start -= size
	}
	if j := strings.IndexAny(text[start:i], " \n"); j >= 0 {
		start += j + 1
	}
	return "…" + text[start:]
}
