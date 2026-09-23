-- Synthetic ZCode sessions (times are Unix ms; 1782000000000 = 2026-06-21T00:00:00Z).
-- Part ids deliberately sort opposite to their time_created: parts are ordered by time.

-- sess_alpha: a full interactive session with an agent-generated title.
INSERT INTO session (id, project_id, parent_id, slug, directory, path, title, version, time_created, time_updated, task_type, title_source)
VALUES ('sess_alpha', 'proj_1', NULL, 'sess_alpha', 'C:\work\alpha', 'C:\work\alpha', 'Fix the build', '0.14.9', 1782000000000, 1782000900000, 'interactive', 'generated');

INSERT INTO message VALUES ('msg_a01', 'sess_alpha', 1782000001000, 1782000001000,
  json_object('role','user','time',json_object('created',1782000001000),'agent','build',
    'contextSnapshot',json_object('envInfo',json_object('cwd','C:\work\alpha'))));
INSERT INTO part VALUES ('part_a01_z', 'msg_a01', 'sess_alpha', 1782000001001, 1782000001001,
  json_object('type','text','text','hello there','time',json_object('start',1782000001001,'end',1782000001001)));
INSERT INTO part VALUES ('part_a01_a', 'msg_a01', 'sess_alpha', 1782000001002, 1782000001002,
  json_object('type','text','text','inlined file contents','synthetic',json('true')));

INSERT INTO message VALUES ('msg_a02', 'sess_alpha', 1782000002000, 1782000002700,
  json_object('role','assistant','time',json_object('created',1782000002000,'completed',1782000002700),
    'parentID','msg_a01','finish','stop','path',json_object('cwd','C:\work\alpha','root','C:\work\alpha')));
INSERT INTO part VALUES ('part_a02_q', 'msg_a02', 'sess_alpha', 1782000002001, 1782000002001, json_object('type','step-start'));
INSERT INTO part VALUES ('part_a02_p', 'msg_a02', 'sess_alpha', 1782000002002, 1782000002002,
  json_object('type','reasoning','text','private chain of thought'));
INSERT INTO part VALUES ('part_a02_o', 'msg_a02', 'sess_alpha', 1782000002003, 1782000002003,
  json_object('type','text','text','Running the build.','time',json_object('start',1782000002003,'end',1782000002003)));
INSERT INTO part VALUES ('part_a02_n', 'msg_a02', 'sess_alpha', 1782000002004, 1782000002500,
  json_object('type','tool','tool','Bash','callID','call_1','state',json_object('status','completed',
    'input',json_object('command','go build ./...','description','Build'),'output','build ok','title','Build',
    'time',json_object('start',1782000002004,'end',1782000002500))));
INSERT INTO part VALUES ('part_a02_m', 'msg_a02', 'sess_alpha', 1782000002600, 1782000002600,
  json_object('type','text','text','Build passed.'));
INSERT INTO part VALUES ('part_a02_l', 'msg_a02', 'sess_alpha', 1782000002700, 1782000002700, json_object('type','step-finish','reason','stop'));

-- A model-only reminder injected by ZCode: not conversation text.
INSERT INTO message VALUES ('msg_a03', 'sess_alpha', 1782000003000, 1782000003000,
  json_object('role','user','time',json_object('created',1782000003000),'synthetic',json('true'),'visibility','model-only','source','todo_reminder'));
INSERT INTO part VALUES ('part_a03', 'msg_a03', 'sess_alpha', 1782000003001, 1782000003001, json_object('type','text','text','todo reminder'));

-- A failed model call: only a step-start, nothing to archive.
INSERT INTO message VALUES ('msg_a04', 'sess_alpha', 1782000004000, 1782000004000,
  json_object('role','assistant','time',json_object('created',1782000004000),'error',json_object('name','AiSdkModelAdapterError','data',json_object('message','boom'))));
INSERT INTO part VALUES ('part_a04', 'msg_a04', 'sess_alpha', 1782000004001, 1782000004001, json_object('type','step-start'));

INSERT INTO message VALUES ('msg_a05', 'sess_alpha', 1782000005000, 1782000005000,
  json_object('role','user','time',json_object('created',1782000005000)));
INSERT INTO part VALUES ('part_a05_b', 'msg_a05', 'sess_alpha', 1782000005001, 1782000005001,
  json_object('type','text','text','راجع المشروع من فضلك'));
INSERT INTO part VALUES ('part_a05_a', 'msg_a05', 'sess_alpha', 1782000005002, 1782000005002,
  json_object('type','file','mime','text/plain','filename','notes.txt','url','file:///C:/work/alpha/notes.txt'));

INSERT INTO message VALUES ('msg_a06', 'sess_alpha', 1782000006000, 1782000006900,
  json_object('role','assistant','time',json_object('created',1782000006000),'finish','tool-calls'));
INSERT INTO part VALUES ('part_a06_c', 'msg_a06', 'sess_alpha', 1782000006001, 1782000006001,
  json_object('type','tool','tool','Read','state',json_object('status','error',
    'input',json_object('file_path','C:\work\alpha\missing.go'),'error','file not found',
    'time',json_object('start',1782000006001,'end',1782000006002))));
INSERT INTO part VALUES ('part_a06_b', 'msg_a06', 'sess_alpha', 1782000006100, 1782000006100,
  json_object('type','tool','tool','Bash','state',json_object('status','completed',
    'input',json_object('command','dir /s'),'output',hex(zeroblob(1500)))));
INSERT INTO part VALUES ('part_a06_a', 'msg_a06', 'sess_alpha', 1782000006200, 1782000006200,
  json_object('type','tool','tool','TodoWrite','state',json_object('status','completed',
    'input',json_object('todos',json_array(json_object('content','step one','status','pending'))),'output','')));

-- Compaction: the user-side marker names the summary message, which repeats earlier content.
INSERT INTO message VALUES ('msg_a07', 'sess_alpha', 1782000007000, 1782000007000,
  json_object('role','user','time',json_object('created',1782000007000)));
INSERT INTO part VALUES ('part_a07', 'msg_a07', 'sess_alpha', 1782000007001, 1782000007001,
  json_object('type','compaction','auto',json('true'),'summaryMessageId','msg_a08'));
INSERT INTO message VALUES ('msg_a08', 'sess_alpha', 1782000008000, 1782000008000,
  json_object('role','assistant','time',json_object('created',1782000008000)));
INSERT INTO part VALUES ('part_a08', 'msg_a08', 'sess_alpha', 1782000008001, 1782000008001,
  json_object('type','text','text','Summary of the earlier conversation'));

-- One unreadable part (skipped) next to a good one.
INSERT INTO message VALUES ('msg_a09', 'sess_alpha', 1782000009000, 1782000009000,
  json_object('role','user','time',json_object('created',1782000009000)));
INSERT INTO part VALUES ('part_a09_b', 'msg_a09', 'sess_alpha', 1782000009001, 1782000009001, '{not json');
INSERT INTO part VALUES ('part_a09_a', 'msg_a09', 'sess_alpha', 1782000009002, 1782000009002,
  json_object('type','text','text','last question'));

-- One unreadable message (skipped with its parts).
INSERT INTO message VALUES ('msg_a10', 'sess_alpha', 1782000010000, 1782000010000, 'not json at all');
INSERT INTO part VALUES ('part_a10', 'msg_a10', 'sess_alpha', 1782000010001, 1782000010001,
  json_object('type','text','text','orphan text'));

-- sess_beta: title is the first input; directory stored with forward slashes.
INSERT INTO session (id, project_id, parent_id, slug, directory, path, title, version, time_created, time_updated, task_type, title_source)
VALUES ('sess_beta', 'proj_2', NULL, 'sess_beta', 'D:/proj/beta', NULL, '  hi   there', '0.14.9', 1782100000000, 1782100500000, 'interactive', 'first_input');
INSERT INTO message VALUES ('msg_b01', 'sess_beta', 1782100001000, 1782100001000,
  json_object('role','user','time',json_object('created',1782100001000)));
INSERT INTO part VALUES ('part_b01', 'msg_b01', 'sess_beta', 1782100001001, 1782100001001,
  json_object('type','text','text','hi there, please help'));
INSERT INTO message VALUES ('msg_b02', 'sess_beta', 1782100002000, 1782100002000,
  json_object('role','assistant','time',json_object('created',1782100002000)));
INSERT INTO part VALUES ('part_b02', 'msg_b02', 'sess_beta', 1782100002001, 1782100002001,
  json_object('type','text','text','Sure.'));

-- sess_child: a subagent child session (skipped).
INSERT INTO session (id, project_id, parent_id, slug, directory, path, title, version, time_created, time_updated, task_type, title_source)
VALUES ('sess_child', 'proj_1', 'sess_alpha', 'sess_child', 'C:\work\alpha', NULL, 'Child', '0.14.9', 1782000002100, 1782000002200, 'subagent_child', 'generated');
INSERT INTO message VALUES ('msg_c01', 'sess_child', 1782000002101, 1782000002101,
  json_object('role','user','time',json_object('created',1782000002101)));
INSERT INTO part VALUES ('part_c01', 'msg_c01', 'sess_child', 1782000002102, 1782000002102,
  json_object('type','text','text','sub prompt'));

-- sess_oldchild: a child written before task_type existed (default 'interactive' + parent_id): skipped.
INSERT INTO session (id, project_id, parent_id, slug, directory, path, title, version, time_created, time_updated, task_type, title_source)
VALUES ('sess_oldchild', 'proj_1', 'sess_alpha', 'sess_oldchild', 'C:\work\alpha', NULL, 'Old child', '0.12.0', 1782000002300, 1782000002400, 'interactive', 'generated');
INSERT INTO message VALUES ('msg_o01', 'sess_oldchild', 1782000002301, 1782000002301,
  json_object('role','user','time',json_object('created',1782000002301)));
INSERT INTO part VALUES ('part_o01', 'msg_o01', 'sess_oldchild', 1782000002302, 1782000002302,
  json_object('type','text','text','old sub prompt'));

-- sess_fork: a fork of sess_alpha that the user renamed (kept despite parent_id).
INSERT INTO session (id, project_id, parent_id, slug, directory, path, title, version, time_created, time_updated, task_type, title_source)
VALUES ('sess_fork', 'proj_1', 'sess_alpha', 'sess_fork', 'C:\work\alpha', NULL, 'My fork', '0.14.9', 1782200000000, 1782200100000, 'fork', 'custom');
INSERT INTO message VALUES ('msg_f01', 'sess_fork', 1782200001000, 1782200001000,
  json_object('role','user','time',json_object('created',1782200001000),'source','fork'));
INSERT INTO part VALUES ('part_f01', 'msg_f01', 'sess_fork', 1782200001001, 1782200001001,
  json_object('type','text','text','forked question'));

-- sess_prompts: no message rows, only input history (coverage prompts).
INSERT INTO session (id, project_id, parent_id, slug, directory, path, title, version, time_created, time_updated, task_type, title_source)
VALUES ('sess_prompts', 'proj_1', NULL, 'sess_prompts', 'C:\work\alpha', NULL, 'New task', '0.14.9', 1782300000000, 1782300000000, 'interactive', 'default');

-- sess_empty: nothing at all (dropped).
INSERT INTO session (id, project_id, parent_id, slug, directory, path, title, version, time_created, time_updated, task_type, title_source)
VALUES ('sess_empty', 'proj_1', NULL, 'sess_empty', 'C:\work\alpha', NULL, 'New task', '0.14.9', 1782400000000, 1782400000000, 'interactive', 'default');

INSERT INTO input_history VALUES ('input_1', 'proj_1', 'sess_alpha', 'hello there', 'prompt', 1782000000800, NULL);
INSERT INTO input_history VALUES ('input_2', 'proj_1', 'sess_prompts', 'first   prompt only', 'prompt', 1782300001000, NULL);
INSERT INTO input_history VALUES ('input_3', 'proj_1', 'sess_prompts', 'second prompt', 'prompt', 1782300002000, NULL);
INSERT INTO input_history VALUES ('input_4', 'proj_1', 'sess_gone', 'prompt of a deleted session', 'prompt', 1782500001000, NULL);
INSERT INTO input_history VALUES ('input_5', 'proj_1', NULL, 'unattributed prompt', 'prompt', 1782500002000, NULL);
INSERT INTO input_history VALUES ('input_6', 'proj_1', 'sess_child', 'sub prompt', 'prompt', 1782000002101, NULL);
INSERT INTO input_history VALUES ('input_7', 'proj_9', 'sess_gone2', 'prompt from an unknown project', 'prompt', 1782600001000, NULL);
INSERT INTO input_history VALUES ('input_8', 'proj_9', 'sess_gone2', '   ', 'prompt', 1782600002000, NULL);
