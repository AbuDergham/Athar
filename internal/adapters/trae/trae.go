// Package trae reads the chat history of Trae (ByteDance's VS Code-based
// IDE, also shipped as Trae CN / TRAE SOLO CN).
//
// Layout (every %APPDATA%\<Host> folder whose name starts with "Trae"):
//
//	User\workspaceStorage\<hash>\state.vscdb  VS Code key-value store (SQLite, ItemTable)
//	User\workspaceStorage\<hash>\workspace.json  {"folder": "file:///d%3A/..."} = the project
//	User\globalStorage\state.vscdb             empty-window / global chats (no project)
//
// Each state.vscdb is one Source (read through fsread.CopySQLite); its
// workspace.json is part of the fingerprint. The ItemTable keys read are:
//
//	memento/icube-ai-agent-storage[-<userId>]  {currentSessionId, list:[session]} (agent / "builder" mode)
//	memento/icube-ai-ng-chat-storage[-<userId>] {currentSessionId, list:[session]} (chat mode)
//	chatHistoryNeedToBeMigrated-<userId>       [session] (chat mode, pre-migration copy)
//
// session = {sessionId, title, name, type, createdAt, updatedAt (ms),
// hasMore, nextPageToken, messages:[{role, content, timestamp (ms), status,
// agentTaskContent{proposal, proposalReasoningContent, guideline{thought,
// planItems[{toolName, thought, reasoningContent, params, result{data,
// error_message}}]}}}]}.
//
// Rules learned from real files (Trae 1.x, 2025):
//   - One conversation can appear under several keys of the same database
//     (the agent list keeps a title-only stub of a chat-mode session; the
//     per-user key and the plain key can both hold an agent session). Copies
//     are merged by sessionId: the copy with the most messages provides the
//     messages, titles and times are combined.
//   - The store is paged: a session with hasMore=true holds only the latest
//     page (20 messages) and gets Coverage partial; the rest lives in
//     ModularData\ai-agent\database.db, which is encrypted and not read.
//   - Agent-mode assistant messages usually have an empty content: their text
//     is agentTaskContent.proposal, guideline.thought and each plan item's
//     thought; plan items with a toolName become tool calls (and outputs from
//     result.data / result.error_message); the "finish" tool's summary is
//     assistant text. reasoningContent / proposalReasoningContent (the model's
//     thinking) and exception notices are never stored.
//   - title (= name) is the first prompt of the session, up to thousands of
//     characters; it is shortened to 100 runes and marked TitlePrompt.
//   - Sessions with neither messages nor a title (empty drafts) are skipped;
//     a titled session without messages is kept with Coverage titles.
//   - The prompt history keys (icube-ai-agent-storage-input-history*) and
//     UI state (ChatStore) are not read.
package trae

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

const (
	id = "trae"
	// dbName is the VS Code state database in each storage folder.
	dbName = "state.vscdb"
	// workspaceFile names the project of a workspaceStorage folder.
	workspaceFile = "workspace.json"
	// extraHost is the Source.Extra key naming the %APPDATA% host folder.
	extraHost = "host"
	// extraScope is the Source.Extra key: "workspace" or "global".
	extraScope = "scope"
)

type adapter struct{}

// New returns the Trae adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return id }
func (adapter) Name() string { return "Trae" }
func (adapter) Version() int { return 1 }

// hosts returns the %APPDATA% folders of Trae builds ("Trae", "Trae CN",
// "TRAE SOLO CN", ...), sorted by name.
func hosts(appData string) []string {
	if appData == "" {
		return nil
	}
	entries, err := os.ReadDir(appData)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(strings.ToLower(e.Name()), "trae") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Sources lists every Trae state.vscdb: one per workspaceStorage folder and
// the globalStorage one. Missing folders are skipped silently.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	for _, host := range hosts(env.AppData) {
		user := filepath.Join(env.AppData, host, "User")
		wsRoot := filepath.Join(user, "workspaceStorage")
		if entries, err := os.ReadDir(wsRoot); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				dir := filepath.Join(wsRoot, e.Name())
				db := filepath.Join(dir, dbName)
				if !isFile(db) {
					continue
				}
				files := fsread.SQLiteSidecars(db)
				if ws := filepath.Join(dir, workspaceFile); isFile(ws) {
					files = append(files, ws)
				}
				out = append(out, adapters.Source{
					Adapter: id, Path: db, Files: files,
					Extra: map[string]string{extraHost: host, extraScope: "workspace"},
				})
			}
		}
		if db := filepath.Join(user, "globalStorage", dbName); isFile(db) {
			out = append(out, adapters.Source{
				Adapter: id, Path: db, Files: fsread.SQLiteSidecars(db),
				Extra: map[string]string{extraHost: host, extraScope: "global"},
			})
		}
	}
	return out, nil
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// Caps: Trae has no command line or deep link that opens one chat, so Athar
// opens the project in Trae and the user picks the chat from its history.
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		App: &adapters.AppCommand{
			Name:    "Trae",
			Program: "Trae.exe",
			Args:    []string{"{cwd}"},
			Candidates: []string{
				`%LOCALAPPDATA%\Programs\Trae\Trae.exe`,
				`%LOCALAPPDATA%\Programs\Trae CN\Trae CN.exe`,
			},
		},
	}
}
