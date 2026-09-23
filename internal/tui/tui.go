// Package tui is Athar's single-screen terminal UI (spec §2): a search box
// over the archive, a result list with a preview, the action menu (resume,
// fork, read, copy, open folder), the Sources & help screen and the update
// notes. Sync runs in the background while the UI is open.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/config"
	"github.com/AbuDergham/Athar/internal/launch"
	"github.com/AbuDergham/Athar/internal/model"
	"github.com/AbuDergham/Athar/internal/registry"
	"github.com/AbuDergham/Athar/internal/render"
	"github.com/AbuDergham/Athar/internal/syncer"
	"github.com/AbuDergham/Athar/internal/update"
)

// App bundles what the UI needs.
type App struct {
	DB       *archive.DB
	Adapters []adapters.Adapter // nil = registry.All()
	Env      adapters.Env
	Machine  string
	Hostname string
	DataDir  string // athar-data folder
	Config   config.Config
	Version  string
	FirstRun bool
	NoUpdate bool
}

// ErrRestart is returned by Run after an update was installed: the caller
// closes the archive and starts the new athar.exe (spec §9).
var ErrRestart = errors.New("tui: update installed, restart Athar")

// Run starts the UI and blocks until the user quits. It waits for a running
// background sync to stop before returning, so the caller may close the DB.
// Empty App fields get defaults (see withDefaults).
func Run(app App) error {
	if app.DB == nil {
		return errors.New("tui: no archive")
	}
	app = withDefaults(app, app.DB.Path())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := newUI(ctx, app, app.DB, realServices(app))
	u.loadStartupMeta()
	_, err := tea.NewProgram(u).Run()
	cancel()
	waitBackground(u.bg, os.Stderr, 300*time.Millisecond)
	if err != nil {
		return err
	}
	return u.quitErr
}

// waitBackground waits for the background sync to stop (it was cancelled and
// stops after its current source). If that takes longer than notice it says
// so, since the terminal is back to its normal screen and would look frozen.
func waitBackground(bg *bgGroup, w io.Writer, notice time.Duration) {
	done := make(chan struct{})
	go func() {
		bg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(notice):
	}
	fmt.Fprintln(w, "Athar: finishing the current sync step…")
	<-done
}

// withDefaults fills empty App fields: every adapter of the registry, the
// current user's Env with a private temp folder for SQLite copies, this PC's
// machine id and host name, and the archive's folder as DataDir (sync.lock).
func withDefaults(app App, archivePath string) App {
	if app.Adapters == nil {
		app.Adapters = registry.All()
	}
	copyDir := filepath.Join(os.TempDir(), "athar", "copy")
	if app.Env.Home == "" {
		app.Env = adapters.CurrentEnv(copyDir)
	}
	if app.Env.TempDir == "" {
		app.Env.TempDir = copyDir
	}
	if app.Env.Getenv == nil {
		app.Env.Getenv = os.Getenv
	}
	if app.Machine == "" {
		app.Machine = syncer.MachineID()
	}
	if app.Hostname == "" {
		app.Hostname, _ = os.Hostname()
	}
	if app.DataDir == "" && archivePath != "" {
		app.DataDir = filepath.Dir(archivePath)
	}
	return app
}

// store is the part of *archive.DB the UI uses (a fake in tests).
type store interface {
	Search(f archive.Filter, limit, offset int) ([]archive.Row, error)
	Messages(sessionID int64) ([]model.Message, error)
	HitSeqs(sessionID int64, query string) ([]int, error)
	Agents() ([]archive.AgentCount, error)
	Sources(machine string) ([]archive.SourceStatus, error)
	Meta(key string) (string, error)
	SetMeta(key, value string) error
	Path() string
}

var _ store = (*archive.DB)(nil)

// services are the side effects the UI triggers (fakes in tests).
type services struct {
	now         func() time.Time
	build       func(t launch.Target, caps adapters.Caps, action launch.Action, skip bool, override []string) (launch.Command, error)
	preflight   func(launch.Command) error
	start       func(launch.Command) error
	copy        func(string) error
	openFolder  func(string) error
	openFile    func(string) error
	live        func(roots []string) map[string]launch.LiveInfo
	render      func(render.Page, string) (string, error)
	readDir     string // where reading pages are written
	sync        func(ctx context.Context, progress func(syncer.Progress)) (syncer.Report, error)
	checkUpdate func(ctx context.Context, current string) (*update.Release, error)
	install     func(ctx context.Context, r *update.Release) error
	dirExists   func(string) bool
	fileSize    func(string) int64
}

func realServices(app App) services {
	return services{
		now:        time.Now,
		build:      launch.Build,
		preflight:  launch.Preflight,
		start:      launch.Start,
		copy:       launch.CopyToClipboard,
		openFolder: launch.OpenFolder,
		openFile:   launch.OpenFile,
		live:       launch.LiveClaudeSessions,
		render:     render.Write,
		readDir:    render.DefaultDir(),
		sync: func(ctx context.Context, progress func(syncer.Progress)) (syncer.Report, error) {
			return syncer.Run(ctx, app.DB, app.Adapters, app.Env,
				syncer.Options{Machine: app.Machine, LockDir: app.DataDir}, progress)
		},
		checkUpdate: update.Check,
		install:     update.Install,
		dirExists: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir()
		},
		fileSize: func(p string) int64 {
			var n int64
			for _, f := range []string{p, p + "-wal"} {
				if fi, err := os.Stat(f); err == nil {
					n += fi.Size()
				}
			}
			return n
		},
	}
}

// bgGroup tracks background goroutines (the sync) so Run can wait for them.
type bgGroup = sync.WaitGroup
