// Package zcode reads ZCode (Z.ai's coding agent, desktop app + bundled CLI)
// sessions.
//
// Layout (verified against ZCode 3.1.8 with CLI 0.14.9 on the owner's PC):
//
//	%USERPROFILE%\.zcode\cli\db\db.sqlite            the conversation store (WAL)
//	%USERPROFILE%\.zcode\v2\tasks-index.sqlite       the desktop app's task list
//	%USERPROFILE%\.zcode\cli\rollout\model-io-*.jsonl raw model request logs (not read)
//
// db.sqlite has an OpenCode-like schema with its own additions:
//   - session(id "sess_…", project_id, parent_id, directory, title,
//     title_source default|first_input|generated|custom, task_type
//     interactive|fork|workflow_parent|workflow_child|subagent_child|
//     nested_workflow_child, time_created, time_updated) — times are Unix ms,
//     directory is the verbatim Windows launch folder.
//   - message(id, session_id, time_created, data JSON {role user|assistant,
//     time{created}, synthetic, visibility user-visible|model-only, source,
//     summary, …}).
//   - part(id, message_id, time_created, data JSON {type text|reasoning|tool|
//     file|subagent|compaction|step-start|step-finish|snapshot|retry|patch, …}).
//     Part ids are NOT time-sortable, so parts are ordered by time_created.
//   - input_history(session_id, project_id, text, kind "prompt", time_created):
//     every prompt the user typed.
//
// Mapping: Cwd = session.directory. Titles: custom → TitleUser, generated →
// TitleAgent, first_input → TitlePrompt, default (a placeholder) → the first
// prompt. A rename in the desktop app (tasks.title_overridden = 1, not
// deleted) wins as TitleUser; the task index is part of the fingerprint.
//
// Skipped on purpose: child sessions (task types *_child, and any non-fork
// session with a parent_id), reasoning parts, step markers, snapshots,
// synthetic or ignored text parts, synthetic / model-only user messages (todo
// reminders, goal continuations), and compaction summaries (the message a
// compaction part names in summaryMessageId, or data.summary = true).
//
// input_history fills sessions that have no message rows (Coverage prompts)
// and yields prompt-log sessions (Origin prompt-log) for prompts whose session
// row no longer exists; their Cwd is the directory of another session of the
// same project when there is one.
//
// There is no terminal resume: ZCode's CLI is bundled inside the app and not
// on PATH. Enter starts the ZCode app, where the user picks the task.
package zcode

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// ID is the adapter id stored in the archive.
const ID = "zcode"

type adapter struct{}

// New returns the ZCode adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return ID }
func (adapter) Name() string { return "ZCode" }

// Version is bumped whenever parsing changes.
func (adapter) Version() int { return 1 }

// Caps: no terminal resume; Enter starts the ZCode desktop app.
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		App: &adapters.AppCommand{
			Name:       "ZCode",
			Program:    "ZCode.exe",
			Candidates: []string{`%LOCALAPPDATA%\Programs\ZCode\ZCode.exe`},
		},
	}
}

// dbPath is ZCode's conversation database for a data root (%USERPROFILE%\.zcode).
func dbPath(root string) string { return filepath.Join(root, "cli", "db", "db.sqlite") }

// tasksIndexPath is the desktop app's task index for a data root.
func tasksIndexPath(root string) string { return filepath.Join(root, "v2", "tasks-index.sqlite") }

// rootOf returns the data root of a db.sqlite path (<root>\cli\db\db.sqlite).
func rootOf(db string) string { return filepath.Dir(filepath.Dir(filepath.Dir(db))) }

// Sources returns the one conversation database, fingerprinted together with
// the task index (whose renames change titles). A missing database is skipped.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	if env.Home == "" {
		return nil, nil
	}
	root := filepath.Join(env.Home, ".zcode")
	db := dbPath(root)
	info, err := os.Stat(db)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	files := fsread.SQLiteSidecars(db)
	if ti := tasksIndexPath(root); fileExists(ti) {
		files = append(files, fsread.SQLiteSidecars(ti)...)
	}
	return []adapters.Source{{Adapter: ID, Path: db, Files: files}}, nil
}

// Read reads the database through a private copy (fsread.CopySQLite) and
// applies the desktop app's title renames when its task index is readable.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	res, err := readStore(ctx, src.Path, env.TempDir)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	if ti := tasksIndexPath(rootOf(src.Path)); fileExists(ti) {
		titles, err := readTaskTitles(ctx, ti, env.TempDir)
		if err != nil {
			if ctx.Err() != nil {
				return adapters.ReadResult{}, ctx.Err()
			}
			res.Warnings = append(res.Warnings, "task index not read (renamed titles missing): "+err.Error())
		}
		applyTaskTitles(res.Sessions, titles)
	}
	return res, nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
