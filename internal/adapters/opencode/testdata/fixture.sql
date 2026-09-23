-- Synthetic rows for opencode.db (applied after schema.sql). No real data.
INSERT INTO project VALUES ('global','/',NULL,NULL,NULL,NULL,NULL,1783850400000,1783850400000,NULL,'[]',NULL);

-- ses_alpha: agent title, a synthetic part (skipped), an image, reasoning
-- (dropped), tools, a 3000-byte tool output (kept to 2048 bytes).
INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
	VALUES ('ses_alpha','global','brave-alpha','C:/work/alpha','Refactor the parser module','1.2.0',1783850400000,1783850460000);
INSERT INTO message VALUES
	('msg_a1','ses_alpha',1783850400000,1783850400000,'{"role":"user","time":{"created":1783850400000},"agent":"build","model":{"providerID":"p","modelID":"m"}}'),
	('msg_a2','ses_alpha',1783850401000,1783850420000,'{"role":"assistant","time":{"created":1783850401000,"completed":1783850420000},"parentID":"msg_a1","modelID":"m","providerID":"p","mode":"build","agent":"build","path":{"cwd":"C:/work/alpha","root":"C:/work/alpha"},"cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"finish":"stop"}'),
	('msg_a3','ses_alpha',1783850450000,1783850450000,'{"role":"user","time":{"created":1783850450000}}'),
	('msg_a4','ses_alpha',1783850451000,1783850460000,'{"role":"assistant","time":{"created":1783850451000,"completed":1783850460000},"parentID":"msg_a3"}');
INSERT INTO part VALUES
	('prt_a1a','msg_a1','ses_alpha',1783850400000,1783850400000,'{"type":"text","text":"Please refactor the parser in parser.go"}'),
	('prt_a1b','msg_a1','ses_alpha',1783850400000,1783850400000,'{"type":"text","synthetic":true,"text":"Called the Read tool with the following input: {\"filePath\":\"parser.go\"}"}'),
	('prt_a1c','msg_a1','ses_alpha',1783850400000,1783850400000,'{"type":"file","mime":"image/png","filename":"screen.png","url":"data:image/png;base64,AAAA"}'),
	('prt_a2a','msg_a2','ses_alpha',1783850401000,1783850401000,'{"type":"step-start","snapshot":"abc123"}'),
	('prt_a2b','msg_a2','ses_alpha',1783850401000,1783850402000,'{"type":"reasoning","text":"PRIVATE-REASONING must never be archived","time":{"start":1783850401000,"end":1783850402000}}'),
	('prt_a2c','msg_a2','ses_alpha',1783850402000,1783850403000,'{"type":"text","text":"I will run the tests first.","time":{"start":1783850402000,"end":1783850403000}}');
INSERT INTO part VALUES
	('prt_a2d','msg_a2','ses_alpha',1783850403000,1783850404000,
	 json_object('type','tool','callID','call_1','tool','bash',
		'state',json_object('status','completed',
			'input',json_object('command','go test ./...','description','Run the tests'),
			'output',replace(hex(zeroblob(1500)),'00','ok'),
			'title','Run the tests','metadata',json_object('exit',0),
			'time',json_object('start',1783850403000,'end',1783850404000))));
INSERT INTO part VALUES
	('prt_a2e','msg_a2','ses_alpha',1783850405000,1783850405000,'{"type":"text","text":"All tests pass."}'),
	('prt_a2f','msg_a2','ses_alpha',1783850405000,1783850405000,'{"type":"step-finish","reason":"stop","cost":0,"tokens":{"input":1,"output":1}}'),
	('prt_a2g','msg_a2','ses_alpha',1783850405000,1783850405000,'{"type":"patch","hash":"abc123","files":["C:/work/alpha/parser.go"]}'),
	('prt_a3a','msg_a3','ses_alpha',1783850450000,1783850450000,'{"type":"text","text":"Now add a README"}'),
	('prt_a4a','msg_a4','ses_alpha',1783850452000,1783850453000,'{"type":"tool","callID":"call_2","tool":"write","state":{"status":"completed","input":{"filePath":"C:/work/alpha/README.md","content":"# Alpha"},"output":"Wrote file successfully.","time":{"start":1783850452000,"end":1783850453000}}}'),
	('prt_a4b','msg_a4','ses_alpha',1783850459000,1783850459000,'{"type":"text","text":"Done."}'),
	('prt_a5a','msg_a5','ses_alpha',1783850460000,1783850460000,'{"type":"text","text":"COMPACTION-SUMMARY of the conversation so far"}');
-- msg_a5: a compaction summary written by the agent (summary=true): not archived.
INSERT INTO message VALUES
	('msg_a5','ses_alpha',1783850460000,1783850460000,'{"role":"assistant","summary":true,"time":{"created":1783850460000}}');

-- ses_beta: default "New session - <ISO>" title (falls back to the first prompt),
-- Arabic text, a failed tool, a part and a message with broken JSON (skipped).
INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
	VALUES ('ses_beta','global','quiet-beta','D:/Projects/beta','New session - 2026-07-13T08:00:00.000Z','1.2.0',1783929600000,1783929660000);
INSERT INTO message VALUES
	('msg_b1','ses_beta',1783929600000,1783929600000,'{"role":"user","time":{"created":1783929600000}}'),
	('msg_b2','ses_beta',1783929601000,1783929650000,'{"role":"assistant","time":{"created":1783929601000,"completed":1783929650000}}'),
	('msg_b3','ses_beta',1783929655000,1783929655000,'{"role":"user", broken');
INSERT INTO part VALUES
	('prt_b1a','msg_b1','ses_beta',1783929600000,1783929600000,'{"type":"text","text":"  كيف أنشر   المشروع\nعلى الخادم؟  "}'),
	('prt_b2a','msg_b2','ses_beta',1783929602000,1783929603000,'{"type":"tool","callID":"call_3","tool":"webfetch","state":{"status":"error","input":{"url":"https://example.com/docs","format":"markdown"},"error":"timeout","time":{"start":1783929602000,"end":1783929603000}}}'),
	('prt_b2b','msg_b2','ses_beta',1783929604000,1783929604000,'not json'),
	('prt_b2c','msg_b2','ses_beta',1783929605000,1783929605000,'{"type":"text","text":"استخدم الأمر deploy"}'),
	('prt_b3a','msg_b3','ses_beta',1783929655000,1783929655000,'{"type":"text","text":"orphaned by a broken message"}');

-- ses_child: a subagent session (parent_id set): skipped entirely.
INSERT INTO session (id, project_id, parent_id, slug, directory, title, version, time_created, time_updated)
	VALUES ('ses_child','global','ses_alpha','child-alpha','C:/work/alpha','Child session - 2026-07-12T10:00:30.000Z','1.2.0',1783850430000,1783850440000);
INSERT INTO message VALUES
	('msg_c1','ses_child',1783850430000,1783850430000,'{"role":"user","time":{"created":1783850430000}}');
INSERT INTO part VALUES
	('prt_c1a','msg_c1','ses_child',1783850430000,1783850430000,'{"type":"text","text":"subagent prompt"}');

-- ses_empty: never used; no messages, so nothing to archive.
INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
	VALUES ('ses_empty','global','empty-one','C:/work/alpha','New session - 2026-07-14T09:00:00.000Z','1.2.0',1784019600000,1784019600000);

-- ses_big: archived in OpenCode (still history), one user text part of 2 MB.
INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated, time_archived)
	VALUES ('ses_big','global','big-paste','C:/work/big','Big paste','1.2.0',1784019600000,1784019610000,1784019620000);
INSERT INTO message VALUES
	('msg_g1','ses_big',1784019600000,1784019600000,'{"role":"user","time":{"created":1784019600000}}');
INSERT INTO part VALUES
	('prt_g1a','msg_g1','ses_big',1784019600000,1784019600000,
	 json_object('type','text','text',replace(hex(zeroblob(1048576)),'00','ab')));
