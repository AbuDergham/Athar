// Package syncer runs adapters and applies their results to the archive (spec §4.3).
//
// One run: take the sync lock, list the sources of every adapter, then read
// the sources of one adapter after another (up to 4 at a time).
// A source whose fingerprint and adapter version are unchanged is skipped;
// any other is read in full and applied in its own archive transaction.
// When every source was visited, FinishSync marks the sessions whose sources
// disappeared (spec §4.4) and meta last_sync_at is set.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
)

// ErrLocked is returned when another process holds the sync lock.
var ErrLocked = errors.New("another Athar window is syncing")

// lockName is the lock file created in Options.LockDir.
const lockName = "sync.lock"

// metaLastSync is the meta key holding the end of the last complete sync (RFC 3339, UTC).
const metaLastSync = "last_sync_at"

// parallel bounds the sources of one adapter read at the same time.
const parallel = 4

// Options control one sync run.
type Options struct {
	Machine string // machine id (MachineGuid); "" = MachineID()
	Rescan  bool   // ignore stored fingerprints
	LockDir string // folder for sync.lock (athar-data); "" = the archive's folder
}

// Progress is reported once when the sources are listed (Done = 0, no
// Adapter/Path) and then after each source. The callback is never called
// concurrently with itself.
type Progress struct {
	Done, Total int
	Adapter     string // ID of the adapter of the source just done
	Path        string // path of the source just done
}

// Report summarizes a run.
type Report struct {
	// Sources is the number of distinct sources listed. Each ends up in
	// Read, Unchanged, Failed or RetryLater, except a source its agent
	// deleted while the sync ran (it is simply gone). Failed also counts
	// adapters that could not list their sources.
	Sources, Read, Unchanged, Failed int
	// RetryLater counts sources still being written (adapters.ErrRetryLater):
	// seen but not read, no error recorded, read again on the next sync.
	RetryLater   int
	NewlyMissing int
	// NewSessions and UpdatedSessions count archive sessions inserted and
	// changed by the sources that were read.
	NewSessions, UpdatedSessions int
	Duration                     time.Duration
	// Errors lists every failure of the run, in the order they happened.
	// Source failures are also stored in the archive (Sources screen).
	Errors []SourceError
}

// SourceError is one failure of a run.
type SourceError struct {
	Adapter string
	Path    string // "" when the adapter could not list its sources
	Err     error
}

// Error names the adapter and the source.
func (e SourceError) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("%s: listing sources: %v", e.Adapter, e.Err)
	}
	return fmt.Sprintf("%s: %s: %v", e.Adapter, e.Path, e.Err)
}

// Unwrap returns the underlying error.
func (e SourceError) Unwrap() error { return e.Err }

// Run syncs every source of every adapter once. Sources whose fingerprint and
// adapter version are unchanged are skipped. Each source is applied in its own
// transaction; a panic or error in one adapter is recorded and does not stop others.
// At the end FinishSync marks sessions whose sources disappeared.
//
// Details:
//   - Only one Run at a time holds the lock (dir\sync.lock); another gets ErrLocked.
//   - A source whose Read returns an error wrapping adapters.ErrRetryLater is
//     seen but not read: no fingerprint is stored and no error recorded.
//   - Any other failure is stored with RecordSourceError (the last good
//     fingerprint is kept, so the source is read again next time) and listed
//     in Report.Errors; Run itself still returns nil.
//   - An adapter whose Sources fails keeps its known sources "seen", so its
//     sessions are not marked missing.
//   - Each source gets a fresh folder under env.TempDir ("" = %TEMP%\athar\copy)
//     for its SQLite copies, removed after the source is applied.
//   - When ctx is canceled, Run stops starting sources, applies nothing more
//     and returns ctx.Err() without FinishSync or last_sync_at: an
//     interrupted sync has not seen every source.
func Run(ctx context.Context, db *archive.DB, list []adapters.Adapter, env adapters.Env, opts Options, progress func(Progress)) (Report, error) {
	start := time.Now()
	if db == nil {
		return Report{}, errors.New("sync: no archive")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	lockDir := opts.LockDir
	if lockDir == "" {
		lockDir = filepath.Dir(db.Path())
	}
	unlock, err := lock(lockDir)
	if err != nil {
		return Report{}, err
	}
	defer unlock()

	machine := opts.Machine
	if machine == "" {
		machine = MachineID()
	}
	if env.TempDir == "" {
		env.TempDir = defaultTempDir()
	}
	prepareTemp(env.TempDir)

	r := &runner{ctx: ctx, db: db, env: env, rescan: opts.Rescan, machine: machine,
		progress: progress, seen: map[string]bool{}}
	groups := r.list(list)
	r.report(Progress{Total: r.total})
	for _, g := range groups {
		if ctx.Err() != nil {
			break
		}
		r.runGroup(g)
	}

	rep := r.rep
	if err := ctx.Err(); err != nil {
		rep.Duration = time.Since(start)
		return rep, err
	}
	if !r.skipFinish {
		n, err := db.FinishSync(machine, r.seen, time.Now())
		if err != nil {
			rep.Duration = time.Since(start)
			return rep, fmt.Errorf("sync: marking deleted sources: %w", err)
		}
		rep.NewlyMissing = n
	}
	if err := db.SetMeta(metaLastSync, time.Now().UTC().Format(time.RFC3339)); err != nil {
		rep.Duration = time.Since(start)
		return rep, fmt.Errorf("sync: %w", err)
	}
	rep.Duration = time.Since(start)
	return rep, nil
}

// runner is the state of one Run.
type runner struct {
	ctx      context.Context
	db       *archive.DB
	env      adapters.Env
	rescan   bool
	machine  string
	progress func(Progress)

	total      int
	skipFinish bool // a listing failure could not be compensated: do not mark anything missing

	mu   sync.Mutex // guards rep, seen, done and serializes progress
	rep  Report
	seen map[string]bool // source paths present in this sync (FinishSync)
	done int
}

// group is one adapter and the sources it listed.
type group struct {
	a       adapters.Adapter
	id      string
	version int
	sources []adapters.Source
}

// list asks every adapter for its sources. A path listed twice (by the same
// or another adapter) is read once, by the first. A listing failure is
// recorded and the adapter's known sources count as seen.
func (r *runner) list(list []adapters.Adapter) []*group {
	var groups []*group
	listed := map[string]bool{}
	for _, a := range list {
		g := &group{a: a}
		srcs, err := g.listSources(r.env)
		if err != nil {
			r.fail(g.id, "", err)
			r.keepKnown(g.id)
		}
		for _, src := range srcs {
			switch {
			case src.Path == "":
				r.fail(g.id, "", errors.New("a source without a path was ignored"))
			case !listed[src.Path]:
				listed[src.Path] = true
				g.sources = append(g.sources, src)
			}
		}
		r.total += len(g.sources)
		groups = append(groups, g)
	}
	r.rep.Sources = r.total
	return groups
}

// listSources calls the adapter's ID, Version and Sources, turning a panic into an error.
func (g *group) listSources(env adapters.Env) (srcs []adapters.Source, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("adapter crashed: %v", p)
		}
	}()
	g.id = g.a.ID()
	g.version = g.a.Version()
	return g.a.Sources(env)
}

// keepKnown marks the archived, present sources of an adapter as seen after
// its listing failed, so FinishSync does not take the failure for deletions.
func (r *runner) keepKnown(adapterID string) {
	if adapterID == "" { // ID() itself crashed: its sources are unknown
		r.skipFinish = true
		return
	}
	paths, err := r.db.SourcePaths(r.machine, adapterID)
	if err != nil {
		r.skipFinish = true
		return
	}
	for _, p := range paths {
		r.seen[p] = true
	}
}

// runGroup processes the sources of one adapter, up to parallel at a time.
func (r *runner) runGroup(g *group) {
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
loop:
	for _, src := range g.sources {
		select {
		case sem <- struct{}{}:
		case <-r.ctx.Done():
			break loop
		}
		if r.ctx.Err() != nil { // both were ready and select picked the semaphore
			<-sem
			break loop
		}
		wg.Add(1)
		go func(src adapters.Source) {
			defer wg.Done()
			defer func() { <-sem }()
			r.record(g, src, r.source(g, src))
		}(src)
	}
	wg.Wait()
}

// fail adds a failure to the report.
func (r *runner) fail(adapterID, path string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rep.Failed++
	r.rep.Errors = append(r.rep.Errors, SourceError{Adapter: adapterID, Path: path, Err: err})
}

// report calls the progress callback, one call at a time.
func (r *runner) report(p Progress) {
	if r.progress != nil {
		r.progress(p)
	}
}
