package tui

import (
	"fmt"
	"time"
)

// relAge is the short relative age of t shown in result rows: now, 5m, 3h,
// 3d, 7mo, 2y. It is "" for a zero time.
func relAge(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	days := int(d / (24 * time.Hour))
	switch {
	case days < 30:
		return fmt.Sprintf("%dd", days)
	case days < 365:
		return fmt.Sprintf("%dmo", days/30)
	default:
		return fmt.Sprintf("%dy", days/365)
	}
}

// dayStart returns local midnight of t's day in now's location.
func dayStart(now, t time.Time) time.Time {
	t = t.In(now.Location())
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, now.Location())
}

// dayLabel names the local calendar day of t for timeline headers:
// "Today", "Yesterday", else e.g. "Mon 21 Sep 2026". now carries the location.
func dayLabel(now, t time.Time) string {
	d := dayStart(now, t)
	y, m, dd := t.In(now.Location()).Date()
	ty, tm, td := now.Date()
	// yesterday's calendar date, taken at noon: a midnight that a DST
	// change skips cannot shift it
	yy, ym, yd := time.Date(ty, tm, td-1, 12, 0, 0, 0, now.Location()).Date()
	switch {
	case y == ty && m == tm && dd == td:
		return "Today"
	case y == yy && m == ym && dd == yd:
		return "Yesterday"
	default:
		return d.Format("Mon 2 Jan 2006")
	}
}

// localDateTime formats a time in the PC's zone for the preview.
func localDateTime(now, t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.In(now.Location()).Format("2006-01-02 15:04")
}

// timeRange is the Ctrl+T time filter.
type timeRange int

const (
	rangeAny timeRange = iota
	rangeToday
	range7d
	range30d
	range12mo
	rangeCount
)

func (r timeRange) String() string {
	switch r {
	case rangeToday:
		return "Today"
	case range7d:
		return "7 days"
	case range30d:
		return "30 days"
	case range12mo:
		return "12 months"
	default:
		return "Any time"
	}
}

// next cycles Any time → Today → 7 days → 30 days → 12 months → Any time.
func (r timeRange) next() timeRange { return (r + 1) % rangeCount }

// since is the lower bound on UpdatedAt for the filter; zero for Any time.
func (r timeRange) since(now time.Time) time.Time {
	switch r {
	case rangeToday:
		return dayStart(now, now)
	case range7d:
		return now.AddDate(0, 0, -7)
	case range30d:
		return now.AddDate(0, 0, -30)
	case range12mo:
		return now.AddDate(-1, 0, 0)
	default:
		return time.Time{}
	}
}
