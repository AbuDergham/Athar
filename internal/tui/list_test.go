package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/model"
)

func entriesString(es []entry) []string {
	var out []string
	for _, e := range es {
		if e.row < 0 {
			out = append(out, "H:"+e.header)
		} else {
			out = append(out, "r"+string(rune('0'+e.row)))
		}
	}
	return out
}

func TestBuildEntriesTimeline(t *testing.T) {
	loc := time.FixedZone("X", 3*3600)
	now := time.Date(2026, 9, 23, 18, 0, 0, 0, loc)
	rows := []archive.Row{
		{UpdatedAt: time.Date(2026, 9, 23, 17, 0, 0, 0, loc)},
		{UpdatedAt: time.Date(2026, 9, 23, 9, 0, 0, 0, loc)},
		{UpdatedAt: time.Date(2026, 9, 22, 9, 0, 0, 0, loc)},
		{UpdatedAt: time.Date(2026, 9, 21, 9, 0, 0, 0, loc)},
	}
	got := entriesString(buildEntries(rows, true, now, false))
	want := []string{"H:Today · 2", "r0", "r1", "H:Yesterday · 1", "r2", "H:Mon 21 Sep 2026 · 1", "r3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("timeline entries = %q\nwant %q", got, want)
	}
	// More pages to load: the last day may continue, its count is open-ended.
	got = entriesString(buildEntries(rows, true, now, true))
	if got[len(got)-2] != "H:Mon 21 Sep 2026 · 1+" {
		t.Errorf("last header with more pages = %q", got[len(got)-2])
	}
}

func TestBuildEntriesSearch(t *testing.T) {
	now := time.Now()
	rows := []archive.Row{
		{MatchedTerms: 2, TotalTerms: 2},
		{MatchedTerms: 2, TotalTerms: 2},
		{MatchedTerms: 1, TotalTerms: 2},
	}
	got := entriesString(buildEntries(rows, false, now, false))
	want := []string{"r0", "r1", "H:Partial matches (1 of 2 words)", "r2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("search entries = %q, want %q", got, want)
	}
	rows = []archive.Row{
		{MatchedTerms: 2, TotalTerms: 3},
		{MatchedTerms: 2, TotalTerms: 3},
		{MatchedTerms: 1, TotalTerms: 3},
	}
	got = entriesString(buildEntries(rows, false, now, false))
	want = []string{"H:Partial matches (2 of 3 words)", "r0", "r1", "H:Partial matches (1 of 3 words)", "r2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("partial-only entries = %q, want %q", got, want)
	}
	// Single-word queries never show a partial header.
	rows = []archive.Row{{MatchedTerms: 1, TotalTerms: 1}}
	if got := entriesString(buildEntries(rows, false, now, false)); !reflect.DeepEqual(got, []string{"r0"}) {
		t.Errorf("one-word entries = %q", got)
	}
}

func TestRowLines(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	r := archive.Row{
		Agent: "claude", Title: "Download videos", ProjectName: "TelegramVideos",
		UpdatedAt: now.Add(-3 * 24 * time.Hour), Coverage: model.CoverageFull,
		Snippet: "…download every video from the…", HitSeq: 3,
	}
	v := rowView{agentName: "Claude Code", agentW: 11, now: now, mode: arabicShaped}

	l1, l2 := rowLines(r, v, 60)
	if l1 != "Claude Code  3d  TelegramVideos" {
		t.Errorf("line1 = %q", l1)
	}
	if l2 != "Download videos" {
		t.Errorf("timeline line2 = %q", l2)
	}

	v.search = true
	_, l2 = rowLines(r, v, 60)
	if l2 != `"…download every video from the…"` {
		t.Errorf("search line2 = %q", l2)
	}

	// no snippet (title match) → quoted title; coverage word when not full.
	r.Snippet, r.Coverage = "", model.CoverageTitles
	_, l2 = rowLines(r, v, 60)
	if l2 != `"Download videos" (titles)` {
		t.Errorf("title-match line2 = %q", l2)
	}

	// markers: live and archive-only.
	v.live = true
	r.Missing = true
	l1, _ = rowLines(r, v, 60)
	if !strings.HasSuffix(l1, "TelegramVideos "+markLive+" "+markMissing) {
		t.Errorf("markers line1 = %q", l1)
	}

	// narrow: everything fits the width.
	for _, w := range []int{10, 20, 25} {
		a, b := rowLines(r, v, w)
		if cellWidth(a) > w || cellWidth(b) > w {
			t.Errorf("width %d: %q (%d) / %q (%d)", w, a, cellWidth(a), b, cellWidth(b))
		}
	}

	// untitled sessions.
	r.Title = ""
	v.search, v.live, r.Missing = false, false, false
	if _, l2 := rowLines(r, v, 60); !strings.HasPrefix(l2, "(no title)") {
		t.Errorf("untitled line2 = %q", l2)
	}
}

func TestRowLinesArabicTitleIsShaped(t *testing.T) {
	now := time.Now()
	r := archive.Row{Title: "مشروع جديد", ProjectName: "x", UpdatedAt: now, Coverage: model.CoverageFull}
	_, l2 := rowLines(r, rowView{agentName: "Codex", agentW: 5, now: now, mode: arabicShaped}, 40)
	if l2 != Visual(Shape("مشروع جديد")) {
		t.Errorf("shaped title = %q", l2)
	}
	_, l2 = rowLines(r, rowView{agentName: "Codex", agentW: 5, now: now, mode: arabicRaw}, 40)
	if l2 != "مشروع جديد" {
		t.Errorf("raw title = %q", l2)
	}
}
