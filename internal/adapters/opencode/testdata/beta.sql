-- Synthetic rows for opencode-beta.db, a channel-specific database (after schema.sql).
INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
	VALUES ('ses_gamma','global','gamma','C:/work/gamma','Gamma channel session','1.3.0-beta',1785585600000,1785585700000);
INSERT INTO message VALUES
	('msg_x1','ses_gamma',1785585600000,1785585600000,'{"role":"user","time":{"created":1785585600000}}'),
	('msg_x2','ses_gamma',1785585601000,1785585700000,'{"role":"assistant","time":{"created":1785585601000}}');
INSERT INTO part VALUES
	('prt_x1','msg_x1','ses_gamma',1785585600000,1785585600000,'{"type":"text","text":"hello from the beta channel"}'),
	('prt_x2','msg_x2','ses_gamma',1785585601000,1785585601000,'{"type":"text","text":"hi"}');
