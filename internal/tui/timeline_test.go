package tui

import (
	"testing"
	"time"
)

func TestRelAge(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{-5 * time.Minute, "now"}, // clock skew: never negative
		{20 * time.Second, "now"},
		{5 * time.Minute, "5m"},
		{59 * time.Minute, "59m"},
		{3 * time.Hour, "3h"},
		{23 * time.Hour, "23h"},
		{3 * 24 * time.Hour, "3d"},
		{29 * 24 * time.Hour, "29d"},
		{7 * 30 * 24 * time.Hour, "7mo"},
		{400 * 24 * time.Hour, "1y"},
		{3 * 365 * 24 * time.Hour, "3y"},
	}
	for _, c := range cases {
		if got := relAge(now, now.Add(-c.ago)); got != c.want {
			t.Errorf("relAge(-%v) = %q, want %q", c.ago, got, c.want)
		}
	}
	if got := relAge(now, time.Time{}); got != "" {
		t.Errorf("relAge(zero) = %q, want empty", got)
	}
}

func TestDayLabel(t *testing.T) {
	loc := time.FixedZone("X", 3*3600)
	now := time.Date(2026, 9, 23, 1, 0, 0, 0, loc) // Wed 23 Sep, 01:00 local
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 9, 23, 0, 30, 0, 0, loc), "Today"},
		{time.Date(2026, 9, 22, 23, 59, 0, 0, loc), "Yesterday"},
		{time.Date(2026, 9, 21, 10, 0, 0, 0, loc), "Mon 21 Sep 2026"},
		// UTC time that is "today" in the local zone.
		{time.Date(2026, 9, 22, 21, 30, 0, 0, time.UTC), "Today"},
	}
	for _, c := range cases {
		if got := dayLabel(now, c.t); got != c.want {
			t.Errorf("dayLabel(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestTimeRange(t *testing.T) {
	loc := time.FixedZone("X", 3*3600)
	now := time.Date(2026, 9, 23, 15, 4, 5, 0, loc)
	r := rangeAny
	var labels []string
	for i := 0; i < 5; i++ {
		labels = append(labels, r.String())
		r = r.next()
	}
	if r != rangeAny {
		t.Errorf("range did not cycle back to Any time: %v", r)
	}
	want := []string{"Any time", "Today", "7 days", "30 days", "12 months"}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("label %d = %q, want %q", i, labels[i], want[i])
		}
	}
	if !rangeAny.since(now).IsZero() {
		t.Error("Any time must have a zero Since")
	}
	if got, want := rangeToday.since(now), time.Date(2026, 9, 23, 0, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("Today since = %v, want %v", got, want)
	}
	if got, want := range7d.since(now), now.AddDate(0, 0, -7); !got.Equal(want) {
		t.Errorf("7 days since = %v, want %v", got, want)
	}
	if got, want := range12mo.since(now), now.AddDate(-1, 0, 0); !got.Equal(want) {
		t.Errorf("12 months since = %v, want %v", got, want)
	}
}
