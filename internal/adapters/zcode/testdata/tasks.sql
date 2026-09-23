-- sess_beta was renamed by the user in the desktop app; sess_alpha was not;
-- sess_fork's rename belongs to a deleted task row and is ignored.
INSERT INTO tasks VALUES ('w1', 'C:\work\alpha', 'sess_alpha', 'Fix the build', 1782000000000, 1782000900000, 0, 0);
INSERT INTO tasks VALUES ('w2', 'D:\proj\beta', 'sess_beta', '  Renamed   by user ', 1782100000000, 1782100500000, 0, 1);
INSERT INTO tasks VALUES ('w1', 'C:\work\alpha', 'sess_fork', 'Deleted rename', 1782200000000, 1782200500000, 1, 1);
