-- Synthetic session for a database chosen with KILO_DB.
INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated) VALUES
	('ses_c01','global','lone-wolf','C:/work/custom','Custom database task','7.7.7',1784400000000,1784400050000);
INSERT INTO message VALUES
	('msg_c01a','ses_c01',1784400000000,1784400000000,'{"role":"user","time":{"created":1784400000000}}');
INSERT INTO part VALUES
	('prt_c01a','msg_c01a','ses_c01',1784400000000,1784400000000,'{"type":"text","text":"use the custom database"}');
