package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
)

func TestRunReadsOnceThenSkipsUnchanged(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	a := f.write(t, "a", "hello", "world")
	b := f.write(t, "b", "one")

	wantCounts(t, "first run", h.run(t, f), counts{Sources: 2, Read: 2})
	rows := h.rows(t)
	if len(rows) != 2 || rows["a"].MessageCount != 2 || rows["b"].MessageCount != 1 {
		t.Fatalf("archived rows %+v", rows)
	}

	wantCounts(t, "second run", h.run(t, f), counts{Sources: 2, Unchanged: 2})
	if f.readCount(a) != 1 || f.readCount(b) != 1 {
		t.Errorf("unchanged sources were read again: a=%d b=%d", f.readCount(a), f.readCount(b))
	}

	f.write(t, "a", "hello", "world", "again")
	wantCounts(t, "after a change", h.run(t, f), counts{Sources: 2, Read: 1, Unchanged: 1})
	if got := h.rows(t)["a"].MessageCount; got != 3 {
		t.Errorf("changed source: %d messages archived, want 3", got)
	}
	if f.readCount(a) != 2 || f.readCount(b) != 1 {
		t.Errorf("reads a=%d b=%d, want 2 and 1", f.readCount(a), f.readCount(b))
	}
}

func TestRunRereadsAfterVersionBumpOrRescan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(f *fake, o *Options)
	}{
		{"version bump", func(f *fake, o *Options) { f.version = 2 }},
		{"rescan", func(f *fake, o *Options) { o.Rescan = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			f := newFake(t, "fake")
			f.write(t, "a", "hello")
			f.write(t, "b", "world")
			h.run(t, f)
			tc.change(f, &h.opts)
			wantCounts(t, "after "+tc.name, h.run(t, f), counts{Sources: 2, Read: 2})
			h.opts.Rescan = false
			wantCounts(t, "next run", h.run(t, f), counts{Sources: 2, Unchanged: 2})
		})
	}
}

func TestRunIsolatesFailingSources(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hook    func() error
		wantErr string
	}{
		{"panic", func() error { panic("boom") }, "boom"},
		{"error", func() error { return errors.New("bad record header") }, "bad record header"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			f1, f2 := newFake(t, "one"), newFake(t, "two")
			bad := f1.write(t, "bad", "x")
			f1.write(t, "good", "y")
			f2.write(t, "other", "z")
			f1.setHook(func(_ context.Context, _ adapters.Env, src adapters.Source) error {
				if src.Path == bad {
					return tc.hook()
				}
				return nil
			})

			rep := h.run(t, f1, f2)
			wantCounts(t, "first run", rep, counts{Sources: 3, Read: 2, Failed: 1})
			if len(rep.Errors) != 1 || rep.Errors[0].Path != bad || rep.Errors[0].Adapter != "one" ||
				!strings.Contains(rep.Errors[0].Error(), tc.wantErr) {
				t.Errorf("report errors %v", rep.Errors)
			}
			if st := h.sources(t)[bad]; !strings.Contains(st.LastError, tc.wantErr) {
				t.Errorf("stored error %q, want it to mention %q", st.LastError, tc.wantErr)
			}
			if rows := h.rows(t); len(rows) != 2 {
				t.Errorf("%d sessions archived, want the 2 good ones", len(rows))
			}

			// A failed source is read again next time (its fingerprint was
			// not stored); once it works its error is cleared.
			f1.setHook(nil)
			wantCounts(t, "second run", h.run(t, f1, f2), counts{Sources: 3, Read: 1, Unchanged: 2})
			if st := h.sources(t)[bad]; st.LastError != "" {
				t.Errorf("error not cleared after a good read: %q", st.LastError)
			}
		})
	}
}

func TestRunRetryLaterIsSeenButUnread(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	a := f.write(t, "a", "first")
	f.write(t, "b", "second")
	retry := func(_ context.Context, _ adapters.Env, src adapters.Source) error {
		if src.Path == a {
			return fmt.Errorf("a.json changed 1s ago: %w", adapters.ErrRetryLater)
		}
		return nil
	}
	f.setHook(retry)

	rep := h.run(t, f)
	wantCounts(t, "new source being written", rep, counts{Sources: 2, Read: 1, RetryLater: 1})
	if len(rep.Errors) != 0 {
		t.Errorf("retry-later reported as an error: %v", rep.Errors)
	}
	if st, ok := h.sources(t)[a]; ok && st.LastError != "" {
		t.Errorf("retry-later stored as an error: %q", st.LastError)
	}
	if _, ok := h.rows(t)["a"]; ok {
		t.Error("a source that was not read has sessions")
	}

	wantCounts(t, "still being written", h.run(t, f), counts{Sources: 2, Unchanged: 1, RetryLater: 1})
	f.setHook(nil)
	wantCounts(t, "written", h.run(t, f), counts{Sources: 2, Read: 1, Unchanged: 1})

	// A known source that changes and is being written again: its sessions
	// stay present, no error, and the change is read on the next sync
	// (the fingerprint of the unread change was not stored).
	f.write(t, "a", "first", "more")
	f.setHook(retry)
	rep = h.run(t, f)
	wantCounts(t, "known source being written", rep, counts{Sources: 2, Unchanged: 1, RetryLater: 1})
	row := h.rows(t)["a"]
	if row.Missing || row.MessageCount != 1 {
		t.Errorf("row during retry: missing=%v messages=%d, want present with 1", row.Missing, row.MessageCount)
	}
	if st := h.sources(t)[a]; st.LastError != "" {
		t.Errorf("retry-later stored as an error: %q", st.LastError)
	}
	f.setHook(nil)
	wantCounts(t, "change read", h.run(t, f), counts{Sources: 2, Read: 1, Unchanged: 1})
	if got := h.rows(t)["a"].MessageCount; got != 2 {
		t.Errorf("%d messages after the retry, want 2", got)
	}
}

func TestRunMarksRemovedSourcesMissing(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	f.write(t, "a", "kept")
	b := f.write(t, "b", "deleted by the agent")
	h.run(t, f)

	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	wantCounts(t, "after delete", h.run(t, f), counts{Sources: 1, Unchanged: 1, NewlyMissing: 1})
	rows := h.rows(t)
	if !rows["b"].Missing || rows["a"].Missing {
		t.Errorf("missing flags a=%v b=%v, want false, true", rows["a"].Missing, rows["b"].Missing)
	}
	if rows["b"].MessageCount != 1 {
		t.Errorf("archived copy of b lost: %d messages", rows["b"].MessageCount)
	}
	wantCounts(t, "next run", h.run(t, f), counts{Sources: 1, Unchanged: 1})
}

func TestRunListingFailureKeepsSessions(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	f.write(t, "a", "one")
	f.write(t, "b", "two")
	h.run(t, f)

	f.listErr = errors.New("access denied")
	rep := h.run(t, f)
	wantCounts(t, "listing failed", rep, counts{Failed: 1})
	if len(rep.Errors) != 1 || rep.Errors[0].Path != "" || !strings.Contains(rep.Errors[0].Error(), "access denied") {
		t.Errorf("report errors %v", rep.Errors)
	}
	for id, r := range h.rows(t) {
		if r.Missing {
			t.Errorf("session %s marked missing after a listing failure", id)
		}
	}
}

// A source listed but deleted by its agent before it is read is not an
// error, and it is not "seen": its sessions are marked missing by that sync.
func TestRunVanishedSourceIsNotAnError(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	f.write(t, "a", "one")
	f.extra = []string{filepath.Join(f.dir, "never.txt")}
	rep := h.run(t, f)
	wantCounts(t, "new source vanished", rep, counts{Sources: 2, Read: 1})
	if len(rep.Errors) != 0 {
		t.Errorf("a source deleted during the sync was reported: %v", rep.Errors)
	}

	gone := f.write(t, "gone", "two")
	f.extra = nil
	h.run(t, f)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	f.extra = []string{gone} // still listed, deleted before it is read
	rep = h.run(t, f)
	wantCounts(t, "known source vanished", rep, counts{Sources: 2, Unchanged: 1, NewlyMissing: 1})
	if len(rep.Errors) != 0 {
		t.Errorf("a source deleted during the sync was reported: %v", rep.Errors)
	}
	if !h.rows(t)["gone"].Missing {
		t.Error("the session of a vanished source is not marked missing")
	}
}

func TestRunDuplicateSourcePathsAreReadOnce(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	a := f.write(t, "a", "one")
	f.extra = []string{a}
	wantCounts(t, "run", h.run(t, f), counts{Sources: 1, Read: 1})
	if n := f.readCount(a); n != 1 {
		t.Errorf("a duplicate source was read %d times", n)
	}
}

func TestRunProgress(t *testing.T) {
	h := newHarness(t)
	f1, f2 := newFake(t, "one"), newFake(t, "two")
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		f1.write(t, n, n)
	}
	f2.write(t, "z", "z")

	var mu sync.Mutex
	var got []Progress
	_, err := Run(context.Background(), h.db, []adapters.Adapter{f1, f2}, h.env, h.opts, func(p Progress) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("%d progress calls, want 7 (one after listing, one per source): %+v", len(got), got)
	}
	if got[0].Done != 0 || got[0].Total != 6 {
		t.Errorf("first progress %+v, want 0/6", got[0])
	}
	for i, p := range got[1:] {
		if p.Done != i+1 || p.Total != 6 || p.Adapter == "" || p.Path == "" {
			t.Errorf("progress %d: %+v", i+1, p)
		}
	}
	if last := got[len(got)-1]; last.Adapter != "two" {
		t.Errorf("adapters must run in order; last progress %+v", last)
	}
}

func TestRunGivesEachSourceItsOwnTempDir(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	for _, n := range []string{"a", "b", "c"} {
		f.write(t, n, n)
	}
	var mu sync.Mutex
	dirs := map[string]bool{}
	f.setHook(func(_ context.Context, env adapters.Env, _ adapters.Source) error {
		if rel, err := filepath.Rel(h.env.TempDir, env.TempDir); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("temp dir %s is not a folder under %s", env.TempDir, h.env.TempDir)
		}
		if err := os.WriteFile(filepath.Join(env.TempDir, "copy.db"), []byte("x"), 0o600); err != nil {
			return err
		}
		mu.Lock()
		dirs[env.TempDir] = true
		mu.Unlock()
		return nil
	})
	wantCounts(t, "run", h.run(t, f), counts{Sources: 3, Read: 3})
	if len(dirs) != 3 {
		t.Errorf("%d distinct temp dirs for 3 sources", len(dirs))
	}
	for d := range dirs {
		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("temp dir %s left behind (%v)", d, err)
		}
	}
	if _, err := os.Stat(h.env.TempDir); err != nil {
		t.Errorf("base temp dir: %v", err)
	}
}

func TestRunRemovesStaleTempDirs(t *testing.T) {
	h := newHarness(t)
	old := filepath.Join(h.env.TempDir, tempPrefix+"1234")
	recent := filepath.Join(h.env.TempDir, tempPrefix+"5678")
	other := filepath.Join(h.env.TempDir, "notes")
	for _, d := range []string{old, recent, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, past, past); err != nil {
		t.Fatal(err)
	}
	h.run(t, newFake(t, "fake"))
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale temp dir of a crashed sync kept (%v)", err)
	}
	for _, d := range []string{recent, other} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s removed: %v", d, err)
		}
	}
}

func TestRunSetsLastSync(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	f.write(t, "a", "one")
	before := time.Now().Add(-time.Second)
	rep := h.run(t, f)
	v, err := h.db.Meta("last_sync_at")
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339, v)
	if err != nil || at.Before(before.Truncate(time.Second)) || at.After(time.Now().Add(time.Second)) {
		t.Errorf("last_sync_at = %q (%v)", v, err)
	}
	if rep.Duration <= 0 {
		t.Errorf("duration %v", rep.Duration)
	}
}

func TestRunCanceled(t *testing.T) {
	t.Run("before start", func(t *testing.T) {
		h := newHarness(t)
		f := newFake(t, "fake")
		f.write(t, "a", "one")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Run(ctx, h.db, []adapters.Adapter{f}, h.env, h.opts, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if v, _ := h.db.Meta("last_sync_at"); v != "" {
			t.Errorf("a canceled sync set last_sync_at %q", v)
		}
	})

	t.Run("during a read", func(t *testing.T) {
		h := newHarness(t)
		f := newFake(t, "fake")
		a := f.write(t, "a", "one")
		b := f.write(t, "b", "two")
		h.run(t, f)
		if err := os.Remove(b); err != nil {
			t.Fatal(err)
		}
		f.write(t, "a", "one", "changed")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.setHook(func(ctx context.Context, _ adapters.Env, src adapters.Source) error {
			cancel()
			return ctx.Err()
		})
		rep, err := Run(ctx, h.db, []adapters.Adapter{f}, h.env, h.opts, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if rep.Failed != 0 || h.sources(t)[a].LastError != "" {
			t.Errorf("cancellation recorded as a source failure: %+v, %q", rep, h.sources(t)[a].LastError)
		}
		// An interrupted sync has not seen every source: nothing is marked missing.
		if h.rows(t)["b"].Missing {
			t.Error("a canceled sync marked a session missing")
		}
	})
}

func TestRunLocked(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	f.write(t, "a", "one")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.setHook(func(context.Context, adapters.Env, adapters.Source) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), h.db, []adapters.Adapter{f}, h.env, h.opts, nil)
		done <- err
	}()
	<-entered
	if _, err := Run(context.Background(), h.db, []adapters.Adapter{f}, h.env, h.opts, nil); !errors.Is(err, ErrLocked) {
		t.Errorf("concurrent Run: err = %v, want ErrLocked", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first Run: %v", err)
	}
	f.setHook(nil)
	if _, err := Run(context.Background(), h.db, []adapters.Adapter{f}, h.env, h.opts, nil); err != nil {
		t.Errorf("Run after the lock was released: %v", err)
	}
}

func TestRunDefaults(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	f.write(t, "a", "one")
	h.opts = Options{} // machine from MachineID, lock next to the archive
	rep := h.run(t, f)
	wantCounts(t, "run", rep, counts{Sources: 1, Read: 1})
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.db.Path()), lockName)); err != nil {
		t.Errorf("lock file not next to the archive: %v", err)
	}
	if _, ok := h.sourcesOf(t, MachineID())[filepath.Join(f.dir, "a.txt")]; !ok {
		t.Error("source not recorded under MachineID()")
	}
	if _, err := Run(context.Background(), nil, nil, h.env, h.opts, nil); err == nil {
		t.Error("Run without an archive succeeded")
	}
}

func (h *harness) sourcesOf(t *testing.T, machine string) map[string]bool {
	t.Helper()
	list, err := h.db.Sources(machine)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, s := range list {
		out[s.Path] = true
	}
	return out
}
