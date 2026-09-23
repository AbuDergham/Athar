package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/config"
	"github.com/AbuDergham/Athar/internal/syncer"
	"github.com/AbuDergham/Athar/internal/tui"
)

// metaCloudWarned holds the data folder for which the cloud-folder warning
// was shown (spec §4.1: shown once per location).
const metaCloudWarned = "cloud_warning_dir"

// backupNotice is how long closing waits silently for the weekly backup
// before saying what it is waiting for.
const backupNotice = 300 * time.Millisecond

// errInterrupted ends athar with exit code 1 after its message was printed.
var errInterrupted = errors.New("interrupted")

// runUI is athar without --sync. After the archive is closed it restarts
// the updated athar.exe (tui.ErrRestart) or reports a failure.
func runUI(opts options, exe, dataDir string, sys *system) int {
	err := openAndRunUI(opts, exe, dataDir, sys)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, tui.ErrRestart):
		fmt.Fprintln(sys.stderr, "Athar was updated. Restarting…")
		flagDir := ""
		if opts.dataDir != "" {
			flagDir = dataDir
		}
		return sys.restart(exe, restartArgs(opts, flagDir))
	case errors.Is(err, errInterrupted):
		return 1
	}
	return sys.fatal(opts, err)
}

// openAndRunUI opens the archive, shows one-time notes, runs --rescan when
// asked, starts the weekly backup in the background and runs the UI. The
// archive is closed (and the backup finished) when it returns.
func openAndRunUI(opts options, exe, dataDir string, sys *system) error {
	cfg, cfgErr := config.Load(filepath.Dir(exe))
	db, err := openArchive(filepath.Join(dataDir, archiveName), sys, sys.console)
	if err != nil {
		return err
	}
	defer db.Close()

	var notes []string
	if cfgErr != nil {
		notes = append(notes, fmt.Sprintf("athar.toml is ignored: %v", cfgErr))
	}
	cloud := cloudFolder(dataDir)
	if cloud != "" {
		if shown, _ := db.Meta(metaCloudWarned); strings.EqualFold(shown, dataDir) {
			cloud = ""
		}
	}
	if cloud != "" {
		notes = append(notes, fmt.Sprintf("Warning: the archive %s is inside your %s folder. "+
			"Syncing a live SQLite file with %s can corrupt it. Consider moving athar.exe to a folder that is not synced, "+
			"or run it with --data-dir <path>. This warning is shown once.", dataDir, cloud, cloud))
	}
	if len(notes) > 0 {
		for _, n := range notes {
			fmt.Fprintln(sys.stderr, n)
		}
		sys.waitEnter("Press Enter to continue.")
		if cloud != "" {
			_ = db.SetMeta(metaCloudWarned, dataDir)
		}
	}

	machine := sys.machineID()
	if opts.rescan {
		if err := rescan(db, dataDir, machine, sys); err != nil {
			return err
		}
	}

	lastSync, _ := db.Meta("last_sync_at")
	backup := startBackup(db, dataDir, sys.now())
	host, _ := sys.hostname()
	err = sys.runTUI(tui.App{
		DB:       db,
		Machine:  machine,
		Hostname: host,
		DataDir:  dataDir,
		Config:   cfg,
		Version:  version,
		FirstRun: lastSync == "",
		NoUpdate: opts.noUpdateCheck,
	})
	if berr := backup.wait(sys.stderr, backupNotice); berr != nil {
		fmt.Fprintln(sys.stderr, "Athar: the weekly backup failed:", berr)
	}
	if err != nil && !errors.Is(err, tui.ErrRestart) {
		return fmt.Errorf("athar: %w", err)
	}
	return err
}

// rescan is --rescan without --sync: re-read every source with a progress
// counter before the UI opens (its own sync then finds nothing changed; the
// UI has no rescan option). Only an interruption (Ctrl+C) stops athar.
func rescan(db *archive.DB, dataDir, machine string, sys *system) error {
	ctx, stop := sys.signalContext()
	defer stop()
	fmt.Fprintln(sys.stdout, "Re-reading every source…")
	rep, err := syncOnce(ctx, db, dataDir, machine, true, sys)
	switch {
	case errors.Is(err, syncer.ErrLocked):
		fmt.Fprintln(sys.stderr, "Another Athar window is syncing; --rescan was skipped.")
		sys.waitEnter("Press Enter to continue.")
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(sys.stderr, "Rescan interrupted.")
		return errInterrupted
	case err != nil:
		fmt.Fprintln(sys.stderr, "Rescan failed:", err)
		sys.waitEnter("Press Enter to continue.")
	default:
		fmt.Fprintln(sys.stdout, summary(rep))
		if rep.Failed > 0 {
			fmt.Fprintln(sys.stdout, "The errors are listed on the Sources screen (F2).")
		}
	}
	return nil
}
