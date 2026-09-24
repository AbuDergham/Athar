package syncer

import (
	"context"
	"errors"
	"testing"

	"github.com/AbuDergham/Athar/internal/adapters"
)

// A transient failure during --rescan (file unchanged) is never retried:
// the next syncs skip the source as unchanged and the error stays on the
// Sources screen.
func TestReviewRescanFailureNeverRetried(t *testing.T) {
	h := newHarness(t)
	f := newFake(t, "fake")
	a := f.write(t, "a", "hello")
	h.run(t, f)

	f.setHook(func(context.Context, adapters.Env, adapters.Source) error {
		return errors.New("The process cannot access the file because it is being used by another process.")
	})
	h.opts.Rescan = true
	wantCounts(t, "rescan", h.run(t, f), counts{Sources: 1, Failed: 1})
	f.setHook(nil)
	h.opts.Rescan = false
	rep := h.run(t, f)
	t.Logf("next sync: %+v, reads of a: %d", countsOf(rep), f.readCount(a))
	if e := h.sources(t)[a].LastError; e != "" {
		t.Errorf("BUG: the transient error stays on the Sources screen after a sync that did not retry the source: %q", e)
	}
}
