package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/syncer"
)

func TestAppendLogTrims(t *testing.T) {
	path := filepath.Join(t.TempDir(), syncLogName)
	line := strings.Repeat("x", 99) + "\n"
	big := strings.Repeat(line, (maxLogSize/len(line))+10)
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendLog(path, "newest entry\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > keepLogSize+len("newest entry\n") {
		t.Errorf("log not trimmed: %d bytes", len(got))
	}
	if !bytes.HasSuffix(got, []byte("newest entry\n")) {
		t.Errorf("log does not end with the new entry")
	}
	if !bytes.HasPrefix(got, []byte(line)) {
		t.Errorf("trimmed log does not start at a line boundary: %q", got[:20])
	}
}

func TestAppendLogCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), syncLogName)
	for _, s := range []string{"a\n", "b\n"} {
		if err := appendLog(path, s); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != "a\nb\n" {
		t.Errorf("log = %q", got)
	}
}

func TestSummary(t *testing.T) {
	cases := []struct {
		rep  syncer.Report
		want []string
	}{
		{syncer.Report{Sources: 71, Unchanged: 71, Duration: 42*time.Millisecond + 400*time.Microsecond},
			[]string{"Synced in 42ms", "71 sources", "71 unchanged", "0 new sessions"}},
		{syncer.Report{Sources: 1, Read: 1, RetryLater: 2, NewSessions: 1, NewlyMissing: 3, Duration: 2740 * time.Millisecond},
			[]string{"Synced in 2.7s", "1 source (", "2 still being written", "1 new session,", "3 no longer on disk"}},
	}
	for _, c := range cases {
		got := summary(c.rep)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("summary = %q, missing %q", got, w)
			}
		}
	}
}
