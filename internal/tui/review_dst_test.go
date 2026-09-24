package tui

import (
	"testing"
	"time"
	_ "time/tzdata"
)

func TestReviewYesterdayOnMidnightDSTDay(t *testing.T) {
	for _, zone := range []string{"Asia/Beirut", "Africa/Cairo", "America/Havana", "America/Santiago", "Europe/Berlin"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		// scan 2026 for days whose local midnight does not exist
		for d := time.Date(2026, 1, 2, 12, 0, 0, 0, loc); d.Year() == 2026; d = d.AddDate(0, 0, 1) {
			now := time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, loc)
			yest := now.Add(-24 * time.Hour).Add(-time.Hour) // late morning yesterday
			if got := dayLabel(now, yest); got != "Yesterday" {
				t.Errorf("%s: now %s, a session at %s is labelled %q, want Yesterday", zone, now.Format("Mon 2 Jan 15:04 MST"), yest.Format("Mon 2 Jan 15:04 MST"), got)
			}
		}
	}
}
