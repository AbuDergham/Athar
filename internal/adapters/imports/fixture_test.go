package imports

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // fixture databases are built by the tests
)

// Synthetic fixtures only: every id, path, title and text below is made up.
// The schemas are subsets of the real ones (column names verified against
// AgentsView v0.40.x and Pi Session Manager v0.8.x).

const avSchema = `
CREATE TABLE sessions (
    id          TEXT PRIMARY KEY,
    project     TEXT NOT NULL DEFAULT '',
    machine     TEXT NOT NULL DEFAULT 'local',
    agent       TEXT NOT NULL DEFAULT 'claude',
    agent_label TEXT NOT NULL DEFAULT '',
    entrypoint  TEXT NOT NULL DEFAULT '',
    first_message TEXT,
    display_name TEXT,
    session_name TEXT,
    started_at  TEXT,
    ended_at    TEXT,
    message_count INTEGER NOT NULL DEFAULT 0,
    file_path   TEXT,
    parent_session_id TEXT,
    relationship_type TEXT NOT NULL DEFAULT '',
    cwd TEXT NOT NULL DEFAULT '',
    deleted_at  TEXT,
    deletion_cause TEXT
);
CREATE TABLE messages (
    id             INTEGER PRIMARY KEY,
    session_id     TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    ordinal        INTEGER NOT NULL,
    role           TEXT NOT NULL,
    content        TEXT NOT NULL,
    thinking_text  TEXT NOT NULL DEFAULT '',
    timestamp      TEXT,
    has_thinking   INTEGER NOT NULL DEFAULT 0,
    has_tool_use   INTEGER NOT NULL DEFAULT 0,
    is_system      INTEGER NOT NULL DEFAULT 0,
    is_sidechain   INTEGER NOT NULL DEFAULT 0,
    UNIQUE(session_id, ordinal)
);
CREATE TABLE tool_calls (
    id         INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tool_name  TEXT NOT NULL,
    category   TEXT NOT NULL DEFAULT '',
    tool_use_id TEXT,
    input_json  TEXT,
    result_content TEXT,
    call_index INTEGER
);
CREATE VIRTUAL TABLE messages_fts USING fts5(content);
`

// avMinimalSchema is an older/smaller layout: only the required columns.
const avMinimalSchema = `
CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT NOT NULL DEFAULT 'claude', file_path TEXT);
CREATE TABLE messages (session_id TEXT NOT NULL, ordinal INTEGER NOT NULL, role TEXT NOT NULL, content TEXT NOT NULL);
`

const psmSchema = `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,
    cwd TEXT NOT NULL,
    name TEXT,
    created TEXT NOT NULL,
    modified TEXT NOT NULL,
    file_modified TEXT NOT NULL DEFAULT '',
    message_count INTEGER NOT NULL DEFAULT 0,
    first_message TEXT,
    last_message TEXT,
    cached_at TEXT NOT NULL DEFAULT '',
    parent_session_path TEXT,
    model TEXT
);
CREATE TABLE message_entries (
    id TEXT PRIMARY KEY,
    entry_id TEXT NOT NULL,
    session_path TEXT NOT NULL,
    role TEXT NOT NULL CHECK(role IN ('user', 'assistant')),
    source_type TEXT NOT NULL CHECK(source_type IN ('user', 'assistant', 'thinking', 'label')),
    content TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    search_text TEXT NOT NULL DEFAULT '',
    label TEXT
);
CREATE TABLE scan_state (
    path TEXT PRIMARY KEY,
    backing_path TEXT NOT NULL DEFAULT '',
    provider_slug TEXT NOT NULL
);
`

const unrelatedSchema = `CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT);`

// fixture ids (synthetic).
const (
	idAlpha   = "11111111-1111-4111-8111-111111111111" // claude, default root
	idGamma   = "55555555-5555-4555-8555-555555555555" // claude, .claude-local root
	idCodex   = "33333333-3333-4333-8333-333333333333"
	idCopilot = "44444444-4444-4444-8444-444444444444"
	idAgy     = "66666666-6666-4666-8666-666666666666"
	idQoder   = "77777777-7777-4777-8777-777777777777"
	idTrash   = "99999999-9999-4999-8999-999999999999"
	idMissing = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	idSDK     = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	idEmpty   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	idGemini  = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
)

// fixturePaths are the agent file paths the fixture rows point at.
type fixturePaths struct {
	home, appData  string
	alpha, gamma   string // claude transcripts (default and .claude-local root)
	codex, copilot string
	agy, qoder     string
	gemini         string
}

func newFixturePaths(root string) fixturePaths {
	home := filepath.Join(root, "home")
	appData := filepath.Join(root, "appdata")
	return fixturePaths{
		home:    home,
		appData: appData,
		alpha:   filepath.Join(home, ".claude", "projects", "C--work-alpha", idAlpha+".jsonl"),
		gamma:   filepath.Join(home, ".claude-local", "projects", "C--work-gamma", idGamma+".jsonl"),
		codex:   filepath.Join(home, ".codex", "sessions", "2026", "09", "01", "rollout-2026-09-01T10-00-00-"+idCodex+".jsonl"),
		copilot: filepath.Join(appData, "Code", "User", "workspaceStorage", "0a1b2c", "chatSessions", idCopilot+".json"),
		agy:     filepath.Join(home, ".gemini", "antigravity-cli", "conversations", idAgy+".db"),
		qoder:   filepath.Join(home, ".qoder", "projects", "C--work-qoder", idQoder+".jsonl"),
		gemini:  filepath.Join(home, ".gemini", "tmp", "work", "chats", "session-2026-09-01T10-00-dddddddd.json"),
	}
}

// createDB creates a database at path from a schema script. With wal the
// connection stays open (auto-checkpoint off) until the test ends, so the
// rows written by fill live only in the -wal file while the test reads.
func createDB(t *testing.T, path, schemaSQL string, wal bool, fill func(db *sql.DB)) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`PRAGMA synchronous=OFF`) // fixture speed only
	if wal {
		exec(`PRAGMA journal_mode=WAL`)
		exec(`PRAGMA wal_autocheckpoint=0`)
	}
	exec(schemaSQL)
	if fill != nil {
		fill(db)
	}
	if wal {
		t.Cleanup(func() { db.Close() })
		return
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// fillAgentsView writes the synthetic AgentsView rows.
func fillAgentsView(t *testing.T, p fixturePaths) func(db *sql.DB) {
	return func(db *sql.DB) {
		sess := func(id, agent, path, cwd, machine, first, display, name, start, end, parent, rel, entry, deleted, cause string) {
			t.Helper()
			null := func(s string) any {
				if s == "" {
					return nil
				}
				return s
			}
			mustExec(t, db, `INSERT INTO sessions (id, agent, file_path, cwd, machine, first_message, display_name, session_name,
				started_at, ended_at, parent_session_id, relationship_type, entrypoint, deleted_at, deletion_cause)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				id, agent, path, cwd, machine, null(first), null(display), null(name), null(start), null(end),
				null(parent), rel, entry, null(deleted), null(cause))
		}
		msgID := 0
		msg := func(session string, ordinal int, role, content, ts string, isSystem int) int {
			t.Helper()
			msgID++
			mustExec(t, db, `INSERT INTO messages (id, session_id, ordinal, role, content, timestamp, is_system) VALUES (?,?,?,?,?,?,?)`,
				msgID, session, ordinal, role, content, ts, isSystem)
			return msgID
		}
		tool := func(mid int, session, name, input, result string, idx int) {
			t.Helper()
			mustExec(t, db, `INSERT INTO tool_calls (message_id, session_id, tool_name, input_json, result_content, call_index) VALUES (?,?,?,?,?,?)`,
				mid, session, name, input, result, idx)
		}

		// Claude, default root: thinking, tool call, compact boundary, Arabic.
		sess(idAlpha, "claude", p.alpha, `C:\work\alpha`, "TESTHOST", "Fix   the build\nplease", "", "",
			"2026-09-01T08:00:00.5Z", "2026-09-01T08:10:00.123456789Z", "", "", "cli", "", "")
		msg(idAlpha, 0, "user", "Fix   the build\nplease", "2026-09-01T08:00:01Z", 0)
		m := msg(idAlpha, 1, "assistant", "[Thinking]\nprivate reasoning here\n[/Thinking]\nI will run the build.\n[Bash]\n$ go build ./...", "2026-09-01T08:00:02.1Z", 0)
		tool(m, idAlpha, "Bash", `{"command":"go build ./..."}`, "build ok", 0)
		msg(idAlpha, 2, "assistant", "Conversation compacted", "2026-09-01T08:05:00Z", 1)
		msg(idAlpha, 3, "user", "شكراً، هل تم إصلاح المشروع؟", "2026-09-01T08:06:00Z", 0)
		msg(idAlpha, 4, "assistant", "[Thinking]\nonly thinking\n[/Thinking]", "2026-09-01T08:07:00Z", 0)
		msg(idAlpha, 5, "user", "<command-name>/model</command-name>", "2026-09-01T08:07:30Z", 0)
		msg(idAlpha, 6, "user", "<bash-input>git status</bash-input>", "2026-09-01T08:08:00Z", 0)

		// Claude subagent of alpha: skipped (parent + relationship).
		sess("agent-a1b2c3d4e5f6a7b8c", "claude", filepath.Join(filepath.Dir(p.alpha), idAlpha, "subagents", "agent-a1.jsonl"),
			`C:\work\alpha`, "local", "subagent task", "", "", "2026-09-01T08:01:00Z", "2026-09-01T08:02:00Z", idAlpha, "subagent", "cli", "", "")
		msg("agent-a1b2c3d4e5f6a7b8c", 0, "user", "subagent task", "2026-09-01T08:01:00Z", 0)

		// Claude, .claude-local root, user rename in AgentsView.
		sess(idGamma, "claude", p.gamma, `C:\work\gamma`, "local", "first prompt", "Renamed by user", "", "2026-09-02T09:00:00Z", "2026-09-02T09:30:00Z", "", "", "cli", "", "")
		msg(idGamma, 0, "user", "first prompt", "2026-09-02T09:00:00Z", 0)
		msg(idGamma, 1, "assistant", "answer", "2026-09-02T09:01:00Z", 0)

		// Codex: a tool call the content does not mention, a long result.
		sess("codex:"+idCodex, "codex", p.codex, `D:\proj\codex`, "local", "list files", "", "", "2026-09-01T10:00:00Z", "2026-09-01T10:05:00Z", "", "", "", "", "")
		msg("codex:"+idCodex, 0, "user", "list files", "2026-09-01T10:00:00Z", 0)
		m = msg("codex:"+idCodex, 1, "assistant", "", "2026-09-01T10:00:05Z", 0)
		tool(m, "codex:"+idCodex, "shell", `{"command":["bash","-lc","ls -la"]}`, string(make3KB()), 0)
		tool(m, "codex:"+idCodex, "update_plan", `{"plan":[{"step":"x"}]}`, "", 1)
		// a call AgentsView rendered under another name ("shell" as "[Bash]")
		m = msg("codex:"+idCodex, 2, "assistant", "[Bash]\n$ pwd", "2026-09-01T10:00:09Z", 0)
		tool(m, "codex:"+idCodex, "shell", `{"command":["pwd"]}`, `D:\proj\codex`, 0)

		// VS Code Copilot, from another PC.
		sess("vscode-copilot:"+idCopilot, "vscode-copilot", p.copilot, "", "OTHER-PC", "explain code", "", "", "2025-05-19T11:35:19.764Z", "2025-05-19T11:48:23.215Z", "", "", "", "", "")
		msg("vscode-copilot:"+idCopilot, 0, "user", "explain code", "2025-05-19T11:35:19.764Z", 0)
		msg("vscode-copilot:"+idCopilot, 1, "assistant", "[Grep: copilot_searchCode]\nIt does X.", "2025-05-19T11:36:00Z", 0)

		// Antigravity CLI, no times on the session row.
		sess("antigravity-cli:"+idAgy, "antigravity-cli", p.agy, "", "local", "", "", "", "", "", "", "", "", "", "")
		msg("antigravity-cli:"+idAgy, 0, "user", "hello agy", "2026-06-17T08:04:33.6326837Z", 0)
		msg("antigravity-cli:"+idAgy, 1, "assistant", "hi", "2026-06-17T08:04:40.7310381Z", 0)

		// Qoder: agent title, no messages -> titles coverage.
		sess("qoder:"+idQoder, "qoder", p.qoder, `C:\work\qoder`, "local", "", "", "Qoder session name", "", "", "", "", "", "", "")

		// Unknown agent label with an unknown path.
		sess("amp:T-0001", "amp", filepath.Join(p.home, ".local", "share", "amp", "threads", "T-0001.json"), "", "local", "amp prompt", "", "", "2026-01-01T00:00:00Z", "2026-01-01T00:01:00Z", "", "", "", "", "")
		msg("amp:T-0001", 0, "user", "amp prompt", "2026-01-01T00:00:00Z", 0)

		// Trashed by the user in AgentsView: skipped. Source missing: kept.
		sess(idTrash, "claude", filepath.Join(filepath.Dir(p.alpha), idTrash+".jsonl"), `C:\work\alpha`, "local", "trashed", "", "", "2026-09-03T00:00:00Z", "2026-09-03T00:01:00Z", "", "", "cli", "2026-09-04T00:00:00Z", "")
		msg(idTrash, 0, "user", "trashed", "2026-09-03T00:00:00Z", 0)
		sess(idMissing, "claude", filepath.Join(filepath.Dir(p.alpha), idMissing+".jsonl"), `C:\work\alpha`, "local", "deleted transcript", "", "", "2026-09-03T00:00:00Z", "2026-09-03T00:01:00Z", "", "", "cli", "2026-09-04T00:00:00Z", "source_missing")
		msg(idMissing, 0, "user", "deleted transcript", "2026-09-03T00:00:00Z", 0)

		// Headless Claude run: skipped. Empty row: skipped.
		sess(idSDK, "claude", filepath.Join(filepath.Dir(p.alpha), idSDK+".jsonl"), `C:\work\alpha`, "local", "headless", "", "", "2026-09-05T00:00:00Z", "2026-09-05T00:01:00Z", "", "", "sdk-cli", "", "")
		msg(idSDK, 0, "user", "headless", "2026-09-05T00:00:00Z", 0)
		sess(idEmpty, "claude", filepath.Join(filepath.Dir(p.alpha), idEmpty+".jsonl"), "", "local", "", "", "", "", "", "", "", "cli", "", "")
	}
}

// make3KB returns a tool result longer than model.MaxToolOutput, ending in
// multi-byte runes so the cut must respect rune boundaries.
func make3KB() []byte {
	b := make([]byte, 0, 3200)
	for len(b) < 2047 {
		b = append(b, 'x')
	}
	for len(b) < 3100 {
		b = append(b, "é"...)
	}
	return b
}

// psmHash is Pi Session Manager's id suffix: first 12 hex of sha256(path).
func psmHash(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:])[:12]
}

// fillPSM writes the synthetic Pi Session Manager rows.
func fillPSM(t *testing.T, p fixturePaths) func(db *sql.DB) {
	return func(db *sql.DB) {
		sess := func(id, path, cwd, name, created, modified, first, parent, slug string) {
			t.Helper()
			null := func(s string) any {
				if s == "" {
					return nil
				}
				return s
			}
			mustExec(t, db, `INSERT INTO sessions (id, path, cwd, name, created, modified, first_message, parent_session_path) VALUES (?,?,?,?,?,?,?,?)`,
				id, path, cwd, null(name), created, modified, null(first), null(parent))
			mustExec(t, db, `INSERT INTO scan_state (path, backing_path, provider_slug) VALUES (?,?,?)`, path, path, slug)
		}
		n := 0
		entry := func(path, role, source, content, ts string) {
			t.Helper()
			n++
			mustExec(t, db, `INSERT INTO message_entries (id, entry_id, session_path, role, source_type, content, timestamp) VALUES (?,?,?,?,?,?,?)`,
				path+"::e"+string(rune('a'+n)), "e"+string(rune('a'+n)), path, role, source, content, ts)
		}

		// Claude transcript; the name is the (truncated) first prompt.
		sess("claude-code:"+idAlpha+":"+psmHash(p.alpha), p.alpha, `C:\work\alpha`, "Fix the build...",
			"2026-09-01T08:00:00.500+00:00", "2026-09-01T08:10:00.123456700+00:00", "Fix the build please", "", "claude-code")
		entry(p.alpha, "user", "user", "Fix the build please", "2026-09-01T08:00:01.000+00:00")
		entry(p.alpha, "assistant", "thinking", "private reasoning", "2026-09-01T08:00:02.000+00:00")
		entry(p.alpha, "assistant", "assistant", "Done, the build passes.", "2026-09-01T08:00:04.000+00:00")
		entry(p.alpha, "assistant", "assistant", "", "2026-09-01T08:00:05.000+00:00")
		entry(p.alpha, "assistant", "label", "checkpoint", "2026-09-01T08:00:06.000+00:00")
		entry(p.alpha, "user", "user", "<local-command-stdout>ok</local-command-stdout>", "2026-09-01T08:00:07.000+00:00")
		// re-indexed entry: later rowid, earlier time
		entry(p.alpha, "assistant", "assistant", "Running the build now.", "2026-09-01T08:00:03.000+00:00")

		// .claude-local transcript mislabeled "pi".
		sess("claude-code:"+idGamma+":"+psmHash(p.gamma), p.gamma, `C:\work\gamma`, "Gamma work title",
			"2026-09-02T09:00:00.000+00:00", "2026-09-02T09:30:00.000+00:00", "first prompt", "", "pi")
		entry(p.gamma, "user", "user", "first prompt", "2026-09-02T09:00:00.000+00:00")

		// Gemini chat mislabeled "pi", nothing indexed: skipped.
		sess("claude-code:"+idGemini+":"+psmHash(p.gemini), p.gemini, "", "", "2026-06-17T07:40:08.726706800+00:00", "2026-06-17T07:40:08.726706800+00:00", "", "", "pi")

		// Codex rollout with an agent title.
		sess("codex:"+idCodex+":"+psmHash(p.codex), p.codex, `D:\proj\codex`, "Codex thread title",
			"2026-09-01T10:00:00.000+00:00", "2026-09-01T10:05:00.000+00:00", "list files", "", "codex")
		entry(p.codex, "user", "user", "list files", "2026-09-01T10:00:00.000+00:00")
		entry(p.codex, "assistant", "assistant", "Here they are.", "2026-09-01T10:00:05.000+00:00")

		// Child session and a prompt log: skipped.
		child := filepath.Join(filepath.Dir(p.alpha), "child.jsonl")
		sess("claude-code:child:"+psmHash(child), child, `C:\work\alpha`, "child", "2026-09-01T08:00:00+00:00", "2026-09-01T08:00:00+00:00", "child", p.alpha, "claude-code")
		entry(child, "user", "user", "child", "2026-09-01T08:00:00+00:00")
		hist := filepath.Join(p.home, ".claude-local", "history.jsonl")
		sess("claude-code:hist:"+psmHash(hist), hist, "", "", "2026-08-22T14:40:05+00:00", "2026-08-22T14:40:05+00:00", "", "", "pi")
		entry(hist, "user", "user", "a prompt", "2026-08-22T14:40:05+00:00")

		// Pi's own session: bare id.
		pi := filepath.Join(p.home, ".pi", "agent", "sessions", "--C--work-pi--", "2026-09-06T10-00-00-000Z_pi0001.jsonl")
		sess("pi0001", pi, `C:\work\pi`, "", "2026-09-06T10:00:00+00:00", "2026-09-06T10:10:00+00:00", "pi prompt", "", "pi")
		entry(pi, "user", "user", "pi prompt", "2026-09-06T10:00:00+00:00")
	}
}
