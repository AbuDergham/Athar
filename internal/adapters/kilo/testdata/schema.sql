-- Synthetic Kilo 7 (kilo.db) schema: the OpenCode schema family as created by
-- Kilo's Drizzle migrations (tables and columns only; no real data).
CREATE TABLE project (
	id TEXT PRIMARY KEY, worktree TEXT NOT NULL, vcs TEXT, name TEXT, icon_url TEXT,
	icon_color TEXT, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	time_initialized INTEGER, sandboxes TEXT NOT NULL, commands TEXT
);
CREATE TABLE session (
	id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT, slug TEXT NOT NULL,
	directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL, share_url TEXT,
	summary_additions INTEGER, summary_deletions INTEGER, summary_files INTEGER,
	summary_diffs TEXT, revert TEXT, permission TEXT,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	time_compacting INTEGER, time_archived INTEGER, workspace_id TEXT
);
CREATE TABLE message (
	id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL
);
CREATE TABLE part (
	id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL
);
CREATE TABLE todo (
	session_id TEXT NOT NULL, content TEXT NOT NULL, status TEXT NOT NULL, priority TEXT NOT NULL,
	position INTEGER NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL
);
CREATE TABLE permission (
	project_id TEXT PRIMARY KEY, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL
);
INSERT INTO project VALUES ('global','/',NULL,NULL,NULL,NULL,1784019600000,1784019600000,NULL,'[]',NULL);
