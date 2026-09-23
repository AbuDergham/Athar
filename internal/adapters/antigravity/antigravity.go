// Package antigravity reads the conversations of the Google Antigravity IDE
// (the agent built into the Antigravity editor, not the agy CLI).
//
// Stores (verified against Antigravity 1.23 / 2.0 files, structure only):
//
//	~\.gemini\antigravity\conversations\<id>.pb      one per conversation; ENCRYPTED, never read
//	~\.gemini\antigravity-backup\conversations\…    copies left by the 2.0 migration
//	~\.gemini\antigravity-ide\conversations\…       copies of the 2.0 IDE (a few ids live only here)
//	<root>\brain\<id>\<name>.md                      plan/task/walkthrough artifacts (Markdown)
//	<root>\brain\<id>\<name>.md.metadata.json        {artifactType, summary, updatedAt, version}
//	<root>\brain\<id>\<name>.md.resolved[.N]         older versions of the artifact
//	<root>\brain\<id>\.system_generated\logs\*       JSONL step logs (transcript.jsonl, overview.txt)
//	<root>\agyhub_summaries_proto.pb                 summary index (protobuf)
//	%APPDATA%\Antigravity*\User\globalStorage\state.vscdb  summary index (SQLite ItemTable)
//
// Sources: one per conversation id found in any root's conversations\*.pb (or
// in a brain folder holding readable text). The three roots are deduplicated
// by id; the main root wins, then backup, then ide. Source.Path is the chosen
// .pb file (the brain folder for brain-only ids); Files are that .pb, the
// brain files that are read, and every index file, so a changed title or a
// new step re-reads the conversation. Extra carries the id and brain folder.
//
// Index (protowire; field numbers confirmed on real files):
//   - agyhub_summaries_proto.pb: repeated field 1 {1: id, 2: Summary}.
//   - state.vscdb ItemTable key "antigravityUnifiedStateSync.trajectorySummaries":
//     base64 text → repeated field 1 {1: id, 2: {1: base64(Summary)}}.
//   - Summary: 1 title, 2 step count, 3 last modified {1: s, 2: ns}, 4 trajectory
//     id, 5 status, 7 created {1: s, 2: ns}, 9 workspace {1: file:/// URI},
//     10 last user input {1: s, 2: ns}, 17 {1: {1: file:/// URI}} (fallback
//     workspace). When several indexes know an id, the most recently modified
//     summary wins and fills its missing fields from the others.
//
// Text:
//   - Step logs (JSONL {step_index, type, source, status, created_at, content,
//     tool_calls[{name, args}], thinking}): USER_INPUT → user (the text inside
//     <USER_REQUEST>, without <ADDITIONAL_METADATA> editor state), PLANNER_RESPONSE
//     → assistant text plus one tool call per tool_calls entry, other step types
//     with content (RUN_COMMAND, VIEW_FILE, …) → tool output (2 KB). SYSTEM
//     steps and thinking are dropped. A later record with the same step_index
//     (RUNNING → DONE) replaces the earlier one in place.
//   - Artifacts: for every <name>.md the current file (else .resolved, else the
//     highest .resolved.N) becomes one assistant message "<Label> (<name>.md)"
//     timed by its metadata updatedAt, after the log messages.
//   - Coverage: full when a step log has messages, partial with artifacts only,
//     titles otherwise (meta when not even the index knows the id).
//
// Whole-file inputs (artifacts, metadata, agyhub index) modified less than 5 s
// ago make Read return adapters.ErrRetryLater. The .pb mtimes were reset by the
// 2.0 migration, so dates come from the index, then from the texts.
//
// There is no way to open one conversation from the command line: Resume opens
// the workspace folder in Antigravity, where the user picks it from the list.
package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
)

const (
	id = "antigravity"
	// settle is how long a whole-file source must be unchanged before it is read (spec §4.3).
	settle = 5 * time.Second
	// Extra keys handed from Sources to Read.
	extraID    = "id"
	extraBrain = "brain"
)

// rootNames are the Antigravity data folders under ~\.gemini, in preference order.
var rootNames = []string{"antigravity", "antigravity-backup", "antigravity-ide"}

type adapter struct {
	now func() time.Time // nil = time.Now; tests move the clock
}

// New returns the Antigravity IDE adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return id }
func (adapter) Name() string { return "Antigravity IDE" }
func (adapter) Version() int { return 1 }

// Caps: no per-conversation resume; the app opens the workspace folder.
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		App: &adapters.AppCommand{
			Name:       "Antigravity",
			Program:    "antigravity",
			Args:       []string{"{cwd}"},
			Candidates: []string{`%LOCALAPPDATA%\Programs\Antigravity\bin\antigravity.cmd`},
		},
	}
}

func (a adapter) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// roots returns ~\.gemini\antigravity, -backup and -ide (existing or not).
func roots(env adapters.Env) []string {
	if env.Home == "" {
		return nil
	}
	out := make([]string, 0, len(rootNames))
	for _, n := range rootNames {
		out = append(out, filepath.Join(env.Home, ".gemini", n))
	}
	return out
}

// Sources lists one source per conversation id (see the package comment).
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var errs []error
	pbs := map[string]string{}      // id → chosen .pb
	brains := map[string][]string{} // id → brain folders in root order
	var order []string
	seen := map[string]bool{}
	note := func(cid string) {
		if !seen[cid] {
			seen[cid] = true
			order = append(order, cid)
		}
	}
	for _, root := range roots(env) {
		entries, err := readDir(filepath.Join(root, "conversations"))
		if err != nil {
			errs = append(errs, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".pb") {
				continue
			}
			cid := name[:len(name)-len(".pb")]
			if !isConversationID(cid) {
				continue
			}
			if _, ok := pbs[cid]; !ok {
				pbs[cid] = filepath.Join(root, "conversations", name)
			}
			note(cid)
		}
		entries, err = readDir(filepath.Join(root, "brain"))
		if err != nil {
			errs = append(errs, err)
		}
		for _, e := range entries {
			if e.IsDir() && isConversationID(e.Name()) {
				brains[e.Name()] = append(brains[e.Name()], filepath.Join(root, "brain", e.Name()))
			}
		}
	}
	// Brain folders with readable text but no .pb anywhere are conversations too.
	var brainOnly []string
	for cid, dirs := range brains {
		if _, ok := pbs[cid]; !ok && pickBrain(dirs).hasText() {
			brainOnly = append(brainOnly, cid)
		}
	}
	sort.Strings(brainOnly)
	for _, cid := range brainOnly {
		note(cid)
	}

	index := indexFiles(env)
	out := make([]adapters.Source, 0, len(order))
	for _, cid := range order {
		b := pickBrain(brains[cid])
		path := pbs[cid]
		var files []string
		if path != "" {
			files = append(files, path)
		} else {
			path = b.dir
		}
		files = append(files, b.files()...)
		files = append(files, index...)
		out = append(out, adapters.Source{
			Adapter: id,
			Path:    path,
			Files:   files,
			Extra:   map[string]string{extraID: cid, extraBrain: b.dir},
		})
	}
	return out, errors.Join(errs...)
}

// readDir lists a folder; a missing folder is not an error.
func readDir(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return entries, fmt.Errorf("antigravity: list %s: %w", dir, err)
	}
	return entries, nil
}

// isConversationID reports whether s looks like a conversation UUID
// (8-4-4-4-12 hex digits). Other names in conversations\ and brain\ are ignored.
func isConversationID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'f') {
				return false
			}
		}
	}
	return true
}

// Read builds the session of one conversation.
func (a adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	cid := src.Extra[extraID]
	if cid == "" {
		cid = strings.TrimSuffix(filepath.Base(src.Path), filepath.Ext(src.Path))
	}
	if !isConversationID(cid) {
		return adapters.ReadResult{}, fmt.Errorf("antigravity: %s is not a conversation", src.Path)
	}
	var b brain
	if dir := src.Extra[extraBrain]; dir != "" {
		b = scanBrain(dir)
	}
	now := a.clock()
	if p := b.recentFile(now); p != "" {
		return adapters.ReadResult{}, fmt.Errorf("antigravity: %s changed less than 5 s ago: %w", filepath.Base(p), adapters.ErrRetryLater)
	}
	idx, err := loadIndex(ctx, env, now)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	res := adapters.ReadResult{Warnings: idx.warnings}
	sum, hasSum := idx.summaries[cid]
	s, skipped := buildSession(cid, sum, hasSum, b, src.Path)
	res.Skipped = skipped
	res.Sessions = append(res.Sessions, s)
	return res, nil
}
