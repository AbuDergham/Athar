package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/syncer"
	"github.com/AbuDergham/Athar/internal/tui"
)

// syncBuffer is a bytes.Buffer safe for the concurrent writes of a test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// failReader fails the test when athar reads its input.
type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Helper()
	f.t.Error("athar read its input, but it must not ask anything here")
	return 0, errors.New("no input")
}

// testSystem returns a fake system: athar.exe in a fresh folder outside the
// (fake) temporary folders, no console, input from stdin, all output (stdout
// and stderr) into the returned buffer, and no real sync, UI or restart.
func testSystem(t *testing.T, stdin string) (*system, *syncBuffer) {
	t.Helper()
	out := &syncBuffer{}
	exeDir := filepath.Join(t.TempDir(), "Tools", "Athar")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sys := &system{
		stdin:      strings.NewReader(stdin),
		stdout:     out,
		stderr:     out,
		executable: func() (string, error) { return filepath.Join(exeDir, "athar.exe"), nil },
		getenv:     func(string) string { return "" },
		tempDir:    func() string { return `Z:\athar-test-temp` },
		now:        time.Now,
		machineID:  func() string { return "machine-1" },
		hostname:   func() (string, error) { return "host-1", nil },
		cleanupOld: func() {},
		runTUI: func(tui.App) error {
			t.Error("the UI was started")
			return nil
		},
		sync: func(context.Context, *archive.DB, syncer.Options, func(syncer.Progress)) (syncer.Report, error) {
			t.Error("a sync was started")
			return syncer.Report{}, nil
		},
		restart: func(string, []string) int {
			t.Error("athar restarted")
			return 0
		},
		signalContext: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
	}
	return sys, out
}

func exeDirOf(t *testing.T, sys *system) string {
	t.Helper()
	exe, err := sys.executable()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(exe)
}

// setMeta writes meta values into the archive at path (creating it).
func setMeta(t *testing.T, path string, kv ...string) {
	t.Helper()
	db, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i+1 < len(kv); i += 2 {
		if err := db.SetMeta(kv[i], kv[i+1]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunVersion(t *testing.T) {
	sys, out := testSystem(t, "")
	sys.executable = func() (string, error) { t.Error("--version located the exe"); return "", nil }
	sys.cleanupOld = func() { t.Error("--version cleaned up") }
	if code := run([]string{"--version"}, sys); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if got := out.String(); got != "athar "+version+"\n" {
		t.Fatalf("--version printed %q", got)
	}
}

func TestRunBadFlag(t *testing.T) {
	sys, out := testSystem(t, "")
	if code := run([]string{"--frobnicate"}, sys); code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if !strings.Contains(out.String(), "--data-dir") {
		t.Errorf("no usage after a bad flag:\n%s", out)
	}
	sys, _ = testSystem(t, "")
	if code := run([]string{"--help"}, sys); code != 0 {
		t.Fatalf("--help exit code %d, want 0", code)
	}
}

func TestRunRefusesTempFolder(t *testing.T) {
	for _, console := range []bool{true, false} {
		sys, out := testSystem(t, "\n")
		sys.console = console
		temp := filepath.Dir(filepath.Dir(exeDirOf(t, sys)))
		sys.getenv = func(k string) string {
			if k == "LOCALAPPDATA" {
				return filepath.Dir(temp)
			}
			return ""
		}
		sys.tempDir = func() string { return temp }
		cleaned := false
		sys.cleanupOld = func() { cleaned = true }

		if code := run(nil, sys); code != 1 {
			t.Fatalf("console=%v: exit code %d, want 1", console, code)
		}
		msg := out.String()
		if !strings.Contains(msg, "Athar keeps its archive next to athar.exe") {
			t.Errorf("console=%v: no refusal message:\n%s", console, msg)
		}
		if got := strings.Contains(msg, "Press Enter to close"); got != console {
			t.Errorf("console=%v: waits for Enter = %v:\n%s", console, got, msg)
		}
		if !cleaned {
			t.Errorf("update.CleanupOld was not called")
		}
		if _, err := os.Stat(filepath.Join(exeDirOf(t, sys), dataDirName)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("athar-data created in a temporary folder")
		}
	}
}

func TestRunSyncRefusalDoesNotWait(t *testing.T) {
	sys, out := testSystem(t, "")
	sys.console = true
	sys.stdin = failReader{t}
	sys.tempDir = func() string { return filepath.Dir(exeDirOf(t, sys)) }
	if code := run([]string{"--sync"}, sys); code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	if strings.Contains(out.String(), "Press Enter") {
		t.Errorf("--sync waited for a key:\n%s", out)
	}
}

func TestRunSyncOK(t *testing.T) {
	sys, out := testSystem(t, "")
	var got syncer.Options
	sys.sync = func(_ context.Context, db *archive.DB, opts syncer.Options, progress func(syncer.Progress)) (syncer.Report, error) {
		got = opts
		if progress != nil { // nil is allowed; a console gets a counter
			progress(syncer.Progress{Total: 2})
			progress(syncer.Progress{Done: 1, Total: 2, Adapter: "claude", Path: "a"})
		}
		return syncer.Report{Sources: 2, Read: 1, Unchanged: 1, NewSessions: 3}, nil
	}
	if code := run([]string{"--sync", "--rescan"}, sys); code != 0 {
		t.Fatalf("exit code %d, want 0:\n%s", code, out)
	}
	dataDir := filepath.Join(exeDirOf(t, sys), dataDirName)
	if !got.Rescan || got.LockDir != dataDir || got.Machine != "machine-1" {
		t.Errorf("sync options = %+v", got)
	}
	if !strings.Contains(out.String(), "2 sources") || !strings.Contains(out.String(), "3 new sessions") {
		t.Errorf("no summary:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dataDir, archiveName)); err != nil {
		t.Errorf("archive not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, syncLogName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s written after a sync without errors", syncLogName)
	}
}

func TestRunSyncErrorsAreLogged(t *testing.T) {
	sys, out := testSystem(t, "")
	sys.sync = func(context.Context, *archive.DB, syncer.Options, func(syncer.Progress)) (syncer.Report, error) {
		return syncer.Report{Sources: 3, Read: 1, Failed: 2, Errors: []syncer.SourceError{
			{Adapter: "codex", Path: `C:\fake\rollout.jsonl`, Err: errors.New("unexpected end of JSON input")},
			{Adapter: "zed", Err: errors.New("access denied")},
		}}, nil
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	for i := 1; i <= 2; i++ {
		if code := run([]string{"--sync", "--data-dir", dataDir}, sys); code != 1 {
			t.Fatalf("run %d: exit code %d, want 1", i, code)
		}
	}
	log, err := os.ReadFile(filepath.Join(dataDir, syncLogName))
	if err != nil {
		t.Fatal(err)
	}
	text := string(log)
	for _, want := range []string{`codex: C:\fake\rollout.jsonl: unexpected end of JSON input`, "zed: listing sources: access denied", "2 errors"} {
		if strings.Count(text, want) != 2 {
			t.Errorf("%s should contain %q once per run:\n%s", syncLogName, want, text)
		}
	}
	if !strings.Contains(out.String(), "unexpected end of JSON input") {
		t.Errorf("errors not printed:\n%s", out)
	}
}

func TestRunSyncLocked(t *testing.T) {
	sys, out := testSystem(t, "")
	sys.sync = func(context.Context, *archive.DB, syncer.Options, func(syncer.Progress)) (syncer.Report, error) {
		return syncer.Report{}, syncer.ErrLocked
	}
	if code := run([]string{"--sync"}, sys); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if !strings.Contains(out.String(), "Another Athar window is syncing") {
		t.Errorf("no locked message:\n%s", out)
	}
}

func TestRunSyncInterrupted(t *testing.T) {
	sys, out := testSystem(t, "")
	sys.sync = func(ctx context.Context, _ *archive.DB, _ syncer.Options, _ func(syncer.Progress)) (syncer.Report, error) {
		return syncer.Report{}, context.Canceled
	}
	if code := run([]string{"--sync"}, sys); code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	log, _ := os.ReadFile(filepath.Join(exeDirOf(t, sys), dataDirName, syncLogName))
	if !strings.Contains(string(log), "interrupted") {
		t.Errorf("%s = %q, want an interruption note (output %s)", syncLogName, log, out)
	}
}

func TestRunSyncDamagedArchive(t *testing.T) {
	sys, out := testSystem(t, "")
	sys.console = true
	sys.stdin = failReader{t} // --sync never asks
	dataDir := filepath.Join(exeDirOf(t, sys), dataDirName)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, archiveName), bytes.Repeat([]byte("junk "), 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--sync"}, sys); code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	log, _ := os.ReadFile(filepath.Join(dataDir, syncLogName))
	if !strings.Contains(string(log), "damaged") {
		t.Errorf("%s = %q, want the damaged-archive error (output %s)", syncLogName, log, out)
	}
}

func TestRunSyncWeeklyBackup(t *testing.T) {
	sys, _ := testSystem(t, "")
	sys.sync = func(context.Context, *archive.DB, syncer.Options, func(syncer.Progress)) (syncer.Report, error) {
		return syncer.Report{}, nil
	}
	dataDir := filepath.Join(exeDirOf(t, sys), dataDirName)

	// A brand-new archive is not backed up.
	if code := run([]string{"--sync"}, sys); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if m, _ := filepath.Glob(filepath.Join(dataDir, "backup", "archive-*.db")); len(m) != 0 {
		t.Fatalf("backup of a new archive: %q", m)
	}

	// An archive that has synced before and was never backed up is.
	setMeta(t, filepath.Join(dataDir, archiveName), "last_sync_at", time.Now().UTC().Format(time.RFC3339))
	if code := run([]string{"--sync"}, sys); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if m, _ := filepath.Glob(filepath.Join(dataDir, "backup", "archive-*.db")); len(m) != 1 {
		t.Fatalf("backups after the second sync: %q, want 1", m)
	}
}

func TestRunUI(t *testing.T) {
	sys, out := testSystem(t, "")
	var app tui.App
	sys.runTUI = func(a tui.App) error {
		app = a
		if _, err := a.DB.Meta("x"); err != nil {
			t.Errorf("archive not usable from the UI: %v", err)
		}
		return nil
	}
	if code := run([]string{"--no-update-check"}, sys); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	dataDir := filepath.Join(exeDirOf(t, sys), dataDirName)
	if app.DB == nil || app.DataDir != dataDir || app.Version != version || !app.FirstRun || !app.NoUpdate ||
		app.Machine != "machine-1" || app.Hostname != "host-1" {
		t.Errorf("tui.App = %+v", app)
	}
	if app.DB.Path() != filepath.Join(dataDir, archiveName) {
		t.Errorf("archive path %s", app.DB.Path())
	}
	// The archive was closed after the UI returned: it can be renamed.
	if err := os.Rename(app.DB.Path(), app.DB.Path()+".moved"); err != nil {
		t.Errorf("archive still open after run: %v", err)
	}
}

func TestRunUINotFirstRunAndBackup(t *testing.T) {
	sys, out := testSystem(t, "")
	dataDir := filepath.Join(exeDirOf(t, sys), dataDirName)
	setMeta(t, filepath.Join(dataDir, archiveName), "last_sync_at", time.Now().UTC().Format(time.RFC3339))
	var app tui.App
	sys.runTUI = func(a tui.App) error { app = a; return nil }
	if code := run(nil, sys); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if app.FirstRun || app.NoUpdate {
		t.Errorf("tui.App FirstRun=%v NoUpdate=%v, want false/false", app.FirstRun, app.NoUpdate)
	}
	if m, _ := filepath.Glob(filepath.Join(dataDir, "backup", "archive-*.db")); len(m) != 1 {
		t.Errorf("weekly backup: %q, want one file", m)
	}
}

func TestRunUIRescanFirst(t *testing.T) {
	sys, out := testSystem(t, "")
	var order []string
	sys.sync = func(_ context.Context, _ *archive.DB, opts syncer.Options, _ func(syncer.Progress)) (syncer.Report, error) {
		if !opts.Rescan {
			t.Errorf("--rescan: sync options %+v", opts)
		}
		order = append(order, "sync")
		return syncer.Report{Sources: 1, Read: 1}, nil
	}
	sys.runTUI = func(tui.App) error { order = append(order, "ui"); return nil }
	if code := run([]string{"--rescan"}, sys); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if strings.Join(order, ",") != "sync,ui" {
		t.Errorf("order = %v, want sync then ui", order)
	}
}

func TestRunUIRestart(t *testing.T) {
	sys, _ := testSystem(t, "")
	dataDir := filepath.Join(t.TempDir(), "data")
	sys.runTUI = func(tui.App) error { return tui.ErrRestart }
	var gotExe string
	var gotArgs []string
	sys.restart = func(exe string, args []string) int {
		gotExe, gotArgs = exe, args
		// The archive is closed before the new exe starts.
		if err := os.Rename(filepath.Join(dataDir, archiveName), filepath.Join(dataDir, "moved.db")); err != nil {
			t.Errorf("archive still open at restart: %v", err)
		}
		return 7
	}
	if code := run([]string{"--data-dir", dataDir, "--no-update-check"}, sys); code != 7 {
		t.Fatalf("exit code %d, want the new exe's 7", code)
	}
	exe, _ := sys.executable()
	if gotExe != exe || strings.Join(gotArgs, " ") != "--data-dir "+dataDir+" --no-update-check" {
		t.Errorf("restart(%q, %q)", gotExe, gotArgs)
	}
}

func TestRunUIError(t *testing.T) {
	sys, out := testSystem(t, "\n")
	sys.console = true
	sys.runTUI = func(tui.App) error { return errors.New("no terminal") }
	if code := run(nil, sys); code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	if !strings.Contains(out.String(), "no terminal") || !strings.Contains(out.String(), "Press Enter to close") {
		t.Errorf("output:\n%s", out)
	}
}

func TestRunUICloudWarningOnce(t *testing.T) {
	sys, out := testSystem(t, "\n\n")
	sys.console = true
	exeDir := filepath.Join(t.TempDir(), "OneDrive", "Athar")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sys.executable = func() (string, error) { return filepath.Join(exeDir, "athar.exe"), nil }
	sys.runTUI = func(tui.App) error { return nil }

	if code := run(nil, sys); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	first := out.String()
	if !strings.Contains(first, "OneDrive") || !strings.Contains(first, "Press Enter to continue") {
		t.Fatalf("no cloud warning:\n%s", first)
	}
	if code := run(nil, sys); code != 0 {
		t.Fatalf("second run exit code %d", code)
	}
	if strings.Contains(strings.TrimPrefix(out.String(), first), "OneDrive") {
		t.Errorf("cloud warning shown twice:\n%s", out)
	}
}

func TestRunUIConfigError(t *testing.T) {
	sys, out := testSystem(t, "")
	exeDir := exeDirOf(t, sys)
	if err := os.WriteFile(filepath.Join(exeDir, "athar.toml"), []byte("[display]\narabic = \"sideways\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	started := false
	sys.runTUI = func(a tui.App) error {
		started = true
		if a.Config.Display.Arabic != "" {
			t.Errorf("config with errors used: %+v", a.Config)
		}
		return nil
	}
	if code := run(nil, sys); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if !started || !strings.Contains(out.String(), "sideways") {
		t.Errorf("config error not shown or UI not started:\n%s", out)
	}
}

func TestRunUIConfigLoaded(t *testing.T) {
	sys, out := testSystem(t, "")
	exeDir := exeDirOf(t, sys)
	if err := os.WriteFile(filepath.Join(exeDir, "athar.toml"), []byte("[display]\narabic = \"raw\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var app tui.App
	sys.runTUI = func(a tui.App) error { app = a; return nil }
	if code := run(nil, sys); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	if app.Config.Display.Arabic != "raw" {
		t.Errorf("config not passed to the UI: %+v", app.Config)
	}
}

func TestRunUIRescanInterruptedOrLocked(t *testing.T) {
	// Ctrl+C during the rescan: athar stops, the UI never opens.
	sys, _ := testSystem(t, "")
	sys.sync = func(context.Context, *archive.DB, syncer.Options, func(syncer.Progress)) (syncer.Report, error) {
		return syncer.Report{}, context.Canceled
	}
	if code := run([]string{"--rescan"}, sys); code != 1 {
		t.Fatalf("interrupted rescan: exit code %d, want 1", code)
	}

	// Another window is syncing: the rescan is skipped and the UI opens.
	sys, out := testSystem(t, "")
	sys.sync = func(context.Context, *archive.DB, syncer.Options, func(syncer.Progress)) (syncer.Report, error) {
		return syncer.Report{}, syncer.ErrLocked
	}
	started := false
	sys.runTUI = func(tui.App) error { started = true; return nil }
	if code := run([]string{"--rescan"}, sys); code != 0 || !started {
		t.Fatalf("locked rescan: exit code %d, UI started %v", code, started)
	}
	if !strings.Contains(out.String(), "--rescan was skipped") {
		t.Errorf("no skipped note:\n%s", out)
	}
}
