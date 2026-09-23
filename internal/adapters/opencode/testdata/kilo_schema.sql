-- Synthetic Kilo 7 database schema: the OpenCode schema family, with fewer
-- session columns (no path, agent, model, cost or token columns).
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
INSERT INTO project VALUES ('global','/',NULL,NULL,NULL,NULL,1784019600000,1784019600000,NULL,'[]',NULL);
INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
	VALUES ('ses_kilo1','global','calm-kilo','C:/work/kilo','Kilo task title','7.4.21',1784019600000,1784019700000);
INSERT INTO message VALUES
	('msg_k1','ses_kilo1',1784019600000,1784019600000,'{"role":"user","time":{"created":1784019600000}}'),
	('msg_k2','ses_kilo1',1784019601000,1784019700000,'{"role":"assistant","time":{"created":1784019601000,"completed":1784019700000}}');
INSERT INTO part VALUES
	('prt_k1','msg_k1','ses_kilo1',1784019600000,1784019600000,'{"type":"text","text":"explain the build script"}'),
	('prt_k2','msg_k2','ses_kilo1',1784019601000,1784019601000,'{"type":"text","text":"It compiles the project."}');
