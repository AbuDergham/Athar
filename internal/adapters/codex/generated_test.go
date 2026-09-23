package codex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

// Tests in this file build their Codex trees in temp dirs (compressed rollouts,
// a 2 MB record, a WAL-mode state database) instead of committing binaries.

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// rolloutLines returns the JSONL lines of a small modern rollout.
func rolloutLines(t *testing.T, id, cwd, prompt, answer string) []string {
	t.Helper()
	return []string{
		mustJSON(t, map[string]any{"timestamp": "2026-03-01T10:00:00.000Z", "type": "session_meta",
			"payload": map[string]any{"id": id, "timestamp": "2026-03-01T10:00:00.000Z", "cwd": cwd, "originator": "codex_cli_rs"}}),
		mustJSON(t, map[string]any{"timestamp": "2026-03-01T10:00:01.000Z", "type": "event_msg",
			"payload": map[string]any{"type": "user_message", "message": prompt, "kind": "plain"}}),
		mustJSON(t, map[string]any{"timestamp": "2026-03-01T10:00:02.000Z", "type": "event_msg",
			"payload": map[string]any{"type": "agent_message", "message": answer}}),
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rolloutPath(codexDir, id string) string {
	return filepath.Join(codexDir, "sessions", "2026", "03", "01", "rollout-2026-03-01T11-00-00-"+id+".jsonl")
}

func TestZstdRollout(t *testing.T) {
	root := t.TempDir()
	codexDir := filepath.Join(root, "home", ".codex")
	id := "0199c000-0000-7000-8000-00000000000b"
	path := rolloutPath(codexDir, id) + ".zst"
	// compressed files are written whole: the last line has no newline and still counts
	content := strings.Join(rolloutLines(t, id, `C:\zst`, "compressed prompt", "compressed answer"), "\n")
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(enc.EncodeAll([]byte(content), nil)))
	enc.Close()

	env := adapterstest.FixtureEnv(t, root, nil)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 0 || len(sessions) != 1 {
		t.Fatalf("sessions %d skipped %d, want 1 and 0", len(sessions), skipped)
	}
	s := sessions[0]
	if s.NativeID != id || s.Cwd != `C:\zst` {
		t.Errorf("session %q cwd %q", s.NativeID, s.Cwd)
	}
	checkMsgs(t, s, []wantMsg{
		{model.RoleUser, model.KindText, "compressed prompt"},
		{model.RoleAssistant, model.KindText, "compressed answer"},
	})
}

func TestLargeSingleLineRecord(t *testing.T) {
	root := t.TempDir()
	codexDir := filepath.Join(root, "home", ".codex")
	id := "0199c000-0000-7000-8000-00000000000c"
	big := "START " + strings.Repeat("é", 1<<20) // 2 MB of two-byte runes
	inner := mustJSON(t, map[string]any{"output": big, "metadata": map[string]any{"exit_code": 0}})
	lines := rolloutLines(t, id, `C:\big`, "print the big file", "done")
	call := mustJSON(t, map[string]any{"timestamp": "2026-03-01T10:00:01.500Z", "type": "response_item",
		"payload": map[string]any{"type": "function_call", "name": "shell", "call_id": "c1",
			"arguments": mustJSON(t, map[string]any{"command": []string{"cat", "big.txt"}})}})
	out := mustJSON(t, map[string]any{"timestamp": "2026-03-01T10:00:01.600Z", "type": "response_item",
		"payload": map[string]any{"type": "function_call_output", "call_id": "c1", "output": inner}})
	if len(out) < 2<<20 {
		t.Fatalf("record is only %d bytes", len(out))
	}
	all := []string{lines[0], lines[1], call, out, lines[2]}
	writeFile(t, rolloutPath(codexDir, id), strings.Join(all, "\n")+"\n")

	env := adapterstest.FixtureEnv(t, root, nil)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 0 || len(sessions) != 1 {
		t.Fatalf("sessions %d skipped %d", len(sessions), skipped)
	}
	msgs := sessions[0].Messages
	if len(msgs) != 4 {
		t.Fatalf("%d messages, want 4", len(msgs))
	}
	o := msgs[2]
	if o.Kind != model.KindToolOutput || !strings.HasPrefix(o.Text, "START é") {
		t.Errorf("output message = %s %.20q", o.Kind, o.Text)
	}
	if len(o.Text) > model.MaxToolOutput || !utf8.ValidString(o.Text) {
		t.Errorf("output kept %d bytes, valid UTF-8 %v", len(o.Text), utf8.ValidString(o.Text))
	}
	if msgs[1].Text != "shell: cat big.txt" {
		t.Errorf("call = %q", msgs[1].Text)
	}
}

// stateDB creates a WAL-mode Codex state database and returns a connection
// that stays open (like a running Codex), so its rows sit in the -wal file.
func stateDB(t *testing.T, path string, rows [][4]string) *sql.Conn {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	for _, q := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0,
			source TEXT NOT NULL DEFAULT 'cli', cwd TEXT NOT NULL, title TEXT NOT NULL,
			first_user_message TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, r := range rows {
		if _, err := conn.ExecContext(ctx, "INSERT INTO threads (id, cwd, title, first_user_message) VALUES (?, ?, ?, ?)",
			r[0], r[1], r[2], r[3]); err != nil {
			t.Fatal(err)
		}
	}
	return conn
}

func TestStateIndexTitles(t *testing.T) {
	root := t.TempDir()
	codexDir := filepath.Join(root, "home", ".codex")
	const (
		idX = "0199d000-0000-7000-8000-000000000001" // renamed in the state DB
		idY = "0199d000-0000-7000-8000-000000000002" // state title == first prompt
		idZ = "0199d000-0000-7000-8000-000000000003" // named in session_index.jsonl
		idW = "0199d000-0000-7000-8000-000000000004" // prompt log only; cwd from the state DB
	)
	for _, r := range [][3]string{{idX, `C:\x`, "Prompt for X"}, {idY, `C:\y`, "Prompt for Y"}, {idZ, `C:\z`, "Prompt for Z"}} {
		writeFile(t, rolloutPath(codexDir, r[0]), strings.Join(rolloutLines(t, r[0], r[1], r[2], "ok"), "\n")+"\n")
	}
	writeFile(t, filepath.Join(codexDir, "history.jsonl"),
		mustJSON(t, map[string]any{"session_id": idW, "ts": 1772359200, "text": "w prompt"})+"\n")
	writeFile(t, filepath.Join(codexDir, "session_index.jsonl"), strings.Join([]string{
		mustJSON(t, map[string]any{"id": idZ, "thread_name": "Named via index v1", "updated_at": "2026-03-01T10:00:00Z"}),
		"garbage line",
		mustJSON(t, map[string]any{"id": idZ, "thread_name": "Named via index v2", "updated_at": "2026-03-01T11:00:00Z"}),
	}, "\n")+"\n")
	// an older schema generation with a stale title: the highest state_N wins
	old := stateDB(t, filepath.Join(codexDir, "state_4.sqlite"), [][4]string{{idX, `C:\x`, "Stale title", "Prompt for X"}})
	old.Close()
	conn := stateDB(t, filepath.Join(codexDir, "state_12.sqlite"), [][4]string{
		{idX, `C:\x`, "Renamed in the app", "Prompt for X"},
		{idY, `C:\y`, "Prompt for Y", "Prompt for Y"},
		{idW, `C:\w-dir`, "w prompt", "w prompt"},
	})
	if info, err := os.Stat(filepath.Join(codexDir, "state_12.sqlite-wal")); err != nil || info.Size() == 0 {
		t.Fatalf("fixture WAL missing or empty: %v", err)
	}

	before := snapshot(t, root)
	env := adapterstest.FixtureEnv(t, root, nil)
	a := New()
	sessions, _ := adapterstest.ReadAll(t, a, env)
	m := byKey(t, sessions)
	check := func(key, title string, src model.TitleSource) {
		t.Helper()
		s, ok := m[key]
		if !ok {
			t.Fatalf("missing session %s", key)
		}
		if s.Title != title || s.TitleSource != src {
			t.Errorf("%s: Title = %q (%s), want %q (%s)", key, s.Title, s.TitleSource, title, src)
		}
	}
	check("live/"+idX, "Renamed in the app", model.TitleAgent)
	check("live/"+idY, "Prompt for Y", model.TitlePrompt)
	check("live/"+idZ, "Named via index v2", model.TitleAgent)
	check("prompt-log/"+idW, "w prompt", model.TitlePrompt)
	if w := m["prompt-log/"+idW]; w.Cwd != `C:\w-dir` {
		t.Errorf("prompt-log Cwd = %q, want it from the state DB", w.Cwd)
	}

	// read-only: nothing in the agent folder changed or appeared
	if after := snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("reading changed the Codex folder:\n before %v\n after  %v", before, after)
	}
	// the private copies were cleaned up
	if left, _ := filepath.Glob(filepath.Join(env.TempDir, "*")); len(left) != 0 {
		t.Errorf("temp copies left behind: %q", left)
	}

	// a later rename in the state DB is seen by the next read of a changed rollout
	if _, err := conn.ExecContext(context.Background(), "UPDATE threads SET title = 'Renamed again' WHERE id = ?", idX); err != nil {
		t.Fatal(err)
	}
	srcs, err := a.Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range srcs {
		if !strings.Contains(src.Path, idX) {
			continue
		}
		res, err := a.Read(context.Background(), env, src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Sessions) != 1 || res.Sessions[0].Title != "Renamed again" {
			t.Errorf("after rename: %+v", res.Sessions)
		}
	}
}

func TestStateIndexUnreadableIsAWarning(t *testing.T) {
	root := t.TempDir()
	codexDir := filepath.Join(root, "home", ".codex")
	id := "0199e000-0000-7000-8000-000000000001"
	writeFile(t, rolloutPath(codexDir, id), strings.Join(rolloutLines(t, id, `C:\q`, "a prompt", "ok"), "\n")+"\n")
	writeFile(t, filepath.Join(codexDir, "state_5.sqlite"), "this is not a database, just bytes")
	env := adapterstest.FixtureEnv(t, root, nil)
	a := New()
	srcs, err := a.Sources(env)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Sources = %v, %v", srcs, err)
	}
	res, err := a.Read(context.Background(), env, srcs[0])
	if err != nil {
		t.Fatalf("an unreadable title index must not fail the rollout: %v", err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].Title != "a prompt" {
		t.Errorf("sessions = %+v", res.Sessions)
	}
	if len(res.Warnings) == 0 {
		t.Errorf("expected a warning about the title index")
	}
}

func TestTruncatedZstdRollout(t *testing.T) {
	root := t.TempDir()
	codexDir := filepath.Join(root, "home", ".codex")
	id := "0199c000-0000-7000-8000-00000000000d"
	path := rolloutPath(codexDir, id) + ".zst"
	content := strings.Repeat(strings.Join(rolloutLines(t, id, `C:\zst`, "compressed prompt", "compressed answer"), "\n")+"\n", 200)
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	full := enc.EncodeAll([]byte(content), nil)
	enc.Close()
	writeFile(t, path, string(full[:len(full)/2])) // still being written

	env := adapterstest.FixtureEnv(t, root, nil)
	a := New()
	src := adapters.Source{Adapter: "codex", Path: path, Files: []string{path}}
	_, err = a.Read(context.Background(), env, src)
	if !errors.Is(err, adapters.ErrRetryLater) {
		t.Fatalf("fresh truncated .zst: err = %v, want ErrRetryLater", err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	_, err = a.Read(context.Background(), env, src)
	if err == nil || errors.Is(err, adapters.ErrRetryLater) {
		t.Fatalf("old truncated .zst: err = %v, want a plain error", err)
	}
}
