package syncer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// outcome is what happened to one source.
type outcome int

const (
	outRead      outcome = iota // read and applied
	outUnchanged                // fingerprint and adapter version unchanged: skipped
	outRetry                    // being written (adapters.ErrRetryLater): seen, not read
	outFailed                   // error or panic: recorded
	outVanished                 // deleted by its agent after it was listed: not seen
	outCanceled                 // the sync was canceled: nothing applied
)

type result struct {
	out   outcome
	err   error
	apply archive.ApplyReport
}

// source fingerprints, reads and applies one source.
func (r *runner) source(g *group, src adapters.Source) (res result) {
	defer func() {
		if p := recover(); p != nil {
			res = result{out: outFailed, err: fmt.Errorf("crashed: %v", p)}
		}
	}()
	files := src.Files
	if len(files) == 0 {
		files = []string{src.Path}
	}
	// The fingerprint is taken before the read: a write during the read
	// changes it again, so that write is read on the next sync.
	fp, err := fsread.Fingerprint(files)
	if err != nil {
		return result{out: outFailed, err: fmt.Errorf("reading file sizes and times: %w", err)}
	}
	if !r.rescan {
		stored, version, ok, err := r.db.SourceState(r.machine, src.Path)
		if err == nil && ok && stored == fp && version == g.version {
			return result{out: outUnchanged}
		}
	}

	env, cleanup := r.sourceEnv()
	defer cleanup()
	rr, err := read(r.ctx, g.a, env, src)
	switch {
	case r.ctx.Err() != nil:
		// Never apply a read the cancellation may have cut short: its
		// fingerprint would make the missing part look already read.
		return result{out: outCanceled}
	case err == nil:
	case errors.Is(err, adapters.ErrRetryLater):
		return result{out: outRetry}
	case errors.Is(err, fs.ErrNotExist) && gone(src.Path, files):
		return result{out: outVanished}
	default:
		return result{out: outFailed, err: err}
	}

	ap, err := r.db.ApplySource(archive.SourceRecord{
		Machine: r.machine, Adapter: g.id, Path: src.Path,
		Fingerprint: fp, AdapterVersion: g.version,
		Skipped: rr.Skipped, Warnings: rr.Warnings,
	}, rr.Sessions)
	if err != nil {
		return result{out: outFailed, err: fmt.Errorf("saving to the archive: %w", err)}
	}
	return result{out: outRead, apply: ap}
}

// read calls the adapter's Read, turning a panic into an error.
func read(ctx context.Context, a adapters.Adapter, env adapters.Env, src adapters.Source) (rr adapters.ReadResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("adapter crashed: %v", p)
		}
	}()
	return a.Read(ctx, env, src)
}

// gone reports that a source and all its files no longer exist.
func gone(path string, files []string) bool {
	for _, p := range append([]string{path}, files...) {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	return true
}

// record stores a failure in the archive, then counts the outcome, marks the
// source seen and reports progress.
func (r *runner) record(g *group, src adapters.Source, res result) {
	if res.out == outFailed {
		// Shown on the Sources screen. The last good fingerprint is kept,
		// so the source is read again on the next sync.
		if err := r.db.RecordSourceError(r.machine, g.id, src.Path, res.err); err != nil {
			res.err = fmt.Errorf("%w (the error could not be saved: %v)", res.err, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch res.out {
	case outRead:
		r.rep.Read++
		r.rep.NewSessions += res.apply.Inserted
		r.rep.UpdatedSessions += res.apply.Updated
	case outUnchanged:
		r.rep.Unchanged++
	case outRetry:
		r.rep.RetryLater++
	case outFailed:
		r.rep.Failed++
		r.rep.Errors = append(r.rep.Errors, SourceError{Adapter: g.id, Path: src.Path, Err: res.err})
	case outCanceled:
		return
	}
	if res.out != outVanished {
		r.seen[src.Path] = true
	}
	r.done++
	r.report(Progress{Done: r.done, Total: r.total, Adapter: g.id, Path: src.Path})
}
