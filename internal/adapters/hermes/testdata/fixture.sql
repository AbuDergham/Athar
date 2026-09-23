-- Synthetic Hermes history (no real data). Times are Unix seconds (REAL):
--   T0 = 1788256800 (2026-09-01T10:00:00Z)   T1 = 1788339600 (2026-09-02T09:00:00Z)
--   T2 = 1788436800 (2026-09-03T12:00:00Z)   T3 = 1788602400 (2026-09-05T10:00:00Z)
INSERT INTO sessions (id, source, session_key, display_name, model_config, parent_session_id,
                      started_at, ended_at, end_reason, cwd, title, title_source, last_activity_at, hidden) VALUES
-- A: CLI session, AI title, tools, notes, a rewound prompt.
('20260901_100000_aaaaaa', 'cli', NULL, NULL, '{"model":"m1","provider":"p1"}', NULL,
 1788256800.5, NULL, NULL, 'C:\work\alpha', '  Deploy the   widget service ', 'llm', 1788257400.0, 0),
-- B: desktop session (no cwd), renamed by the user, compressed into C; hidden in the app.
('20260902_090000_bbbbbb', 'desktop', NULL, NULL, NULL, NULL,
 1788339600.0, 1788341400.0, 'compression', NULL, 'My renamed chat', 'user', 1788341400.0, 1),
-- C: compression continuation of B (merged into B).
('20260902_093000_cccccc', 'desktop', NULL, NULL, NULL, '20260902_090000_bbbbbb',
 1788341400.0, NULL, NULL, NULL, 'My renamed chat #2', 'derived', 1788341600.0, 0),
-- subagent child of B (skipped).
('20260902_091500_dddddd', 'subagent', NULL, NULL, NULL, '20260902_090000_bbbbbb',
 1788340500.0, 1788340600.0, 'completed', NULL, 'subagent run', 'llm', NULL, 0),
-- delegate child of B marked in model_config (skipped).
('20260902_091600_eeeeee', 'desktop', NULL, NULL, '{"_delegate_from":"20260902_090000_bbbbbb"}', '20260902_090000_bbbbbb',
 1788340560.0, NULL, NULL, NULL, NULL, NULL, NULL, 0),
-- F: /branch child of A (its own conversation), no title.
('20260903_120000_ffffff', 'cli', NULL, NULL, '{"_branched_from":"20260901_100000_aaaaaa","model":"m1"}', '20260901_100000_aaaaaa',
 1788436800.0, NULL, NULL, 'C:\work\alpha\sub', NULL, NULL, NULL, 0),
-- cron run (skipped: headless).
('20260904_060000_111111', 'cron', NULL, NULL, NULL, NULL,
 1788501600.0, 1788501700.0, 'completed', 'C:\work\alpha', 'cron job', 'llm', NULL, 0),
-- session without messages (skipped).
('20260904_070000_222222', 'cli', NULL, NULL, NULL, NULL,
 1788505200.0, NULL, NULL, 'C:\work\alpha', 'Empty one', 'user', NULL, 0),
-- tool-created session (skipped).
('20260904_080000_333333', 'tool', NULL, NULL, NULL, NULL,
 1788508800.0, NULL, NULL, NULL, NULL, NULL, NULL, 0),
-- H: gateway session ended by a reset; I: its reset child (own conversation); J: an ephemeral child (skipped).
('20260905_100000_444444', 'telegram', 'agent:main:telegram:dm:42', 'Chat Name', NULL, NULL,
 1788602400.0, 1788602500.0, 'session_reset', NULL, NULL, NULL, NULL, 0),
('20260905_110000_555555', 'telegram', 'agent:main:telegram:dm:42', 'Chat Name', NULL, '20260905_100000_444444',
 1788606000.0, NULL, NULL, NULL, NULL, NULL, NULL, 0),
('20260905_103000_666666', 'telegram', NULL, NULL, NULL, '20260905_100000_444444',
 1788604200.0, NULL, NULL, NULL, NULL, NULL, NULL, 0),
-- K: no title and no prompt: display name is the last resort.
('20260906_100000_777777', 'api_server', NULL, 'Display Name Only', NULL, NULL,
 1788688800.0, NULL, NULL, NULL, NULL, NULL, NULL, 0),
-- L: parent row missing: kept as its own conversation.
('20260906_110000_888888', 'cli', NULL, NULL, NULL, 'missing_parent_id',
 1788692400.0, NULL, NULL, 'C:\work\beta', NULL, NULL, NULL, 0);

INSERT INTO messages (id, session_id, role, content, tool_call_id, tool_calls, tool_name, timestamp,
                      reasoning, active, compacted, _compressed_summary, display_kind, display_order) VALUES
(1, '20260901_100000_aaaaaa', 'user', 'Please deploy the widget service', NULL, NULL, NULL, 1788256801.25, NULL, 1, 0, 0, NULL, NULL),
(2, '20260901_100000_aaaaaa', 'assistant', 'I will check the repo first.', NULL,
 '[{"id":"c1","call_id":"c1","response_item_id":"r1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"git status\",\"timeout\":30}"}},{"id":"c2","call_id":"c2","response_item_id":"r2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"C:\\\\work\\\\alpha\\\\main.go\"}"}}]',
 NULL, 1788256802.0, NULL, 1, 0, 0, NULL, NULL),
(3, '20260901_100000_aaaaaa', 'tool', '{"output": "On branch main\nnothing to commit", "exit_code": 0, "error": null}', 'c1', NULL, 'terminal', 1788256803.0, NULL, 1, 0, 0, NULL, NULL),
(4, '20260901_100000_aaaaaa', 'tool', '{"content": "package main", "total_lines": 1}', 'c2', NULL, 'read_file', 1788256804.0, NULL, 1, 0, 0, NULL, NULL),
(5, '20260901_100000_aaaaaa', 'assistant', NULL, NULL, NULL, NULL, 1788256805.0, 'private thoughts', 1, 0, 0, NULL, NULL),
(6, '20260901_100000_aaaaaa', 'user', '[System: The model was switched to m2.]', NULL, NULL, NULL, 1788256806.0, NULL, 1, 0, 0, 'model_switch', NULL),
(7, '20260901_100000_aaaaaa', 'assistant', 'scaffolding for the model', NULL, NULL, NULL, 1788256807.0, NULL, 1, 0, 0, 'hidden', NULL),
(8, '20260901_100000_aaaaaa', 'user', 'a prompt the user took back', NULL, NULL, NULL, 1788256808.0, NULL, 0, 0, 0, NULL, NULL),
(9, '20260901_100000_aaaaaa', 'assistant', 'Deployed. تم النشر بنجاح', NULL, NULL, NULL, 1788256809.0, NULL, 1, 0, 0, NULL, NULL),
(10, '20260901_100000_aaaaaa', 'tool', '{"output": "' || replace(hex(zeroblob(2500)), '00', 'ب') || '", "exit_code": 0}', 'c3', NULL, 'terminal', 1788256810.0, NULL, 1, 0, 0, NULL, NULL),
(11, '20260901_100000_aaaaaa', 'tool', 'plain text result', 'c4', NULL, 'web_search', 1788256811.0, NULL, 1, 0, 0, NULL, NULL),
(12, '20260901_100000_aaaaaa', 'tool', '{"success": false, "error": "file not found"}', 'c5', NULL, 'patch', 1788256812.0, NULL, 1, 0, 0, NULL, NULL),
(13, '20260901_100000_aaaaaa', 'assistant', '', NULL,
 '[{"type":"function","function":{"name":"web_search","arguments":{"query":"widget docs"}}},{"id":"c9","type":"function","function":{"name":"execute_code","arguments":"{\"code\":\"print(1)\"}"}}]',
 NULL, 1788256813.0, NULL, 1, 0, 0, NULL, NULL),
(14, '20260901_100000_aaaaaa', 'assistant', 'Done.', NULL, 'not json', NULL, 1788256814.0, NULL, 1, 0, 0, NULL, NULL),
(15, '20260901_100000_aaaaaa', 'user', '[System: note without a display kind]', NULL, NULL, NULL, 1788256815.0, NULL, 1, 0, 0, NULL, NULL),
(16, '20260901_100000_aaaaaa', 'user', char(0) || 'json:[{"type":"text","text":"Compare with this picture"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]', NULL, NULL, NULL, 1788256816.0, NULL, 1, 0, 0, NULL, NULL),

(20, '20260902_090000_bbbbbb', 'user',
 '[IMPORTANT: The user has invoked the "web-research" skill, indicating they want you to follow its instructions. The full skill content is loaded below.]' || char(10) || char(10) ||
 '# Web research' || char(10) || 'Long synthetic skill body.' || char(10) || char(10) ||
 'The user has provided the following instruction alongside the skill invocation: find telegram bots' || char(10) || char(10) ||
 '[Runtime note: synthetic]',
 NULL, NULL, NULL, 1788339610.0, NULL, 0, 1, 0, 'skill_invocation', NULL),
(21, '20260902_090000_bbbbbb', 'assistant', 'Here are three bots.', NULL, NULL, NULL, 1788339620.0, NULL, 0, 1, 0, NULL, 21),
(22, '20260902_090000_bbbbbb', 'user',
 '[IMPORTANT: The user has invoked the "summarize" skill, indicating they want you to follow its instructions. The full skill content is loaded below.]' || char(10) || 'Synthetic body.',
 NULL, NULL, NULL, 1788339630.0, NULL, 0, 1, 0, 'skill_invocation', NULL),
(23, '20260902_090000_bbbbbb', 'assistant', 'Summary of earlier turns', NULL, NULL, NULL, 1788339640.0, NULL, 1, 0, 1, NULL, NULL),
(24, '20260902_090000_bbbbbb', 'user', 'rewound in B', NULL, NULL, NULL, 1788339635.0, NULL, 0, 0, 0, NULL, NULL),
(25, '20260902_090000_bbbbbb', 'assistant', 'Here are three bots.', NULL, NULL, NULL, 1788339620.0, NULL, 1, 0, 0, NULL, 21),

(30, '20260902_093000_cccccc', 'user', '[CONTEXT COMPACTION — REFERENCE ONLY] earlier turns summarized', NULL, NULL, NULL, 1788341401.0, NULL, 1, 0, 0, NULL, NULL),
(31, '20260902_093000_cccccc', 'user', 'continue with step two', NULL, NULL, NULL, 1788341410.0, NULL, 1, 0, 0, NULL, NULL),
(32, '20260902_093000_cccccc', 'assistant', 'Step two done.', NULL, NULL, NULL, 1788341420.0, NULL, 1, 0, 0, NULL, NULL),
(33, '20260902_093000_cccccc', 'user', '[CONTEXT SUMMARY]: old summary' || char(10) || '--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---' || char(10) || 'and step three', NULL, NULL, NULL, 1788341430.0, NULL, 1, 0, 0, NULL, NULL),
(34, '20260902_093000_cccccc', 'assistant', '[PRIOR CONTEXT — for reference only; not a new message]' || char(10) || 'kept prior text' || char(10) || '[END OF PRIOR CONTEXT — COMPACTION SUMMARY BELOW]' || char(10) || '[CONTEXT COMPACTION — REFERENCE ONLY] summary', NULL, NULL, NULL, 1788341440.0, NULL, 1, 0, 0, NULL, NULL),

(40, '20260902_091500_dddddd', 'user', 'subagent task text', NULL, NULL, NULL, 1788340501.0, NULL, 1, 0, 0, NULL, NULL),
(41, '20260902_091600_eeeeee', 'user', 'delegate task text', NULL, NULL, NULL, 1788340561.0, NULL, 1, 0, 0, NULL, NULL),
(42, '20260902_091500_dddddd', 'assistant', 'subagent answer', NULL, NULL, NULL, 1788340502.0, NULL, 1, 0, 0, NULL, NULL),

(50, '20260903_120000_ffffff', 'user', '  Try   the other' || char(10) || 'approach  ', NULL, NULL, NULL, 1788436805.0, NULL, 1, 0, 0, NULL, NULL),
(51, '20260903_120000_ffffff', 'assistant', 'Trying it.', NULL, NULL, NULL, 1788436804.0, NULL, 1, 0, 0, NULL, NULL),

(60, '20260904_060000_111111', 'user', 'cron job prompt', NULL, NULL, NULL, 1788501601.0, NULL, 1, 0, 0, NULL, NULL),
(61, '20260904_080000_333333', 'user', 'tool session prompt', NULL, NULL, NULL, 1788508801.0, NULL, 1, 0, 0, NULL, NULL),

(70, '20260905_100000_444444', 'user', 'first telegram question', NULL, NULL, NULL, 1788602401.0, NULL, 1, 0, 0, NULL, NULL),
(71, '20260905_100000_444444', 'assistant', 'answer one', NULL, NULL, NULL, 1788602402.0, NULL, 1, 0, 0, NULL, NULL),
(72, '20260905_110000_555555', 'user', 'new day question', NULL, NULL, NULL, 1788606001.0, NULL, 1, 0, 0, NULL, NULL),
(73, '20260905_110000_555555', 'assistant', 'answer two', NULL, NULL, NULL, 1788606002.0, NULL, 1, 0, 0, NULL, NULL),
(74, '20260905_103000_666666', 'user', 'ephemeral work', NULL, NULL, NULL, 1788604201.0, NULL, 1, 0, 0, NULL, NULL),

(80, '20260906_100000_777777', 'assistant', 'Unprompted note', NULL, NULL, NULL, 1788688801.0, NULL, 1, 0, 0, NULL, NULL),
(81, '20260906_110000_888888', 'user', 'orphan child prompt', NULL, NULL, NULL, 1788692401.0, NULL, 1, 0, 0, NULL, NULL);
