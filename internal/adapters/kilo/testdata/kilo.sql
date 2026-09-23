-- Synthetic sessions for kilo.db: a titled session with a tool call, its
-- subagent (child) session (skipped), a session still carrying the
-- "New session - <time>" placeholder title (titled from its first prompt,
-- Arabic text), a session without messages (skipped) and a message with
-- broken JSON (skipped and counted).
INSERT INTO session (id, project_id, parent_id, slug, directory, title, version, time_created, time_updated) VALUES
	('ses_k01','global',NULL,'brave-otter','C:/work/kilo-app','Add a retry to the fetch helper','7.7.7',1784019600000,1784019660000),
	('ses_k02','global','ses_k01','quiet-heron','C:/work/kilo-app','Child session - 2026-07-14T09:00:30.000Z','7.7.7',1784019630000,1784019640000),
	('ses_k03','global',NULL,'bold-lynx','D:/notes','New session - 2026-07-15T09:00:00.000Z','7.7.7',1784106000000,1784106100000),
	('ses_k04','global',NULL,'empty-crow','C:/work/empty','Nothing here yet','7.7.7',1784200000000,1784200000000);
INSERT INTO message VALUES
	('msg_k01a','ses_k01',1784019600000,1784019600000,'{"role":"user","time":{"created":1784019600000}}'),
	('msg_k01b','ses_k01',1784019610000,1784019660000,'{"role":"assistant","time":{"created":1784019610000,"completed":1784019660000}}'),
	('msg_k01c','ses_k01',1784019620000,1784019620000,'{broken json'),
	('msg_k02a','ses_k02',1784019630000,1784019630000,'{"role":"user","time":{"created":1784019630000}}'),
	('msg_k03a','ses_k03',1784106000000,1784106000000,'{"role":"user","time":{"created":1784106000000}}'),
	('msg_k03b','ses_k03',1784106050000,1784106100000,'{"role":"assistant","time":{"created":1784106050000}}');
INSERT INTO part VALUES
	('prt_k01a','msg_k01a','ses_k01',1784019600000,1784019600000,'{"type":"text","text":"add a retry to fetchJSON"}'),
	('prt_k01b','msg_k01b','ses_k01',1784019611000,1784019611000,'{"type":"reasoning","text":"thinking about it"}'),
	('prt_k01c','msg_k01b','ses_k01',1784019612000,1784019615000,'{"type":"tool","callID":"call_1","tool":"bash","state":{"status":"completed","input":{"command":"go test ./..."},"output":"ok  example/fetch","time":{"start":1784019612000,"end":1784019615000}}}'),
	('prt_k01d','msg_k01b','ses_k01',1784019650000,1784019650000,'{"type":"text","text":"Added a retry with backoff."}'),
	('prt_k02a','msg_k02a','ses_k02',1784019630000,1784019630000,'{"type":"text","text":"subagent prompt"}'),
	('prt_k03a','msg_k03a','ses_k03',1784106000000,1784106000000,'{"type":"text","text":"لخّص   ملاحظات الاجتماع"}'),
	('prt_k03b','msg_k03b','ses_k03',1784106050000,1784106050000,'{"type":"text","text":"Here is a summary."}');
