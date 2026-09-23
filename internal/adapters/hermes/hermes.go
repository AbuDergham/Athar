// Package hermes reads Hermes Agent's local history (spec §7), read-only.
//
// Hermes (Nous Research; CLI, desktop app and messaging gateway) keeps every
// conversation in one SQLite database, state.db, in its home folder:
// %LOCALAPPDATA%\hermes on Windows, ~/.hermes elsewhere, or HERMES_HOME when
// set. Named profiles are homes of their own under <home>\profiles\<name>.
// The database is only ever read from a private copy (fsread.CopySQLite).
//
// Tables used: sessions(id, source, parent_session_id, model_config,
// end_reason, session_key, cwd, title, title_source, display_name,
// started_at, last_activity_at) and messages(id, session_id, role, content,
// timestamp, tool_calls, tool_name, tool_call_id, active, compacted,
// _compressed_summary, display_kind). Times are Unix seconds (REAL). Missing
// optional columns (older Hermes versions) are tolerated.
//
// What counts as one conversation follows Hermes' own session picker:
//   - Rows with a parent_session_id are compression continuations when the
//     parent ended with end_reason 'compression'; they are merged into the
//     chain's first session, whose id is the NativeID (`hermes --resume`
//     follows a compression root forward to its live tip itself).
//   - /branch children (model_config._branched_from, or a parent that ended
//     'branched' before the child started) and reset children
//     (model_config._reset_from, or a parent that ended at a reset boundary
//     with the same session_key) are conversations of their own, as is a
//     child whose parent row no longer exists.
//   - Delegates (model_config._delegate_from), any other child (sub-agent
//     runs) and headless sources (cron, subagent, tool, kanban) are skipped.
//
// Messages follow Hermes' display projection: live rows plus rows archived by
// compaction (active = 1 OR compacted = 1; rows taken back by undo/rewind are
// left out), with the copies compaction makes collapsed by (role, content,
// timestamp, tool ids). Compaction summaries, display_kind 'hidden' rows and
// reasoning are dropped; slash-skill scaffolding becomes "/skill — instruction"
// as the user typed it; "[System: …]" notes become system messages.
//
// Titles: sessions.title (title_source 'user' or NULL → user, 'llm' or
// 'derived' → agent; the best one along a compression chain), else the first
// prompt, else the gateway display_name (TitleNone).
package hermes

import (
	"github.com/AbuDergham/Athar/internal/adapters"
)

type adapter struct{}

// New returns the Hermes adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return "hermes" }
func (adapter) Name() string { return "Hermes" }
func (adapter) Version() int { return 1 }

// Caps: `hermes --resume <id>` restores the recorded working directory
// itself; --yolo skips approval prompts. The data root is selected with
// HERMES_HOME (Hermes reads no other variable for it).
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		Resume:            []string{"hermes", "--resume", "{id}"},
		ProgramCandidates: []string{`%LOCALAPPDATA%\hermes\bin\hermes.exe`},
		SkipPermissions:   []string{"--yolo"},
		RootEnv:           "HERMES_HOME",
	}
}
