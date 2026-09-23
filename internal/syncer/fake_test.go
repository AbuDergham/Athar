package syncer

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// fake is a scripted adapter: every *.txt file in dir is one source holding
// one session (NativeID = file stem) whose messages are the file's lines.
type fake struct {
	id  string
	dir string

	mu      sync.Mutex
	version int
	listErr error
	extra   []string // paths listed as sources even when they do not exist
	reads   map[string]int
	// hook runs at the start of Read; it may block, panic or fail the read.
	hook func(ctx context.Context, env adapters.Env, src adapters.Source) error
}

func newFake(t *testing.T, id string) *fake {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &fake{id: id, dir: dir, version: 1, reads: map[string]int{}}
}

func (f *fake) ID() string   { return f.id }
func (f *fake) Name() string { return "Fake " + f.id }
func (f *fake) Version() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.version
}
func (f *fake) Caps() adapters.Caps { return adapters.Caps{} }

func (f *fake) Sources(env adapters.Env) ([]adapters.Source, error) {
	f.mu.Lock()
	listErr, extra := f.listErr, append([]string(nil), f.extra...)
	f.mu.Unlock()
	if listErr != nil {
		return nil, listErr
	}
	paths, err := filepath.Glob(filepath.Join(f.dir, "*.txt"))
	if err != nil {
		return nil, err
	}
	paths = append(paths, extra...)
	sort.Strings(paths)
	var out []adapters.Source
	for _, p := range paths {
		out = append(out, adapters.Source{Adapter: f.id, Path: p, Files: []string{p}})
	}
	return out, nil
}

func (f *fake) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	f.mu.Lock()
	f.reads[src.Path]++
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, env, src); err != nil {
			return adapters.ReadResult{}, err
		}
	}
	data, err := fsread.ReadFile(src.Path)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	stem := strings.TrimSuffix(filepath.Base(src.Path), ".txt")
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	s := model.Session{
		Agent: f.id, NativeID: stem, TitleSource: model.TitlePrompt,
		StartedAt: start, UpdatedAt: start, Coverage: model.CoverageFull, Origin: model.OriginLive,
	}
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		at := start.Add(time.Duration(i) * time.Minute)
		s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: line})
		s.UpdatedAt = at
	}
	s.Title = s.Messages[0].Text
	return adapters.ReadResult{Sessions: []model.Session{s}}, nil
}

func (f *fake) setHook(h func(ctx context.Context, env adapters.Env, src adapters.Source) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hook = h
}

func (f *fake) readCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[path]
}

// write creates or replaces <dir>/<name>.txt and gives it an mtime that
// differs from its previous one, so the fingerprint always changes.
func (f *fake) write(t *testing.T, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(f.dir, name+".txt")
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	if info, err := os.Stat(p); err == nil {
		mtime = info.ModTime().Add(2 * time.Second)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

// harness is an archive plus the Env and Options of a test sync.
type harness struct {
	db   *archive.DB
	env  adapters.Env
	opts Options
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	db, err := archive.Open(filepath.Join(dir, "athar-data", "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &harness{
		db: db,
		env: adapters.Env{
			Home:    filepath.Join(dir, "home"),
			TempDir: filepath.Join(dir, "tmp", "copy"),
			Getenv:  func(string) string { return "" },
		},
		opts: Options{Machine: "M1", LockDir: filepath.Join(dir, "athar-data")},
	}
}

func (h *harness) run(t *testing.T, list ...adapters.Adapter) Report {
	t.Helper()
	rep, err := Run(context.Background(), h.db, list, h.env, h.opts, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

// rows returns the archived sessions by NativeID.
func (h *harness) rows(t *testing.T) map[string]archive.Row {
	t.Helper()
	rows, err := h.db.Search(archive.Filter{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]archive.Row{}
	for _, r := range rows {
		out[r.NativeID] = r
	}
	return out
}

// sources returns the Sources-screen lines by path.
func (h *harness) sources(t *testing.T) map[string]archive.SourceStatus {
	t.Helper()
	list, err := h.db.Sources(h.opts.Machine)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]archive.SourceStatus{}
	for _, s := range list {
		out[s.Path] = s
	}
	return out
}

// counts is the part of a Report the tests compare.
type counts struct {
	Sources, Read, Unchanged, Failed, RetryLater, NewlyMissing int
}

func countsOf(r Report) counts {
	return counts{r.Sources, r.Read, r.Unchanged, r.Failed, r.RetryLater, r.NewlyMissing}
}

func wantCounts(t *testing.T, label string, got Report, want counts) {
	t.Helper()
	if c := countsOf(got); c != want {
		t.Errorf("%s: report %+v, want %+v (errors %v)", label, c, want, got.Errors)
	}
}
