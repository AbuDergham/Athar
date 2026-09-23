-- Synthetic OpenCode database schema (same tables and columns as the OpenCode
-- 1.x SQLite store). Tests build fixture databases from this script in WAL mode.
CREATE TABLE project (
	id TEXT PRIMARY KEY, worktree TEXT NOT NULL, vcs TEXT, name TEXT,
	icon_url TEXT, icon_url_override TEXT, icon_color TEXT,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	time_initialized INTEGER, sandboxes TEXT NOT NULL, commands TEXT
);
CREATE TABLE session (
	id TEXT PRIMARY KEY, project_id TEXT NOT NULL, workspace_id TEXT, parent_id TEXT,
	slug TEXT NOT NULL, directory TEXT NOT NULL, path TEXT, title TEXT NOT NULL,
	version TEXT NOT NULL, share_url TEXT,
	summary_additions INTEGER, summary_deletions INTEGER, summary_files INTEGER,
	summary_diffs TEXT, metadata TEXT,
	cost REAL NOT NULL DEFAULT 0, tokens_input INTEGER NOT NULL DEFAULT 0,
	tokens_output INTEGER NOT NULL DEFAULT 0, tokens_reasoning INTEGER NOT NULL DEFAULT 0,
	tokens_cache_read INTEGER NOT NULL DEFAULT 0, tokens_cache_write INTEGER NOT NULL DEFAULT 0,
	revert TEXT, permission TEXT, agent TEXT, model TEXT,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	time_compacting INTEGER, time_archived INTEGER
);
CREATE TABLE message (
	id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL
);
CREATE TABLE part (
	id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL
);
CREATE TABLE event (id TEXT PRIMARY KEY, aggregate_id TEXT NOT NULL, seq INTEGER NOT NULL, type TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE session_message (
	id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL, seq INTEGER NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL
);
CREATE TABLE todo (
	session_id TEXT NOT NULL, content TEXT NOT NULL, status TEXT NOT NULL, priority TEXT NOT NULL,
	position INTEGER NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL
);
CREATE INDEX message_session_idx ON message (session_id);
CREATE INDEX part_message_idx ON part (message_id);
CREATE INDEX part_session_idx ON part (session_id);
