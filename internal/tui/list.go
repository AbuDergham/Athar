package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/model"
)

// Row markers (spec §2.2). Both measure one cell in lipgloss and in Windows
// Terminal 1.24 (§2.6 spike); they sit at the end of the first row line so a
// terminal that draws them wider cannot shift the columns.
const (
	markLive    = "●" // open in another window (§6.5)
	markMissing = "🗄" // archive-only: the agent deleted its copy
)

// entry is one item of the result list: a header line or a session row
// (which takes two lines).
type entry struct {
	header string // header text when row < 0
	row    int    // index into the rows; -1 for a header
}

func (e entry) height() int {
	if e.row < 0 {
		return 1
	}
	return 2
}

// buildEntries interleaves rows with headers: one header per local calendar
// day for the timeline ("Today · 3"), or "Partial matches (k of n words)"
// before each group of partial search results. more means further pages may
// exist, so the count of the last day is open-ended ("· 12+").
func buildEntries(rows []archive.Row, timeline bool, now time.Time, more bool) []entry {
	es := make([]entry, 0, len(rows)+8)
	if timeline {
		for i := 0; i < len(rows); {
			day := dayStart(now, rows[i].UpdatedAt)
			j := i
			for j < len(rows) && dayStart(now, rows[j].UpdatedAt).Equal(day) {
				j++
			}
			count := fmt.Sprint(j - i)
			if more && j == len(rows) {
				count += "+"
			}
			es = append(es, entry{header: dayLabel(now, rows[i].UpdatedAt) + " · " + count, row: -1})
			for k := i; k < j; k++ {
				es = append(es, entry{row: k})
			}
			i = j
		}
		return es
	}
	prev := -1
	for i, r := range rows {
		if r.TotalTerms > 1 && r.MatchedTerms < r.TotalTerms && r.MatchedTerms != prev {
			es = append(es, entry{header: fmt.Sprintf("Partial matches (%d of %d words)", r.MatchedTerms, r.TotalTerms), row: -1})
		}
		prev = r.MatchedTerms
		es = append(es, entry{row: i})
	}
	return es
}

// rowView carries what rowLines needs besides the row itself.
type rowView struct {
	agentName string // display name of the agent
	agentW    int    // width of the agent column
	now       time.Time
	live      bool // ● running now
	search    bool // a query is active: show the snippet in quotes
	mode      string
}

// rowLines formats a session as two plain lines of at most width cells:
//
//	Claude Code  3d  TelegramVideos ●
//	"…download every video from the…" (titles)
func rowLines(r archive.Row, v rowView, width int) (string, string) {
	if width < 1 {
		width = 1
	}
	agent := ansi.Truncate(v.agentName, v.agentW, "…")
	agent += strings.Repeat(" ", max(0, v.agentW-cellWidth(agent)))
	age := relAge(v.now, r.UpdatedAt)
	head := agent + " " + strings.Repeat(" ", max(0, 3-len(age))) + age + "  "

	markers := ""
	if v.live {
		markers += " " + markLive
	}
	if r.Missing {
		markers += " " + markMissing
	}
	project := r.ProjectName
	if project == "" {
		project = "(no project)"
	}
	var line1 string
	if room := width - cellWidth(head) - cellWidth(markers); room >= 4 {
		line1 = head + displayLine(project, room, v.mode) + markers
	} else {
		line1 = ansi.Truncate(head+project+markers, width, "…")
	}

	title := r.Title
	if strings.TrimSpace(title) == "" {
		title = "(no title)"
	}
	cov := ""
	if r.Coverage != "" && r.Coverage != model.CoverageFull {
		cov = " (" + string(r.Coverage) + ")"
	}
	var line2 string
	if v.search {
		text := r.Snippet
		if strings.TrimSpace(text) == "" {
			text = title
		}
		room := width - 2 - cellWidth(cov)
		if room < 3 {
			return line1, ansi.Truncate(`"`+cleanLine(text)+`"`+cov, width, "…")
		}
		line2 = `"` + displayLine(text, room, v.mode) + `"` + cov
	} else {
		room := width - cellWidth(cov)
		if room < 3 {
			return line1, ansi.Truncate(cleanLine(title)+cov, width, "…")
		}
		line2 = displayLine(title, room, v.mode) + cov
	}
	return line1, line2
}
