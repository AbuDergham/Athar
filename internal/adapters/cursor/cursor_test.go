package cursor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"

	_ "modernc.org/sqlite" // fixture databases are built with the sqlite driver
)

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

// kvDB creates a VS Code-style state.vscdb with the given ItemTable and
// cursorDiskKV rows. A nil value is stored as SQL NULL.
func kvDB(t *testing.T, path string, items map[string]any, disk map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`,
		`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	put := func(table string, rows map[string]any) {
		for k, v := range rows {
			var val any
			switch x := v.(type) {
			case nil:
				val = nil
			case string: // raw text, stored as is (lets tests store broken JSON)
				val = x
			default:
				b, err := json.Marshal(x)
				if err != nil {
					t.Fatal(err)
				}
				val = string(b)
			}
			if _, err := db.Exec(`INSERT INTO `+table+` (key, value) VALUES (?, ?)`, k, val); err != nil {
				t.Fatal(err)
			}
		}
	}
	put("ItemTable", items)
	put("cursorDiskKV", disk)
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

type obj = map[string]any

func workspace(t *testing.T, env adapters.Env, hash, folder string, composers ...string) {
	t.Helper()
	dir := filepath.Join(env.AppData, "Cursor", "User", "workspaceStorage", hash)
	if folder != "" {
		b, _ := json.Marshal(obj{"folder": folder})
		writeFile(t, filepath.Join(dir, "workspace.json"), string(b))
	}
	var all []any
	for _, c := range composers {
		all = append(all, obj{"composerId": c, "createdAt": 1, "unifiedMode": "agent"})
	}
	kvDB(t, filepath.Join(dir, "state.vscdb"), map[string]any{
		"composer.composerData": obj{"allComposers": all, "selectedComposerIds": []any{}},
		"unrelated.key":         obj{"x": 1},
	}, nil)
}

func bubbleRow(id string, typ int, text, created string) obj {
	b := obj{"_v": 3, "bubbleId": id, "type": typ, "text": text, "richText": "", "capabilityStatuses": obj{}}
	if created != "" {
		b["createdAt"] = created
	}
	return b
}

// buildFixture writes a synthetic Cursor tree under a temp root.
func buildFixture(t *testing.T) adapters.Env {
	t.Helper()
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)

	longOutput := strings.Repeat("é", 3000) // 6000 bytes, cut to 2 KB on a rune boundary
	toolResult, _ := json.Marshal(obj{"output": "ok\n", "exitCodeV2": 0, "rejected": false})
	longResult, _ := json.Marshal(obj{"contents": longOutput, "totalLinesInFile": 1})

	thinking := bubbleRow("th1", 2, "", "2025-12-14T10:01:30.000Z")
	thinking["capabilityType"] = 30
	thinking["thinking"] = obj{"text": "hidden reasoning"}
	tool := bubbleRow("t1", 2, "", "2025-12-14T10:02:00.000Z")
	tool["capabilityType"] = 15
	tool["toolFormerData"] = obj{"name": "run_terminal_cmd", "rawArgs": `{"command":"npm test","is_background":false}`,
		"params": `{"command":"npm test"}`, "result": string(toolResult), "status": "completed"}
	readTool := bubbleRow("t2", 2, "", "2025-12-14T10:02:30.000Z")
	readTool["toolFormerData"] = obj{"name": "read_file", "rawArgs": `{"target_file":"src/app.go"}`, "result": string(longResult), "status": "completed"}
	failTool := bubbleRow("t3", 2, "", "2025-12-14T10:02:40.000Z")
	failTool["toolFormerData"] = obj{"name": "grep", "rawArgs": `{"query":"TODO"}`, "result": "", "status": "error",
		"error": `{"clientVisibleErrorMessage":"tool failed","modelVisibleErrorMessage":"tool failed (model)"}`}
	uriBubble := bubbleRow("d1", 1, "where is delta", "2025-12-01T08:00:00.000Z")
	uriBubble["workspaceUris"] = []any{"file:///d%3A/work/delta"}
	uriBubble["workspaceProjectDir"] = `C:\x\.cursor\projects\d-work-delta`

	disk := map[string]any{
		// Current schema: headers + one row per bubble; mapped by a workspace DB.
		"composerData:c-new": obj{"_v": 10, "composerId": "c-new", "name": "Fix the login flow",
			"createdAt": 1765706000000, "lastUpdatedAt": 1765706600000, "status": "completed",
			"conversation": []any{}, "conversationMap": obj{}, "speculativeSummarizationEncryptionKey": "secret",
			"fullConversationHeadersOnly": []any{
				obj{"bubbleId": "u1", "type": 1}, obj{"bubbleId": "a1", "type": 2},
				obj{"bubbleId": "th1", "type": 2}, obj{"bubbleId": "t1", "type": 2, "serverBubbleId": "s1"},
				obj{"bubbleId": "t2", "type": 2}, obj{"bubbleId": "t3", "type": 2},
				obj{"bubbleId": "gone", "type": 2}, obj{"bubbleId": "nul", "type": 2},
				obj{"bubbleId": "a2", "type": 2},
			}},
		"bubbleId:c-new:u1":    bubbleRow("u1", 1, "The login form rejects valid users", "2025-12-14T10:00:00.000Z"),
		"bubbleId:c-new:a1":    bubbleRow("a1", 2, "Let me run the tests.", "2025-12-14T10:01:00.000Z"),
		"bubbleId:c-new:th1":   thinking,
		"bubbleId:c-new:t1":    tool,
		"bubbleId:c-new:t2":    readTool,
		"bubbleId:c-new:t3":    failTool,
		"bubbleId:c-new:nul":   nil,
		"bubbleId:c-new:a2":    bubbleRow("a2", 2, "Fixed: the session cookie was not set.", "2025-12-14T10:05:00.000Z"),
		"bubbleId:c-newer:zz":  bubbleRow("zz", 1, "belongs to another composer", "2025-12-14T11:00:00.000Z"),
		"checkpointId:c-new:x": obj{"files": []any{}},

		// Oldest schema: messages inline in conversation[], no _v.
		"composerData:c-old": obj{"composerId": "c-old", "name": "Old chat",
			"createdAt": 1740322996177, "lastUpdatedAt": 1740323614338, "status": "completed",
			"conversation": []any{
				obj{"bubbleId": "o1", "type": 1, "text": "Explain the Dockerfile", "richText": "{}"},
				obj{"bubbleId": "o2", "type": 2, "text": "It builds the backend image.",
					"timingInfo": obj{"clientStartTime": 1740323111487, "clientEndTime": 1740323130490}},
				obj{"bubbleId": "o3", "type": 1, "text": "   "},
			}},

		// Newest schema: workspaceIdentifier on the composer, no name.
		"composerData:c-wid": obj{"_v": 16, "composerId": "c-wid", "createdAt": 1767000000000,
			"workspaceIdentifier":         obj{"id": "abc", "uri": obj{"fsPath": `e:\repo\gamma`, "external": "file:///e%3A/repo/gamma"}},
			"fullConversationHeadersOnly": []any{obj{"bubbleId": "g1", "type": 1}, obj{"bubbleId": "g2", "type": 2}}},
		"bubbleId:c-wid:g1": bubbleRow("g1", 1, "  Add   a README\n for gamma  ", "2025-12-29T09:20:00Z"),
		"bubbleId:c-wid:g2": bubbleRow("g2", 2, "Done.", "2025-12-29T12:21:00.500+03:00"),

		// Project only known from a bubble's workspaceUris.
		"composerData:c-uri": obj{"_v": 10, "composerId": "c-uri", "name": "Delta", "createdAt": 1764576000000,
			"fullConversationHeadersOnly": []any{obj{"bubbleId": "d1", "type": 1}}},
		"bubbleId:c-uri:d1": uriBubble,

		// Empty drafts are skipped.
		"composerData:c-draft":  obj{"_v": 10, "composerId": "c-draft", "createdAt": 1765708761193, "fullConversationHeadersOnly": []any{}, "conversation": []any{}, "isDraft": true},
		"composerData:c-legacy": obj{"composerId": "c-legacy", "createdAt": 1740322725849, "conversation": []any{}},

		// No bubbles in the store; the text comes from its agent transcript.
		"composerData:c-trans": obj{"_v": 16, "composerId": "c-trans", "name": "Transcript chat", "createdAt": 1768000000000, "lastUpdatedAt": 1768000900000,
			"fullConversationHeadersOnly": []any{}},

		// A torn row is skipped, never fatal.
		"composerData:c-bad": `{"_v": 10, "composerId": "c-bad", "fullConvers`,
		"inlineDiffs-xyz":    obj{"a": 1},
	}
	kvDB(t, filepath.Join(env.AppData, "Cursor", "User", "globalStorage", "state.vscdb"),
		map[string]any{"cursorAuth/accessToken": "token-must-never-be-read"}, disk)
	writeFile(t, filepath.Join(env.AppData, "Cursor", "User", "globalStorage", "state.vscdb.backup"), "not a database")

	workspace(t, env, "aaaa1111", "file:///d%3A/work/alpha", "c-new", "c-trans")
	workspace(t, env, "bbbb2222", "file:///c%3A/code/proj%20two", "c-old")
	workspace(t, env, "1765708592153", "", "c-draft") // empty window: no workspace.json
	workspace(t, env, "cccc3333", "file:///e%3A/elsewhere", "c-wid")

	// Agent transcripts (newer Cursor builds and the Cursor CLI).
	proj := filepath.Join(env.Home, ".cursor", "projects")
	big := strings.Repeat("x", 2<<20)
	lines := []string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Monday, Dec 15, 2025, 9:30 AM (UTC+3)</timestamp>\n<user_query>\nsummarize the notes\n</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"` + big + `"},{"type":"tool_use","name":"Shell","input":{"command":"dir /b"}}]}}`,
		`{"role":"tool","message":{"content":[{"type":"tool_result","content":"a.txt"}]}}`,
		`{"type":"turn_ended"}`,
		`not json`,
		`{"role":"assistant","message":{"content":"all done"}}`,
		`{"role":"user","message":{"content":"unterminated last line is not delivered"}}`,
	}
	writeFile(t, filepath.Join(proj, "d-work-alpha", "agent-transcripts", "t-only.jsonl"), strings.Join(lines, "\n"))
	writeFile(t, filepath.Join(proj, "d-work-alpha", "agent-transcripts", "t-only", "subagents", "sub1.jsonl"),
		`{"role":"user","message":{"content":"subagent prompt"}}`+"\n")
	writeFile(t, filepath.Join(proj, "d-work-alpha", "agent-transcripts", "c-trans.jsonl"),
		`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>hello from the transcript</user_query>"}]}}`+"\n"+
			`{"role":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`+"\n")
	writeFile(t, filepath.Join(proj, "d-work-alpha", "agent-transcripts", "t-nested", "t-nested.jsonl"),
		`{"role":"user","message":{"content":"nested prompt"}}`+"\n")
	writeFile(t, filepath.Join(proj, "x-unknown-place", "agent-transcripts", "t-txt.txt"),
		"user:\n<user_query>\nplain text prompt\n</user_query>\n\nassistant:\nplain text answer\nsecond line\n")
	writeFile(t, filepath.Join(proj, "x-unknown-place", "rules", "r.mdc"), "rule")
	writeFile(t, filepath.Join(proj, "c-code-proj-two", "terminals", "1.txt"), "terminal")
	return env
}

func byID(ss []model.Session) map[string]model.Session {
	m := map[string]model.Session{}
	for _, s := range ss {
		m[s.NativeID] = s
	}
	return m
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "cursor" || a.Name() != "Cursor" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if c.Resume != nil || c.Fork != nil || c.SkipPermissions != nil || c.RootEnv != "" {
		t.Fatalf("Cursor has no terminal resume: %+v", c)
	}
	if c.App == nil || c.App.Name != "Cursor" || c.App.Program != "cursor" {
		t.Fatalf("App = %+v", c.App)
	}
	if len(c.App.Args) != 1 || c.App.Args[0] != "{cwd}" {
		t.Errorf("App.Args = %q", c.App.Args)
	}
	want := map[string]bool{`%ProgramFiles%\cursor\Cursor.exe`: false, `%LOCALAPPDATA%\Programs\cursor\Cursor.exe`: false}
	for _, cand := range c.App.Candidates {
		if _, ok := want[cand]; ok {
			want[cand] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("Candidates %q lack %q", c.App.Candidates, k)
		}
	}
}

func TestSourcesMissing(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on an empty tree = %v, %v", srcs, err)
	}
}

func TestSources(t *testing.T) {
	env := buildFixture(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 {
		t.Fatalf("got %d sources, want 1", len(srcs))
	}
	s := srcs[0]
	db := filepath.Join(env.AppData, "Cursor", "User", "globalStorage", "state.vscdb")
	if s.Adapter != "cursor" || s.Path != db || s.Root != "" {
		t.Errorf("source = %+v", s)
	}
	files := map[string]bool{}
	for _, f := range s.Files {
		files[f] = true
	}
	ws := filepath.Join(env.AppData, "Cursor", "User", "workspaceStorage")
	proj := filepath.Join(env.Home, ".cursor", "projects")
	for _, f := range []string{
		db,
		filepath.Join(ws, "aaaa1111", "workspace.json"),
		filepath.Join(ws, "aaaa1111", "state.vscdb"),
		filepath.Join(proj, "d-work-alpha", "agent-transcripts", "t-only.jsonl"),
		filepath.Join(proj, "d-work-alpha", "agent-transcripts", "t-nested", "t-nested.jsonl"),
		filepath.Join(proj, "x-unknown-place", "agent-transcripts", "t-txt.txt"),
	} {
		if !files[f] {
			t.Errorf("Files lack %s", f)
		}
	}
	for f := range files {
		// Empty windows (no workspace.json) cannot give a project.
		if strings.Contains(f, "subagents") || strings.HasSuffix(f, ".backup") || strings.Contains(f, "terminals") || strings.Contains(f, "1765708592153") {
			t.Errorf("Files include %s", f)
		}
	}
}

func TestSourcesTranscriptsOnly(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	p := filepath.Join(env.Home, ".cursor", "projects", "d-cli", "agent-transcripts", "cli1.jsonl")
	writeFile(t, p, `{"role":"user","message":{"content":"from the cli"}}`+"\n")
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Sources = %v, %v", srcs, err)
	}
	ss, _ := adapterstest.ReadAll(t, New(), env)
	if len(ss) != 1 || ss[0].NativeID != "cli1" || len(ss[0].Messages) != 1 {
		t.Fatalf("sessions = %+v", ss)
	}
}

func TestRead(t *testing.T) {
	env := buildFixture(t)
	ss, skipped := adapterstest.ReadAll(t, New(), env)
	got := byID(ss)
	var ids []string
	for id := range got {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	want := []string{"c-new", "c-old", "c-trans", "c-uri", "c-wid", "t-nested", "t-only", "t-txt"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("sessions = %v, want %v", ids, want)
	}
	// c-bad (torn JSON), the missing "gone" bubble, the NULL "nul" bubble, "not json".
	if skipped != 4 {
		t.Errorf("skipped = %d, want 4", skipped)
	}
	for _, s := range ss {
		if s.Agent != "cursor" || s.Origin != model.OriginLive || s.Root != "" {
			t.Errorf("%s: agent/origin/root = %q %q %q", s.NativeID, s.Agent, s.Origin, s.Root)
		}
		for _, m := range s.Messages {
			if strings.Contains(m.Text, "hidden reasoning") || strings.Contains(m.Text, "token-must-never") || strings.Contains(m.Text, "subagent") {
				t.Errorf("%s: leaked %q", s.NativeID, m.Text)
			}
			if m.Kind == model.KindToolOutput && len(m.Text) > model.MaxToolOutput {
				t.Errorf("%s: tool output of %d bytes", s.NativeID, len(m.Text))
			}
		}
	}

	n := got["c-new"]
	if n.Cwd != `D:\work\alpha` || n.Title != "Fix the login flow" || n.TitleSource != model.TitleAgent {
		t.Errorf("c-new cwd/title = %q %q %q", n.Cwd, n.Title, n.TitleSource)
	}
	// lastUpdatedAt (10:03:20) is older than the last bubble: UpdatedAt is the latest of both.
	if !n.StartedAt.Equal(ms(1765706000000)) || !n.UpdatedAt.Equal(time.Date(2025, 12, 14, 10, 5, 0, 0, time.UTC)) {
		t.Errorf("c-new times = %v %v", n.StartedAt, n.UpdatedAt)
	}
	if n.Coverage != model.CoveragePartial {
		t.Errorf("c-new coverage = %s, want partial (a bubble is missing)", n.Coverage)
	}
	type rk struct {
		role model.Role
		kind model.Kind
		text string
	}
	wantMsgs := []rk{
		{model.RoleUser, model.KindText, "The login form rejects valid users"},
		{model.RoleAssistant, model.KindText, "Let me run the tests."},
		{model.RoleAssistant, model.KindToolCall, "run_terminal_cmd: npm test"},
		{model.RoleTool, model.KindToolOutput, "ok"}, // outputs are trimmed
		{model.RoleAssistant, model.KindToolCall, "read_file: src/app.go"},
		{model.RoleTool, model.KindToolOutput, ""}, // checked below
		{model.RoleAssistant, model.KindToolCall, "grep: TODO"},
		{model.RoleTool, model.KindToolOutput, "tool failed"},
		{model.RoleAssistant, model.KindText, "Fixed: the session cookie was not set."},
	}
	if len(n.Messages) != len(wantMsgs) {
		for i, m := range n.Messages {
			t.Logf("msg %d: %s %s %.40q", i, m.Role, m.Kind, m.Text)
		}
		t.Fatalf("c-new has %d messages, want %d", len(n.Messages), len(wantMsgs))
	}
	for i, w := range wantMsgs {
		m := n.Messages[i]
		if m.Role != w.role || m.Kind != w.kind || (w.text != "" && m.Text != w.text) {
			t.Errorf("c-new msg %d = %s %s %.60q, want %s %s %q", i, m.Role, m.Kind, m.Text, w.role, w.kind, w.text)
		}
	}
	if out := n.Messages[5].Text; len(out) > model.MaxToolOutput || len(out) < model.MaxToolOutput-4 || !strings.HasPrefix(out, "éé") {
		t.Errorf("long tool output: %d bytes", len(out))
	}
	if !n.Messages[0].At.Equal(time.Date(2025, 12, 14, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("bubble time = %v", n.Messages[0].At)
	}

	o := got["c-old"]
	if o.Cwd != `C:\code\proj two` || o.Title != "Old chat" || o.Coverage != model.CoverageFull || len(o.Messages) != 2 {
		t.Errorf("c-old = %q %q %s %d msgs", o.Cwd, o.Title, o.Coverage, len(o.Messages))
	} else if o.Messages[0].Role != model.RoleUser || o.Messages[1].Role != model.RoleAssistant || !o.Messages[1].At.Equal(ms(1740323130490)) ||
		!o.Messages[0].At.Equal(ms(1740323111487)) { // a user bubble takes the start of the reply

		t.Errorf("c-old messages = %+v", o.Messages)
	}
	if !o.StartedAt.Equal(ms(1740322996177)) || !o.UpdatedAt.Equal(ms(1740323614338)) {
		t.Errorf("c-old times = %v %v", o.StartedAt, o.UpdatedAt)
	}

	w := got["c-wid"]
	if w.Cwd != `E:\repo\gamma` {
		t.Errorf("c-wid cwd = %q (workspaceIdentifier wins over the workspace DB)", w.Cwd)
	}
	if w.Title != "Add a README for gamma" || w.TitleSource != model.TitlePrompt {
		t.Errorf("c-wid title = %q %q", w.Title, w.TitleSource)
	}
	if !w.UpdatedAt.Equal(time.Date(2025, 12, 29, 9, 21, 0, 500e6, time.UTC)) {
		t.Errorf("c-wid updated = %v", w.UpdatedAt)
	}
	if !w.StartedAt.Equal(time.Date(2025, 12, 29, 9, 20, 0, 0, time.UTC)) {
		t.Errorf("c-wid started = %v (earliest of createdAt and message times)", w.StartedAt)
	}

	if u := got["c-uri"]; u.Cwd != `D:\work\delta` {
		t.Errorf("c-uri cwd = %q", u.Cwd)
	}

	tr := got["c-trans"]
	if tr.Cwd != `D:\work\alpha` || tr.Title != "Transcript chat" || len(tr.Messages) != 2 ||
		tr.Messages[0].Text != "hello from the transcript" || tr.Messages[1].Text != "hi" {
		t.Errorf("c-trans = %q %q %+v", tr.Cwd, tr.Title, tr.Messages)
	}
	if !tr.StartedAt.Equal(ms(1768000000000)) || !tr.UpdatedAt.Equal(ms(1768000900000)) {
		t.Errorf("c-trans times = %v %v", tr.StartedAt, tr.UpdatedAt)
	}

	to := got["t-only"]
	if to.Cwd != `D:\work\alpha` {
		t.Errorf("t-only cwd = %q (slug d-work-alpha matches a known workspace)", to.Cwd)
	}
	if to.Title != "summarize the notes" || to.TitleSource != model.TitlePrompt {
		t.Errorf("t-only title = %q %q", to.Title, to.TitleSource)
	}
	if len(to.Messages) != 5 {
		for i, m := range to.Messages {
			t.Logf("msg %d: %s %s %.40q", i, m.Role, m.Kind, m.Text)
		}
		t.Fatalf("t-only has %d messages, want 5", len(to.Messages))
	}
	if to.Messages[0].Text != "summarize the notes" || !to.Messages[0].At.Equal(time.Date(2025, 12, 15, 6, 30, 0, 0, time.UTC)) {
		t.Errorf("t-only first = %q %v", to.Messages[0].Text, to.Messages[0].At)
	}
	if len(to.Messages[1].Text) != 2<<20 || to.Messages[2].Kind != model.KindToolCall || to.Messages[2].Text != "Shell: dir /b" {
		t.Errorf("t-only assistant/tool = %d %s %q", len(to.Messages[1].Text), to.Messages[2].Kind, to.Messages[2].Text)
	}
	if to.Messages[3].Role != model.RoleTool || to.Messages[3].Text != "a.txt" || to.Messages[4].Text != "all done" {
		t.Errorf("t-only tail = %+v %+v", to.Messages[3], to.Messages[4])
	}
	if !to.StartedAt.Equal(time.Date(2025, 12, 15, 6, 30, 0, 0, time.UTC)) || to.UpdatedAt.Before(to.StartedAt) {
		t.Errorf("t-only times = %v %v", to.StartedAt, to.UpdatedAt)
	}

	if nest := got["t-nested"]; nest.Cwd != `D:\work\alpha` || len(nest.Messages) != 1 {
		t.Errorf("t-nested = %q %d", nest.Cwd, len(nest.Messages))
	}
	tx := got["t-txt"]
	if tx.Cwd != "" || len(tx.Messages) != 2 || tx.Messages[0].Text != "plain text prompt" ||
		tx.Messages[1].Role != model.RoleAssistant || tx.Messages[1].Text != "plain text answer\nsecond line" {
		t.Errorf("t-txt = %q %+v", tx.Cwd, tx.Messages)
	}
}

// snapshot records every path under dir, with size and mtime for files
// (os.Stat, not the directory listing, whose cached times NTFS updates lazily).
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[p] = "dir"
			return nil
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		out[p] = fmt.Sprintf("%d|%d", info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadIsReadOnly(t *testing.T) {
	env := buildFixture(t)
	root := filepath.Dir(env.Home)
	before := snapshot(t, root)
	adapterstest.ReadAll(t, New(), env)
	after := snapshot(t, root)
	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed", p)
		}
	}
}

func TestReadCanceled(t *testing.T) {
	env := buildFixture(t)
	srcs, _ := New().Sources(env)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Read(ctx, env, srcs[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read on a canceled context = %v", err)
	}
}

func TestFileURIPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"file:///d%3A/work/alpha", `D:\work\alpha`},
		{"file:///c:/code/proj%20two/", `C:\code\proj two`},
		{"file://server/share/x", `\\server\share\x`},
		{"vscode-remote://ssh-remote%2Bbox/home/u", "vscode-remote://ssh-remote%2Bbox/home/u"},
		{"", ""},
	}
	for _, c := range cases {
		if got := fileURIPath(c.in); got != c.want {
			t.Errorf("fileURIPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugKey(t *testing.T) {
	if slugKey(`D:\Projects\Cursor\DebtManager`) != slugKey("d-Projects-Cursor-DebtManager") {
		t.Error("path and slug keys differ")
	}
	if slugKey(`C:\Users\someone\Desktop\code\pos-system`) != slugKey("c-Users-someone-Desktop-code-pos-system") {
		t.Error("path with dash and slug keys differ")
	}
}

func TestParseTranscriptTimestamp(t *testing.T) {
	got, ok := parseTranscriptTime("Wednesday, Feb 4, 2026, 11:05 PM (UTC-5:30)")
	if !ok || !got.Equal(time.Date(2026, 2, 5, 4, 35, 0, 0, time.UTC)) {
		t.Errorf("got %v %v", got, ok)
	}
	if _, ok := parseTranscriptTime("garbage"); ok {
		t.Error("garbage parsed")
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
