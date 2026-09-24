package tui

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/update"
)

// Quitting while "Installing vX…" returns from Run without waiting for
// update.Install: the process can exit between Install's two renames.
func TestReviewQuitDuringInstallDoesNotWait(t *testing.T) {
	rows, msgs := fixture()
	release := make(chan struct{})
	started := make(chan struct{})
	h := newHarnessWith(t, &fakeStore{rows: rows, msgs: msgs, hits: map[int64][]int{}, meta: map[string]string{}}, func(a *App, s *services) {
		s.install = func(ctx context.Context, r *update.Release) error {
			close(started)
			<-release // e.g. inside replaceExe, after athar.exe -> athar.exe.old
			return nil
		}
	})
	defer close(release)
	h.wait = 50 * time.Millisecond
	h.u.release = &update.Release{Version: "v1.1.0"}
	h.key("f2")
	h.key("u")
	h.key("i") // runs the install command (abandoned by the harness after 50 ms)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("install not started")
	}
	h.key("esc") // notes -> sources
	h.key("esc") // sources -> main
	h.key("esc") // empty query: quit
	if !h.quit {
		t.Fatal("did not quit")
	}
	// What tui.Run does after the program ends: wait for u.bg only.
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { waitBackground(h.u.bg, &buf, time.Second); close(done) }()
	select {
	case <-done:
		t.Errorf("Run would return (and main exit) while Install is still running; installing=%v", h.u.installing)
	case <-time.After(500 * time.Millisecond):
	}
}
