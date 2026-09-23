// Package agy reads the history of Google Antigravity CLI ("agy").
//
// Data root: %USERPROFILE%\.gemini\antigravity-cli (agy 1.0.9 has no
// environment variable that moves it). Sources, all read-only:
//
//   - conversations\<id>.db: one SQLite database per conversation, read only
//     through fsread.CopySQLite. The file stem is the conversation id (the
//     cascade id, also field 6 of the metadata blob). Tables used:
//     trajectory_metadata_blob (id='main'; protobuf: 1 = {1: workspace file://
//     URI}, 2 = created Timestamp, 7 = workspace URI) and steps (idx, step_type,
//     metadata, step_payload; protobuf blobs). Step type 14 is a user input
//     (payload 19.2 = text, or the texts of the 19.3 items), 15 a planner
//     response (payload 20.1 = response text, 20.8 = modified response,
//     20.7 = tool calls {2: name, 3: arguments JSON}; 20.3 is thinking and is
//     never read). Every step's metadata field 1 is its creation time. Other
//     step types (tool executions, notifications) only count for the dates;
//     tool outputs are not extracted. A database with no steps (created but
//     never used) yields no session. Damaged blobs or steps are skipped and
//     counted; they never fail the source.
//   - brain\<id>\.system_generated\messages\*.json: notifications delivered to
//     the conversation ({sender, recipient, timestamp, content, sourceMetadata});
//     part of the conversation's source (fingerprinted with the database) and
//     merged into its messages by time: tool notifications as tool outputs,
//     others as system messages. read.json (read flags) is ignored. A file that
//     is not JSON and changed less than 5 s ago (still being written) makes Read
//     return an error wrapping adapters.ErrRetryLater.
//   - history.jsonl: the prompt log {display, timestamp (ms), workspace (path),
//     conversationId, type}. One prompts-coverage session per conversation id
//     (Origin prompt-log); the archive merges it into the database session.
//     "slash_command" lines are ignored; lines without a conversationId (older
//     agy) are counted as skipped.
//
// The databases' own transcripts (brain\<id>\.system_generated\logs\*.jsonl)
// are empty in agy 1.0.9 and are not read. Titles are the first user prompt:
// agy stores no readable summary title.
package agy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// agentID is the adapter id stored in the archive.
const agentID = "agy"

type adapter struct{}

// New returns the Antigravity CLI adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return agentID }
func (adapter) Name() string { return "Antigravity CLI" }
func (adapter) Version() int { return 1 }

// dataRoot is agy's data folder.
func dataRoot(env adapters.Env) string {
	return filepath.Join(env.Home, ".gemini", "antigravity-cli")
}

// Sources lists one source per conversation database (with its sidecars and
// brain notification files) and one for history.jsonl.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	if env.Home == "" {
		return nil, nil
	}
	root := dataRoot(env)
	var out []adapters.Source

	convDir := filepath.Join(root, "conversations")
	entries, err := os.ReadDir(convDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.EqualFold(filepath.Ext(name), ".db") {
			continue
		}
		db := filepath.Join(convDir, name)
		id := strings.TrimSuffix(name, filepath.Ext(name))
		// An unreadable notifications folder is reported by Read, not here.
		notes, _ := jsonFiles(brainMessagesDir(root, id))
		out = append(out, adapters.Source{
			Adapter: agentID,
			Path:    db,
			Files:   append(fsread.SQLiteSidecars(db), notes...),
		})
	}

	hist := filepath.Join(root, "history.jsonl")
	if info, err := os.Stat(hist); err == nil && info.Mode().IsRegular() {
		out = append(out, adapters.Source{Adapter: agentID, Path: hist, Files: []string{hist}})
	}
	return out, nil
}

// Read parses one source returned by Sources.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	switch {
	case strings.EqualFold(filepath.Ext(src.Path), ".db"):
		return readConversation(ctx, env, src.Path)
	case strings.EqualFold(filepath.Base(src.Path), "history.jsonl"):
		return readHistory(ctx, src.Path)
	}
	return adapters.ReadResult{}, fmt.Errorf("agy: unknown source %s", src.Path)
}

// Caps: `agy --conversation <id>` resumes (agy 1.0.9 --help).
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		Resume:            []string{"agy", "--conversation", "{id}"},
		ProgramCandidates: []string{`%LOCALAPPDATA%\agy\bin\agy.exe`},
		SkipPermissions:   []string{"--dangerously-skip-permissions"},
	}
}

// jsonFiles lists the *.json files directly in dir, sorted by name; a missing
// dir is empty.
func jsonFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}
