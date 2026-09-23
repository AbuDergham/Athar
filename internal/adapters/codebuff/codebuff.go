// Package codebuff reads Codebuff and Freebuff chats. Both CLIs share one
// on-disk layout (verified against freebuff 0.0.91 and the strings of a newer
// freebuff.exe on the owner's PC):
//
//	%USERPROFILE%\.config\manicode\                        prod data root (Root "")
//	%USERPROFILE%\.config\manicode-<env>\                  dev/staging roots (Root = the folder)
//	<root>\projects\<project>\chats\<chat-id>\             one folder per chat
//	    log.jsonl            debug log (older builds, and the only file on the owner's PC)
//	    chat-messages.json   the chat as the UI shows it (newer builds)
//	    run-state.json       SDK run state; sessionState.*.cwd is the working folder
//	    chat-meta.json       {messageCount, firstPrompt, ...}
//	<root>\message-history.json                            the prompt history (up-arrow list)
//
// <chat-id> is the UTC creation time with ':' replaced by '-'
// ("2026-01-02T03-04-05.678Z"); <project> is the base name of the project
// folder. NativeID is "<project>:<chat-id>", the same id AgentsView uses after
// its "codebuff:"/"freebuff:" prefix, so imported rows collapse with live ones.
//
// A chat folder with chat-messages.json is read from it (Coverage full):
// an array of {id, variant: user|ai|error, content, blocks[], timestamp}.
// ai blocks are text (textType "reasoning" is thinking and dropped), tool
// {toolName, input, output}, agent (a subagent: only its spawn and returned
// content are kept, never its nested blocks), plan, ask-user; mode dividers
// and images are skipped. Timestamps are local "HH:MM AM" strings, so exact
// times come from ids like "user-<unix ms>" when present, and time-of-day
// stamps are placed on the chat folder's local date with midnight rollover.
//
// Otherwise log.jsonl is read (Coverage partial: a debug log, not a
// transcript). Lines are {level, msg, data, timestamp, pid, hostname, userId,
// userEmail}; hostname and the user fields are never read. Only these records
// carry the conversation:
//   - msg "[send-message] ...": data.runConfig.prompt is the user's prompt,
//     runConfig.agent the main agent type, and
//     runConfig.previousRun.sessionState.fileContext.cwd the working folder
//     (absent for the first prompt of a chat).
//   - msg "Start agent <type> step <n> (<agent-id>[ - <prompt>])": the first
//     one after a prompt names the main agent; subagents log "(<parent>-<child>)".
//   - msg "End agent <type> step <n> (<agent-id>...)" of the main agent:
//     data.fullResponse (the step's visible text), data.toolCalls and
//     data.toolResults. Subagent steps are skipped; the parent keeps its
//     spawn_agents call and the result it got back. The cumulative message
//     histories logged with every step are not read.
//   - msg "Main prompt finished" with data.output.type "error": the error
//     message becomes a system message.
//
// message-history.json (a JSON array of strings, oldest first, capped at
// 1000 by the CLI; no ids, no times) becomes one prompt-log session
// "message-history" (":<root folder>" for a non-default root) holding only the
// prompts that no chat folder under the same root contains.
//
// Whole-file JSON sources modified within the last 5 s are not read; Read
// returns ErrRecentlyModified (which wraps adapters.ErrRetryLater) so the
// syncer reads the source again on the next sync.
// There is no resume command: `freebuff` self-updates when started, so Athar
// never runs it (spec §6.3).
package codebuff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// ID is the adapter id stored in the archive.
const ID = "codebuff"

// File names inside a data root.
const (
	chatMessagesFile = "chat-messages.json"
	runStateFile     = "run-state.json"
	chatMetaFile     = "chat-meta.json"
	logFile          = "log.jsonl"
	historyFile      = "message-history.json"
)

// Source.Extra["kind"] values.
const (
	kindChat    = "chat"
	kindHistory = "history"
)

// ErrRecentlyModified is returned (wrapped) by Read when a whole-file JSON
// source changed within the last 5 s: it may be half-written, so it is read
// on the next sync instead (spec §4.3). It wraps adapters.ErrRetryLater.
var ErrRecentlyModified = fmt.Errorf("modified in the last 5 s: %w", adapters.ErrRetryLater)

// recentWindow is how long a whole-file JSON source must be left alone after a write.
const recentWindow = 5 * time.Second

// Package clock and zone; tests replace them.
var (
	now       = time.Now
	localZone = time.Local // Codebuff's time-of-day stamps are the PC's local time
)

type adapter struct{}

// New returns the Codebuff/Freebuff adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return ID }
func (adapter) Name() string { return "Codebuff" }

// Version is bumped whenever parsing changes.
func (adapter) Version() int { return 1 }

// Caps is empty: no resume command is confirmed, and freebuff must never be
// started by Athar (it self-updates on launch).
func (adapter) Caps() adapters.Caps { return adapters.Caps{} }

// dataRoot is one ~/.config/manicode[-<env>] folder.
type dataRoot struct {
	path string
	root string // Session.Root: "" for the prod folder, else the folder path
}

// dataRoots returns ~/.config/manicode first, then every ~/.config/manicode-<env>.
func dataRoots(env adapters.Env) []dataRoot {
	cfg := filepath.Join(env.Home, ".config")
	entries, err := os.ReadDir(cfg)
	if err != nil {
		return nil
	}
	var out []dataRoot
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if name != "manicode" && !strings.HasPrefix(name, "manicode-") {
			continue
		}
		p := filepath.Join(cfg, e.Name())
		if !isDir(p) {
			continue
		}
		r := dataRoot{path: p, root: p}
		if name == "manicode" {
			r.root = ""
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].root == "") != (out[j].root == "") {
			return out[i].root == ""
		}
		return out[i].path < out[j].path
	})
	return out
}

// chatDir is one <root>\projects\<project>\chats\<chat-id> folder.
type chatDir struct {
	path    string
	project string
	chatID  string
}

// chatDirs lists the chat folders under a data root, sorted by project and chat id.
func chatDirs(root string) []chatDir {
	projects := filepath.Join(root, "projects")
	pents, err := os.ReadDir(projects)
	if err != nil {
		return nil
	}
	var out []chatDir
	for _, p := range pents {
		chats := filepath.Join(projects, p.Name(), "chats")
		if !isDir(filepath.Join(projects, p.Name())) {
			continue
		}
		cents, err := os.ReadDir(chats)
		if err != nil {
			continue
		}
		for _, c := range cents {
			dir := filepath.Join(chats, c.Name())
			if isDir(dir) {
				out = append(out, chatDir{path: dir, project: p.Name(), chatID: c.Name()})
			}
		}
	}
	return out
}

// chatFiles returns the files a chat folder is read from (they make up its
// fingerprint): chat-messages.json with run-state.json and chat-meta.json when
// present, else log.jsonl; nil when the folder holds neither.
func chatFiles(dir string) []string {
	if cm := filepath.Join(dir, chatMessagesFile); isFile(cm) {
		files := []string{cm}
		for _, name := range []string{runStateFile, chatMetaFile} {
			if p := filepath.Join(dir, name); isFile(p) {
				files = append(files, p)
			}
		}
		return files
	}
	if lf := filepath.Join(dir, logFile); isFile(lf) {
		return []string{lf}
	}
	return nil
}

// Sources lists one source per chat folder and one per message-history.json.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	for _, r := range dataRoots(env) {
		for _, c := range chatDirs(r.path) {
			files := chatFiles(c.path)
			if len(files) == 0 {
				continue
			}
			out = append(out, adapters.Source{
				Adapter: ID, Path: c.path, Files: files, Root: r.root,
				Extra: map[string]string{"kind": kindChat},
			})
		}
		if h := filepath.Join(r.path, historyFile); isFile(h) {
			out = append(out, adapters.Source{
				Adapter: ID, Path: h, Files: []string{h}, Root: r.root,
				Extra: map[string]string{"kind": kindHistory},
			})
		}
	}
	return out, nil
}

// Read parses one chat folder or one message-history.json.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	kind := src.Extra["kind"]
	if kind == "" && strings.EqualFold(filepath.Base(src.Path), historyFile) {
		kind = kindHistory
	}
	if kind == kindHistory {
		return readHistory(ctx, src)
	}
	return readChatDir(ctx, src)
}

// readChatDir reads one chat folder from chat-messages.json or, failing that, log.jsonl.
func readChatDir(ctx context.Context, src adapters.Source) (adapters.ReadResult, error) {
	dir := src.Path
	c := chatDir{path: dir, chatID: filepath.Base(dir), project: filepath.Base(filepath.Dir(filepath.Dir(dir)))}
	created := chatDirTime(c.chatID)

	var (
		pc       parsedChat
		coverage model.Coverage
		mainFile string
		err      error
	)
	switch files := chatFiles(dir); {
	case len(files) == 0:
		return adapters.ReadResult{}, nil // the chat folder was emptied or removed since Sources
	case filepath.Base(files[0]) == chatMessagesFile:
		if err := checkRecent(files...); err != nil {
			return adapters.ReadResult{}, err
		}
		mainFile, coverage = files[0], model.CoverageFull
		pc, err = readChatMessages(ctx, dir, created)
	default:
		mainFile, coverage = files[0], model.CoveragePartial
		pc, err = readLog(ctx, files[0])
	}
	if err != nil {
		return adapters.ReadResult{}, err
	}
	res := adapters.ReadResult{Skipped: pc.skipped}
	if len(pc.msgs) == 0 {
		return res, nil // nothing was said in this chat (e.g. the CLI was opened and closed)
	}

	s := model.Session{
		Agent:    ID,
		NativeID: c.project + ":" + c.chatID,
		Cwd:      pc.cwd,
		Coverage: coverage,
		Origin:   model.OriginLive,
		Root:     src.Root,
		Messages: pc.msgs,
	}
	s.Title, s.TitleSource = promptTitle(pc.msgs, pc.firstPrompt)
	s.StartedAt, s.UpdatedAt = sessionTimes(created, pc.msgs, pc.last, mainFile)
	res.Sessions = []model.Session{s}
	return res, nil
}

// parsedChat is what the chat-messages.json and log.jsonl readers return.
type parsedChat struct {
	msgs        []model.Message
	cwd         string
	firstPrompt string    // title fallback (chat-meta.json)
	last        time.Time // latest activity that produced no message
	skipped     int
}

// promptTitle is the first user prompt (whitespace collapsed, at most 100
// runes), else fallback; TitleNone when there is neither.
func promptTitle(msgs []model.Message, fallback string) (string, model.TitleSource) {
	for _, m := range msgs {
		if m.Role == model.RoleUser && m.Kind == model.KindText {
			if t := firstRunes(cleanTitle(m.Text), 100); t != "" {
				return t, model.TitlePrompt
			}
		}
	}
	if t := firstRunes(cleanTitle(fallback), 100); t != "" {
		return t, model.TitlePrompt
	}
	return "", model.TitleNone
}

// sessionTimes: StartedAt is the earliest of the chat creation time and the
// message times; UpdatedAt the latest message or activity time. Without any
// time, both fall back to the main file's mtime.
func sessionTimes(created time.Time, msgs []model.Message, last time.Time, mainFile string) (started, updated time.Time) {
	started, updated = created, last
	for _, m := range msgs {
		if m.At.IsZero() {
			continue
		}
		if started.IsZero() || m.At.Before(started) {
			started = m.At
		}
		if m.At.After(updated) {
			updated = m.At
		}
	}
	if updated.IsZero() || updated.Before(started) {
		updated = started
	}
	if updated.IsZero() {
		if _, mtime, err := fsread.Stat(mainFile); err == nil {
			updated = mtime.UTC()
		}
	}
	if started.IsZero() {
		started = updated
	}
	return started.UTC(), updated.UTC()
}

// chatDirTime parses a chat folder name ("2026-01-02T03-04-05.678Z", UTC) into
// a UTC time; a name without the Z is local time. Zero when it is not a time.
func chatDirTime(name string) time.Time {
	if t, err := time.Parse("2006-01-02T15-04-05Z", name); err == nil {
		return t.UTC()
	}
	if t, err := time.ParseInLocation("2006-01-02T15-04-05", name, localZone); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// checkRecent returns ErrRecentlyModified when any existing file changed within recentWindow.
func checkRecent(paths ...string) error {
	t := now()
	for _, p := range paths {
		_, mtime, err := fsread.Stat(p)
		if err != nil {
			continue
		}
		if age := t.Sub(mtime); age < recentWindow && age > -recentWindow {
			return fmt.Errorf("%s: %w", p, ErrRecentlyModified)
		}
	}
	return nil
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
