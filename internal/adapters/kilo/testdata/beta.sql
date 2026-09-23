-- Synthetic session for a non-latest channel database (kilo-beta.db).
INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated) VALUES
	('ses_b01','global','swift-kite','C:/work/beta','Beta channel task','7.8.0-beta.1',1784300000000,1784300100000);
INSERT INTO message VALUES
	('msg_b01a','ses_b01',1784300000000,1784300000000,'{"role":"user","time":{"created":1784300000000}}');
INSERT INTO part VALUES
	('prt_b01a','msg_b01a','ses_b01',1784300000000,1784300000000,'{"type":"text","text":"try the beta build"}');
