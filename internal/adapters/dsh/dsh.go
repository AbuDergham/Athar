// Package dsh reads DeepSeek Harness (dsh, npm @deepseek-ai/dsh) sessions.
//
// Layout (dsh 0.1.x; checked against the installed package source and the
// structure of real files):
//
//	<root>\sessions\<project-key>\<session-id>\session[.vN].jsonl[.zstd]
//
// <root> is %DSH_HOME% when set and %USERPROFILE%\.dsh; both are read (the
// default root may still hold older sessions). <project-key> is a lossy
// "--<encoded cwd>--" or "_no-cwd"; it is never decoded because the header
// line carries the exact cwd. Each format generation N is its own file: a
// migration writes session.vN.jsonl[.zstd] next to the older session.jsonl[.zstd],
// seeded with the old events and closed by a "session/end-seed" marker, and new
// events are appended to it. One generation is read, so the older copy never
// duplicates the session: the newest one whose format this parser knows (v3;
// each dsh format bump so far renamed events), the zstd copy when a generation
// exists both compressed and plain. A newer, unknown generation is only read
// when nothing else exists, and is reported on the Sources screen. Non-canonical
// names (staging/temp files, "v0", leading zeros, upper case) and the
// subagents\ folder are ignored.
//
// A .zstd log is a concatenation of complete, checksummed zstd frames, one per
// appended batch; a crash can leave a torn last frame, which is reported as a
// warning while the events of the complete frames are kept.
//
// Line 1 is the header {"type":"session","version","id","createdAt"(ms),"cwd",
// "delegationDepth","origin","parentSession",...}. Subagent sessions (origin
// "subagent" or delegationDepth > 0) are skipped; a parentSession alone marks a
// fork, which is kept as a session of its own (its log starts with the parent's
// events up to the fork, then "session/end-seed"). Every other line is an event
// {"type","seq","time"(epoch ms),"data"}:
//
//   - user/message: data{content[], source{kind}, id}. Only source.kind "user"
//     is the human; "plugin", "skill-catalog" and "agent-instructions" are
//     injected context (reminders, AGENTS.md, compaction replacements).
//   - agent/inbox/spliced: data.inserted[] messages queued while the agent ran.
//     They normally reappear as user/message with the same id; a human one that
//     never does is kept at the inbox position.
//   - assistant/message: data.message.content[] "text" blocks ("reasoning" is
//     dropped; "tool-call" blocks are repeated by tool/call events).
//   - tool/call: data{name, arguments (a JSON string)}.
//   - tool/result: data.message.content[]{type:"tool-result", content[] text}.
//   - session/title: data{title, source{kind}}; kinds "user" (rename),
//     "provider" (LLM title) and "fallback" (first prompt). The last one wins,
//     as in dsh's own fold.
//
// Streaming chunk events (reasoning-chunks, text-chunks, tool-call-chunks,
// assistant/chunk, keyed seq0/time0), step/turn markers, system messages,
// commands, compaction records and settings events are ignored; they still
// count as activity for UpdatedAt, except session/end-seed (the migration
// time). Sessions without any message (opened and never used) are skipped.
//
// NativeID is the header id ("session-<uuid>", the folder name as fallback);
// AgentsView files the same session as "deepseek-harness:<header id>".
//
// dsh has no confirmed terminal resume command (its web UI resumes sessions
// over ACP), so Caps offers no resume; it only names DSH_HOME as the root variable.
package dsh

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// rootEnv selects dsh's data root (dsh resolves $DSH_HOME, else ~/.dsh).
const rootEnv = "DSH_HOME"

type adapter struct{}

// New returns the DeepSeek dsh adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return "dsh" }
func (adapter) Name() string { return "DeepSeek dsh" }
func (adapter) Version() int { return 1 }

// Caps: no resume (unconfirmed for dsh), only the data-root variable.
func (adapter) Caps() adapters.Caps { return adapters.Caps{RootEnv: rootEnv} }

// root is one dsh data folder; id is what goes into Source.Root / Session.Root.
type root struct{ dir, id string }

// roots returns DSH_HOME (when set and not the default) and %USERPROFILE%\.dsh.
func roots(env adapters.Env) []root {
	var def string
	if env.Home != "" {
		def = filepath.Join(env.Home, ".dsh")
	}
	var out []root
	if env.Getenv != nil {
		if v := strings.TrimSpace(env.Getenv(rootEnv)); v != "" && filepath.IsAbs(v) && !samePath(v, def) {
			v = filepath.Clean(v)
			out = append(out, root{dir: v, id: v})
		}
	}
	if def != "" {
		out = append(out, root{dir: def})
	}
	return out
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Sources lists one source per session folder: <root>\sessions\<project>\<session>\,
// fingerprinted by all of its canonical log generations.
// An unreadable root does not hide the other one; its error is returned with them.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	var errs []error
	for _, r := range roots(env) {
		srcs, err := sessionSources(r)
		if err != nil {
			errs = append(errs, err)
		}
		out = append(out, srcs...)
	}
	return out, errors.Join(errs...)
}

func sessionSources(r root) ([]adapters.Source, error) {
	base := filepath.Join(r.dir, "sessions")
	info, err := os.Stat(base)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.IsDir()) {
		return nil, nil // missing (or not a folder): nothing to read here
	}
	if err != nil {
		return nil, err
	}
	projects, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var out []adapters.Source
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		sessions, err := os.ReadDir(filepath.Join(base, p.Name()))
		if err != nil {
			continue // unreadable project folder: skip it, keep the others
		}
		for _, s := range sessions {
			if !s.IsDir() {
				continue
			}
			dir := filepath.Join(base, p.Name(), s.Name())
			files := logFiles(dir)
			if len(files) == 0 {
				continue
			}
			out = append(out, adapters.Source{Adapter: "dsh", Path: dir, Files: files, Root: r.id})
		}
	}
	return out, nil
}

// logFiles returns the canonical session logs directly inside dir, sorted by name.
func logFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if _, _, ok := parseLogName(e.Name()); ok {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// logName matches dsh's canonical generation names: session.jsonl (generation 0)
// or session.vN.jsonl (N >= 1, no leading zero), optionally with .zstd.
var logName = regexp.MustCompile(`^session(?:\.v([1-9][0-9]*))?\.jsonl(\.zstd)?$`)

func parseLogName(name string) (gen int, compressed, ok bool) {
	m := logName.FindStringSubmatch(name)
	if m == nil {
		return 0, false, false
	}
	if m[1] != "" {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, false, false
		}
		gen = n
	}
	return gen, m[2] != "", true
}

// logFile is one existing canonical generation file of a session.
type logFile struct {
	path       string
	gen        int
	compressed bool
}

// better orders candidates: higher generation first, zstd before plain within
// one generation (dsh and AgentsView treat the zstd copy as the live one).
func (a logFile) better(b logFile) bool {
	if a.gen != b.gen {
		return a.gen > b.gen
	}
	return a.compressed && !b.compressed
}

// pickLog chooses the log to read: the newest generation this parser knows
// (format <= knownFormat), because every dsh format bump so far renamed events;
// only when no known generation exists is the newest unknown one read. warn
// explains a newer generation that was skipped or read on trust.
func pickLog(files []string) (pick logFile, warn string, err error) {
	var known, unknown *logFile
	for _, f := range files {
		gen, z, ok := parseLogName(filepath.Base(f))
		if !ok {
			continue
		}
		if _, _, err := fsread.Stat(f); err != nil {
			continue
		}
		c := logFile{path: f, gen: gen, compressed: z}
		slot := &known
		if gen > knownFormat {
			slot = &unknown
		}
		if *slot == nil || c.better(**slot) {
			*slot = &c
		}
	}
	switch {
	case known != nil && unknown != nil:
		return *known, fmt.Sprintf("%s uses format v%d, newer than v%d; read %s instead",
			filepath.Base(unknown.path), unknown.gen, knownFormat, filepath.Base(known.path)), nil
	case known != nil:
		return *known, "", nil
	case unknown != nil:
		return *unknown, fmt.Sprintf("%s uses format v%d, newer than v%d; some records may be missing",
			filepath.Base(unknown.path), unknown.gen, knownFormat), nil
	}
	return logFile{}, "", fmt.Errorf("no dsh session log among %d file(s): %w", len(files), fs.ErrNotExist)
}

// Read parses one log generation of a session folder (see pickLog).
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	log, warn, err := pickLog(src.Files)
	if err != nil {
		return res, err
	}
	path := log.path
	p := newParser()
	n := 0
	fn := func(line []byte) error {
		if n++; n%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		p.line(line)
		return nil
	}
	if log.compressed {
		err = fsread.ZstdLines(path, fn)
	} else {
		err = fsread.Lines(path, fn)
	}
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return res, cerr
		}
		if errors.Is(err, fs.ErrNotExist) || p.records == 0 {
			return res, err
		}
		// A torn last zstd frame (crash while appending) or a read error mid-file:
		// keep what was read; the rest comes with the next sync.
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: stopped after %d records: %v", filepath.Base(path), p.records, err))
	}
	res.Skipped = p.skipped
	if warn != "" {
		res.Warnings = append(res.Warnings, warn) // already says what the header would
	} else {
		res.Warnings = append(res.Warnings, p.warnings...)
	}
	if s, ok := p.session(filepath.Base(src.Path), src.Root); ok {
		res.Sessions = append(res.Sessions, s)
	}
	return res, nil
}
