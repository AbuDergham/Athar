package syncer

import (
	"os"
	"path/filepath"
	"testing"
)

// A source moved away (e.g. deleted to the Recycle Bin) is correctly marked
// missing; when it comes back byte-for-byte identical (Recycle Bin restore
// keeps size and mtime, a removable drive is plugged back in) the sessions
// it holds should no longer be "missing".
func TestReviewRestoredSourceUnmarksSessions(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	a := f.write(t, "a", "hello", "world")

	wantCounts(t, "first", h.run(t, f), counts{Sources: 1, Read: 1})
	if h.rows(t)["a"].Missing {
		t.Fatal("missing after first sync")
	}

	// Move the file away (Recycle Bin) and sync.
	away := filepath.Join(t.TempDir(), "a.txt")
	if err := os.Rename(a, away); err != nil {
		t.Fatal(err)
	}
	wantCounts(t, "deleted", h.run(t, f), counts{Sources: 0, NewlyMissing: 1})
	if !h.rows(t)["a"].Missing {
		t.Fatal("not missing after delete")
	}

	// Restore it: same bytes, same mtime.
	if err := os.Rename(away, a); err != nil {
		t.Fatal(err)
	}
	rep := h.run(t, f)
	t.Logf("restored: %+v", countsOf(rep))
	if _, ok := h.sources(t)[a]; !ok {
		t.Errorf("source not listed after restore")
	}
	if h.rows(t)["a"].Missing {
		t.Errorf("BUG: session still marked missing although its source is back and was seen (report %+v)", countsOf(rep))
	}
	// And it stays so.
	h.run(t, f)
	if h.rows(t)["a"].Missing {
		t.Errorf("BUG: still missing after another sync")
	}
}

// Listing failure compensation: an adapter whose root is temporarily gone
// (returns no sources, no error) marks everything missing; when the root is back
// nothing changed on disk, yet the sessions stay missing.
func TestReviewRootTemporarilyUnavailable(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	f.write(t, "a", "hello")
	f.write(t, "b", "world")
	h.run(t, f)
	orig := f.dir
	f.mu.Lock()
	f.dir = filepath.Join(t.TempDir(), "unplugged")
	f.mu.Unlock()
	wantCounts(t, "unplugged", h.run(t, f), counts{NewlyMissing: 2})
	f.mu.Lock()
	f.dir = orig
	f.mu.Unlock()
	rep := h.run(t, f)
	t.Logf("plugged back: %+v", countsOf(rep))
	for id, r := range h.rows(t) {
		if r.Missing {
			t.Errorf("BUG: session %s still missing after its drive is back", id)
		}
	}
}
