// Package gemini reads Gemini CLI history (spec §7).
//
// Layout under <home>\.gemini, where <home> is %USERPROFILE% or GEMINI_CLI_HOME:
//
//	tmp\<slug>\chats\session-<YYYY-MM-DDTHH-MM>-<first 8 of id>.jsonl   transcript (op-log JSONL)
//	tmp\<slug>\chats\session-*.json                                     legacy transcript (one JSON document)
//	tmp\<slug>\chats\<parent id>\<id>.jsonl                             subagent transcript (skipped)
//	tmp\<slug>\.project_root                                            project path, lowercased on Windows
//	tmp\<slug-or-sha256>\logs.json                                      prompt log [{sessionId, messageId, type, message, timestamp}]
//	history\<slug>\.project_root, projects.json {"projects": {"<lowercased path>": "<slug>"}}
//
// Rules (from the Gemini CLI source, packages/core/src/services/
// chatRecordingService.ts on main as of 2026-09, and the structure of files
// written by 0.46):
//
//   - The first line of a transcript is metadata {sessionId, projectHash,
//     startTime, lastUpdated, kind}; kind "subagent" sessions are skipped.
//     NativeID is the full sessionId (file names carry only 8 hex chars).
//   - The rest is replayed like the CLI does: a message record {id, ...}
//     replaces an earlier record with the same id in place; {"$set": {...}}
//     merges metadata (summary = AI title, lastUpdated) and, when it carries
//     "messages", clears and rebuilds the list; {"$rewindTo": id} drops that
//     message and everything after it (everything when the id is unknown).
//   - Message types: user (text from displayContent, else content; slash
//     commands, "?" and injected <session_context>/<hook_context> are not
//     conversation), gemini (text parts without thought:true, toolCalls[],
//     functionCall parts), info/error/warning dropped. Tool results appear
//     both in toolCalls[].result and as user functionResponse parts after a
//     history checkpoint; each call id is emitted once.
//   - A legacy .json that has a migrated .jsonl twin (same name + "l") is skipped.
//   - Cwd = .project_root of the slug folder, else history\<slug>\.project_root,
//     else the projects.json key of the slug; old sha256 folders are matched by
//     hashing the known project paths. Gemini lowercases the path on Windows,
//     so the real casing is restored from the file system when the folder exists.
//   - logs.json yields prompt-log sessions (coverage prompts) that the archive
//     merges into the transcript of the same id when there is one.
package gemini

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
	"github.com/AbuDergham/Athar/internal/fsread"
)

const (
	agentID = "gemini"

	// rootEnv selects the Gemini home (the folder that contains .gemini).
	rootEnv = "GEMINI_CLI_HOME"

	// freshWindow: whole-file JSON sources modified more recently than this
	// may be half-written and are left for the next sync (spec §4.3).
	freshWindow = 5 * time.Second
)

// now is the clock used for the freshness check; tests replace it.
var now = time.Now

type adapter struct{}

// New returns the Gemini CLI adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return agentID }
func (adapter) Name() string { return "Gemini CLI" }
func (adapter) Version() int { return 1 }

// Caps: resume looks the id up in the current project's chats folder only,
// so launch must start in the session's Cwd (the project root).
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		Resume:            []string{"gemini", "--resume", "{id}"},
		ProgramCandidates: []string{`%APPDATA%\npm\gemini.cmd`},
		SkipPermissions:   []string{"--yolo"},
		RootEnv:           rootEnv,
	}
}

// root is one Gemini home: dir is its .gemini folder, root the value for
// Session.Root ("" for the default home, else the GEMINI_CLI_HOME value).
type root struct {
	dir  string
	root string
}

func roots(env adapters.Env) []root {
	var out []root
	if env.Home != "" {
		out = append(out, root{dir: filepath.Join(env.Home, ".gemini")})
	}
	if env.Getenv != nil {
		if h := strings.TrimSpace(env.Getenv(rootEnv)); h != "" && !samePath(h, env.Home) {
			out = append(out, root{dir: filepath.Join(h, ".gemini"), root: h})
		}
	}
	return out
}

func samePath(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// Sources lists every transcript and prompt log of every Gemini home.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	for _, r := range roots(env) {
		tmp := filepath.Join(r.dir, "tmp")
		slugs, err := os.ReadDir(tmp)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, slug := range slugs {
			if !slug.IsDir() {
				continue
			}
			slugDir := filepath.Join(tmp, slug.Name())
			extra := markerFiles(r.dir, slugDir)

			// An unreadable chats folder only hides that project; it never
			// stops the other projects from being listed.
			chats := filepath.Join(slugDir, "chats")
			entries, _ := os.ReadDir(chats)
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !strings.HasPrefix(name, "session-") {
					continue // subagent folders and anything else
				}
				p := filepath.Join(chats, name)
				switch {
				case strings.HasSuffix(name, ".jsonl"):
				case strings.HasSuffix(name, ".json"):
					if isFile(p + "l") {
						continue // migrated to .jsonl by the CLI; the .jsonl is the live copy
					}
				default:
					continue
				}
				out = append(out, source(p, r.root, extra))
			}

			logs := filepath.Join(slugDir, "logs.json")
			if isFile(logs) {
				out = append(out, source(logs, r.root, extra))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func source(path, root string, extra []string) adapters.Source {
	return adapters.Source{
		Adapter: agentID,
		Path:    path,
		Files:   append([]string{path}, extra...),
		Root:    root,
	}
}

// markerFiles are the files that decide a slug folder's project path; they are
// part of each source's fingerprint so a changed marker re-reads the source.
func markerFiles(gemDir, slugDir string) []string {
	marker := filepath.Join(slugDir, ".project_root")
	if isFile(marker) {
		return []string{marker}
	}
	return []string{
		filepath.Join(gemDir, "history", filepath.Base(slugDir), ".project_root"),
		filepath.Join(gemDir, "projects.json"),
	}
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// Read parses one transcript or prompt log.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	path := src.Path
	name := filepath.Base(path)
	switch {
	case strings.EqualFold(name, "logs.json"):
		if err := checkSettled(path); err != nil {
			return adapters.ReadResult{}, err
		}
		slugDir := filepath.Dir(path)
		gemDir := filepath.Dir(filepath.Dir(slugDir))
		return readLogs(path, projectRoot(gemDir, slugDir), src.Root)
	case strings.HasSuffix(name, ".jsonl"), strings.HasSuffix(name, ".json"):
		legacy := strings.HasSuffix(name, ".json")
		if legacy {
			if err := checkSettled(path); err != nil {
				return adapters.ReadResult{}, err
			}
		}
		slugDir := filepath.Dir(filepath.Dir(path))
		gemDir := filepath.Dir(filepath.Dir(slugDir))
		return readTranscript(ctx, path, legacy, projectRoot(gemDir, slugDir), src.Root)
	default:
		return adapters.ReadResult{}, fmt.Errorf("gemini: unknown source %s", path)
	}
}

// checkSettled refuses whole-file JSON sources written in the last few seconds:
// they may be half-written. The error makes sync retry the source next time.
func checkSettled(path string) error {
	_, mtime, err := fsread.Stat(path)
	if err != nil {
		return err
	}
	if age := now().Sub(mtime); age < freshWindow && age > -freshWindow {
		return fmt.Errorf("%s changed less than %v ago; it is read on the next sync", filepath.Base(path), freshWindow)
	}
	return nil
}
