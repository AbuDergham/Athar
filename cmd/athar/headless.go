package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/syncer"
)

// runSync is athar --sync: one sync without the UI (spec §4.3), for a
// scheduled task. It never waits for input. Exit code 0 when every source
// was read or unchanged, or when another Athar window is syncing (it keeps
// the archive fresh); 1 on any error. Errors are appended to
// athar-data\last-sync.txt.
func runSync(opts options, dataDir string, sys *system) int {
	logPath := filepath.Join(dataDir, syncLogName)
	failed := func(err error) int {
		fmt.Fprintln(sys.stderr, err)
		logEntry(logPath, sys, "failed", []string{err.Error()})
		return 1
	}

	db, err := openArchive(filepath.Join(dataDir, archiveName), sys, false)
	if err != nil {
		return failed(err)
	}
	defer db.Close()

	var problems []string
	if err := startBackup(db, dataDir, sys.now()).wait(io.Discard, time.Hour); err != nil {
		problems = append(problems, "weekly backup: "+err.Error())
		fmt.Fprintln(sys.stderr, "Weekly backup failed:", err)
	}

	ctx, stop := sys.signalContext()
	defer stop()
	rep, err := syncOnce(ctx, db, dataDir, sys.machineID(), opts.rescan, sys)
	switch {
	case errors.Is(err, syncer.ErrLocked):
		fmt.Fprintln(sys.stdout, "Another Athar window is syncing; it keeps the archive up to date.")
		return exitFor(problems, logPath, sys)
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(sys.stderr, "Sync interrupted.")
		logEntry(logPath, sys, "interrupted", append(problems, reportErrors(rep)...))
		return 1
	case err != nil:
		return failed(fmt.Errorf("sync: %w", err))
	}
	fmt.Fprintln(sys.stdout, summary(rep))
	for _, e := range rep.Errors {
		fmt.Fprintln(sys.stderr, "  "+e.Error())
	}
	return exitFor(append(problems, reportErrors(rep)...), logPath, sys)
}

// exitFor logs problems (if any) and returns the exit code.
func exitFor(problems []string, logPath string, sys *system) int {
	if len(problems) == 0 {
		return 0
	}
	logEntry(logPath, sys, plural(len(problems), "error"), problems)
	return 1
}

// syncOnce runs one sync with a progress counter on a console.
func syncOnce(ctx context.Context, db *archive.DB, dataDir, machine string, rescan bool, sys *system) (syncer.Report, error) {
	var progress func(syncer.Progress)
	shown := false
	if sys.progressTTY {
		progress = func(p syncer.Progress) {
			if p.Total > 0 {
				fmt.Fprintf(sys.stdout, "\rSyncing %d/%d…", p.Done, p.Total)
				shown = true
			}
		}
	}
	rep, err := sys.sync(ctx, db, syncer.Options{Machine: machine, Rescan: rescan, LockDir: dataDir}, progress)
	if shown {
		fmt.Fprint(sys.stdout, "\r"+strings.Repeat(" ", 30)+"\r")
	}
	return rep, err
}

// summary is the one-line result of a sync.
func summary(r syncer.Report) string {
	var b strings.Builder
	d := r.Duration.Round(100 * time.Millisecond)
	if r.Duration < time.Second {
		d = r.Duration.Round(time.Millisecond)
	}
	fmt.Fprintf(&b, "Synced in %s: %s (%d read, %d unchanged", d, plural(r.Sources, "source"), r.Read, r.Unchanged)
	if r.RetryLater > 0 {
		fmt.Fprintf(&b, ", %d still being written", r.RetryLater)
	}
	fmt.Fprintf(&b, ", %d failed) · %s, %d updated", r.Failed, plural(r.NewSessions, "new session"), r.UpdatedSessions)
	if r.NewlyMissing > 0 {
		fmt.Fprintf(&b, ", %d no longer on disk (kept in the archive)", r.NewlyMissing)
	}
	return b.String()
}

// reportErrors lists the errors of a sync, one line each.
func reportErrors(r syncer.Report) []string {
	out := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		out = append(out, e.Error())
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// logEntry appends one entry to last-sync.txt: a header with the local time,
// the version and what happened, then one indented line per problem.
// Failing to write the log is reported but does not change the exit code.
func logEntry(path string, sys *system, what string, problems []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  athar %s --sync: %s\n", sys.now().Format("2006-01-02 15:04:05 -07:00"), version, what)
	for _, p := range problems {
		b.WriteString("  " + strings.ReplaceAll(p, "\n", "\n  ") + "\n")
	}
	if err := appendLog(path, b.String()); err != nil {
		fmt.Fprintf(sys.stderr, "Cannot write %s: %v\n", path, err)
	}
}

// Size limits of last-sync.txt: when it grows past maxLogSize, only its last
// keepLogSize bytes (from a line start) are kept.
const (
	maxLogSize  = 1 << 20
	keepLogSize = 256 << 10
)

// appendLog appends text to the log at path, trimming it first when too big.
func appendLog(path, text string) error {
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogSize {
		if err := trimLog(path); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// trimLog keeps the last keepLogSize bytes of the log, from a line start.
func trimLog(path string) error {
	data, err := os.ReadFile(path)
	if err != nil || len(data) <= keepLogSize {
		return err
	}
	tail := data[len(data)-keepLogSize:]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, tail, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
