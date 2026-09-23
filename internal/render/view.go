package render

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

// pageView is the data the template renders; every string is already cleaned.
type pageView struct {
	DocTitle     string
	Title        string
	Agent        string
	Project      string
	Started      string // local "2006-01-02 15:04"; "" when unknown
	StartedISO   string
	Updated      string
	UpdatedISO   string
	Count        string // "5 messages"
	Coverage     string
	CoverageNote string
	Messages     []msgView
	HitCount     int
	HitPos       string // 1-based position of the scroll target among hits, or "–"
	Generated    string
}

// msgView is one rendered message.
type msgView struct {
	Index    int
	Class    string
	Anchor   bool // carries id="hit"
	Role     string
	Time     string // "15:04", or with the date when the day changes
	TimeISO  string
	TimeFull string
	Tool     bool // rendered collapsed in <details>
	Open     bool // <details open>
	Summary  string
	Text     string
	More     string // "(N KB more, truncated)"
}

const (
	dateTimeLayout = "2006-01-02 15:04"
	fullLayout     = "Mon 2006-01-02 15:04:05 -07:00"
)

// buildView turns a Page into template data. now is the generation time.
func buildView(p Page, now time.Time) pageView {
	agent := strings.TrimSpace(cleanText(p.AgentName))
	title := strings.TrimSpace(cleanText(p.Title))
	if title == "" {
		title = "(untitled)"
	}
	project := strings.TrimSpace(cleanText(p.ProjectPath))
	if project == "" {
		project = "(no project)"
	}
	coverage := string(p.Coverage)
	if coverage == "" {
		coverage = "unknown"
	}

	hits := map[int]bool{}
	for _, h := range p.Hits {
		if h >= 0 && h < len(p.Messages) {
			hits[h] = true
		}
	}
	anchor := -1
	if p.ScrollTo >= 0 && p.ScrollTo < len(p.Messages) {
		anchor = p.ScrollTo
	}

	v := pageView{
		DocTitle:     title + " · " + orDefault(agent, "Athar") + " · Athar",
		Title:        title,
		Agent:        orDefault(agent, "Unknown agent"),
		Project:      project,
		Count:        plural(len(p.Messages), "message"),
		Coverage:     coverage,
		CoverageNote: coverageNote(p.Coverage),
		HitCount:     len(hits),
		HitPos:       hitPos(hits, anchor),
		Generated:    now.Local().Format(dateTimeLayout),
	}
	v.Started, v.StartedISO = localTime(p.StartedAt)
	v.Updated, v.UpdatedISO = localTime(p.UpdatedAt)

	lastDay := ""
	v.Messages = make([]msgView, len(p.Messages))
	for i, m := range p.Messages {
		mv := msgView{
			Index:  i,
			Anchor: i == anchor,
			Role:   roleLabel(m.Role, agent),
			Text:   cleanText(m.Text),
		}
		mv.Class = "msg role-" + classToken(string(m.Role)) + " kind-" + classToken(string(m.Kind))
		if hits[i] {
			mv.Class += " hit"
		}
		if !m.At.IsZero() {
			t := m.At.Local()
			day := t.Format("2006-01-02")
			if day != lastDay {
				mv.Time = t.Format(dateTimeLayout)
				lastDay = day
			} else {
				mv.Time = t.Format("15:04")
			}
			mv.TimeISO = t.Format(time.RFC3339)
			mv.TimeFull = t.Format(fullLayout)
		}
		switch m.Kind {
		case model.KindToolCall:
			mv.Tool = true
			mv.Summary = "Tool call: " + orDefault(firstLine(mv.Text, 120), "(empty)")
		case model.KindToolOutput:
			mv.Tool = true
			size := len(mv.Text)
			if kept, more := truncate(mv.Text, maxToolOutput); more > 0 {
				mv.Text = kept
				mv.More = "(" + strconv.Itoa(ceilKB(more)) + " KB more, truncated)"
			}
			if size == 0 {
				mv.Summary = "Output (empty)"
			} else {
				mv.Summary = "Output · " + kb(size) + ": " + firstLine(mv.Text, 100)
			}
		}
		mv.Open = mv.Tool && (mv.Anchor || hits[i])
		v.Messages[i] = mv
	}
	return v
}

// localTime formats t in the PC's time zone for display and for datetime="".
func localTime(t time.Time) (display, iso string) {
	if t.IsZero() {
		return "", ""
	}
	l := t.Local()
	return l.Format(dateTimeLayout), l.Format(time.RFC3339)
}

// roleLabel is the name shown above a message.
func roleLabel(r model.Role, agent string) string {
	switch r {
	case model.RoleUser:
		return "You"
	case model.RoleAssistant:
		return orDefault(agent, "Assistant")
	case model.RoleTool:
		return "Tool"
	case model.RoleSystem:
		return "System"
	}
	return orDefault(strings.TrimSpace(cleanText(string(r))), "Message")
}

// coverageNote explains a coverage level below full.
func coverageNote(c model.Coverage) string {
	switch c {
	case model.CoverageFull, "":
		return ""
	case model.CoveragePartial:
		return "Only part of this conversation could be read from the agent's files."
	case model.CoveragePrompts:
		return "Only your prompts were available; the agent's replies are not in the archive."
	case model.CoverageTitles:
		return "Only the title, dates and project are available for this conversation."
	case model.CoverageMeta:
		return "Only the id and dates are available; the agent's store cannot be read."
	}
	return ""
}

// hitPos is the 1-based rank of anchor among the hits, or "–" when the anchor
// is not a hit.
func hitPos(hits map[int]bool, anchor int) string {
	if !hits[anchor] {
		return "–"
	}
	idx := make([]int, 0, len(hits))
	for h := range hits {
		idx = append(idx, h)
	}
	sort.Ints(idx)
	return strconv.Itoa(sort.SearchInts(idx, anchor) + 1)
}

// classToken keeps s usable as one CSS class token.
func classToken(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return -1
	}, s)
	return orDefault(s, "other")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func itoa(n int) string { return strconv.Itoa(n) }
