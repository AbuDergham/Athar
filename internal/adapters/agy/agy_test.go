package agy

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

// Synthetic conversation ids used by the fixture tree.
const (
	idA = "11111111-1111-4111-8111-111111111111" // DB with steps + history prompts + brain message
	idB = "22222222-2222-4222-8222-222222222222" // history.jsonl only
	idC = "33333333-3333-4333-8333-333333333333" // DB with 0 steps: no session
	idD = "44444444-4444-4444-8444-444444444444" // WAL DB held open, damaged metadata blob and one damaged step
	idE = "55555555-5555-4555-8555-555555555555" // history.jsonl only, one ~2 MB prompt
)

// t0 is 2026-06-17T08:00:00Z, the base time of conversation A.
var t0 = time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)

func at(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

// thinkingMarker appears only in thinking fields; it must never reach a message.
const thinkingMarker = "THINKSECRET"

// ---- protobuf builders (synthetic blobs shaped like agy's) ----

func pbStr(num protowire.Number, s string) []byte {
	b := protowire.AppendTag(nil, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func pbMsg(num protowire.Number, parts ...[]byte) []byte {
	var inner []byte
	for _, p := range parts {
		inner = append(inner, p...)
	}
	b := protowire.AppendTag(nil, num, protowire.BytesType)
	return protowire.AppendBytes(b, inner)
}

func pbVarint(num protowire.Number, v uint64) []byte {
	b := protowire.AppendTag(nil, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func pbTime(num protowire.Number, t time.Time) []byte {
	return pbMsg(num, pbVarint(1, uint64(t.Unix())), pbVarint(2, uint64(t.Nanosecond())))
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// stepMetadata is the step metadata message (also embedded as payload field 5).
func stepMetadata(t time.Time, source uint64, toolCall []byte) []byte {
	return cat(pbTime(1, t), pbVarint(3, source), toolCall,
		pbStr(12, "99999999-9999-4999-8999-999999999999"))
}

func toolCall(num protowire.Number, id, name, args string) []byte {
	return pbMsg(num, pbStr(1, id), pbStr(2, name), pbStr(3, args), pbStr(9, name))
}

type step struct {
	idx      int
	typ      int
	metadata []byte
	payload  []byte
}

func userStep(idx int, t time.Time, text string, useItems bool) step {
	md := stepMetadata(t, 4, nil)
	var input []byte
	if useItems {
		input = pbMsg(19, pbMsg(3, pbStr(1, text)), pbStr(4, ""))
	} else {
		input = pbMsg(19, pbStr(2, text), pbMsg(3, pbStr(1, text)), pbStr(4, ""))
	}
	return step{idx, 14, md, cat(pbVarint(1, 14), pbVarint(4, 3), pbMsg(5, md), input)}
}

func plannerStep(idx int, t time.Time, fields ...[]byte) step {
	md := stepMetadata(t, 2, nil)
	resp := pbMsg(20, append([][]byte{pbStr(6, "bot-0000000000000000000000000000000000")}, fields...)...)
	return step{idx, 15, md, cat(pbVarint(1, 15), pbVarint(4, 3), pbMsg(5, md), resp)}
}

func otherStep(idx, typ int, t time.Time, extra []byte) step {
	md := stepMetadata(t, 5, nil)
	return step{idx, typ, md, cat(pbVarint(1, uint64(typ)), pbVarint(4, 3), pbMsg(5, md), extra)}
}

func trajectoryBlob(id, workspaceURI string, created time.Time) []byte {
	return cat(
		pbMsg(1, pbStr(1, workspaceURI), pbStr(3, "")),
		pbTime(2, created),
		pbStr(3, "88888888-8888-4888-8888-888888888888"),
		pbStr(6, id),
		pbStr(7, workspaceURI),
		pbStr(18, "77777777-7777-4777-8777-777777777777"),
	)
}

const schemaSQL = "CREATE TABLE `trajectory_meta` (`trajectory_id` text,`cascade_id` text,`trajectory_type` integer,`source` integer,PRIMARY KEY (`trajectory_id`));" +
	"CREATE TABLE `steps` (`idx` integer,`step_type` integer NOT NULL DEFAULT 0,`status` integer NOT NULL DEFAULT 0,`has_subtrajectory` numeric NOT NULL DEFAULT false,`metadata` blob,`error_details` blob,`permissions` blob,`task_details` blob,`render_info` blob,`step_payload` blob,`step_format` integer NOT NULL DEFAULT 0,PRIMARY KEY (`idx`));" +
	"CREATE TABLE `gen_metadata` (`idx` integer,`data` blob,`size` integer NOT NULL DEFAULT 0,PRIMARY KEY (`idx`));" +
	"CREATE TABLE `trajectory_metadata_blob` (`id` text DEFAULT \"main\",`data` blob,PRIMARY KEY (`id`));"

// writeConversationDB creates a conversation database like agy's. With keepOpen,
// the database is in WAL mode, its rows stay in the -wal (no checkpoint) and the
// connection stays open until the test ends, like a running agy.
func writeConversationDB(t *testing.T, path, id string, blob []byte, steps []step, keepOpen bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if keepOpen {
		var mode string
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal_mode=WAL: %q %v", mode, err)
		}
		exec("PRAGMA wal_autocheckpoint=0")
	}
	exec(schemaSQL)
	exec("INSERT INTO trajectory_meta VALUES (?, ?, 4, 17)", "66666666-6666-4666-8666-666666666666", id)
	if blob != nil {
		exec("INSERT INTO trajectory_metadata_blob (id, data) VALUES ('main', ?)", blob)
	}
	for _, s := range steps {
		exec("INSERT INTO steps (idx, step_type, status, metadata, step_payload) VALUES (?, ?, 3, ?, ?)",
			s.idx, s.typ, s.metadata, s.payload)
	}
	if keepOpen {
		t.Cleanup(func() { conn.Close(); db.Close() })
		if _, err := os.Stat(path + "-wal"); err != nil {
			t.Fatalf("expected a -wal next to the fixture db: %v", err)
		}
		return
	}
	conn.Close()
	db.Close()
}

// buildFixture copies testdata into a temp root, adds a ~2 MB prompt and a
// half-written last line to history.jsonl, and builds the conversation DBs.
func buildFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := copyTree(filepath.Join("testdata", "home"), filepath.Join(root, "home")); err != nil {
		t.Fatal(err)
	}
	agyDir := filepath.Join(root, "home", ".gemini", "antigravity-cli")
	convDir := filepath.Join(agyDir, "conversations")

	// History: a single ~2 MB record, then an unterminated (half-written) line.
	f, err := os.OpenFile(filepath.Join(agyDir, "history.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"display":"` + bigPrompt() + `","timestamp":1782900000000,"workspace":"C:\\work\\big","conversationId":"` + idE + `"}` + "\n")
	f.WriteString(`{"display":"half written","timestamp":1781773400000,"workspace":"D:\\beta","conversationId":"` + idB + `"`)
	f.Close()

	// A: a full conversation.
	writeConversationDB(t, filepath.Join(convDir, idA+".db"), idA,
		trajectoryBlob(idA, "file:///C:/work/alpha%20one", at(1.25)),
		[]step{
			userStep(0, at(10), "build a tiny scraper for the alpha site", false),
			otherStep(1, 98, at(11), nil),
			plannerStep(2, at(12),
				pbStr(3, "**Planning** "+thinkingMarker),
				toolCall(7, "tc000001", "run_command", `{"CommandLine":"go mod init alpha","Cwd":"C:\\work\\alpha one","toolAction":"Init","toolSummary":"init"}`),
				toolCall(7, "tc000002", "view_file", `{"AbsolutePath":"C:\\work\\alpha one\\main.go","StartLine":1}`),
				pbVarint(12, 2)),
			otherStep(3, 21, at(13), pbMsg(28, pbStr(2, `C:\work\alpha one`), pbStr(23, "go mod init alpha"), pbMsg(21, pbStr(1, "go: creating new go.mod")))),
			plannerStep(4, at(20),
				pbStr(1, "Done. The scraper lives in main.go."),
				pbStr(3, "**Wrapping up** "+thinkingMarker),
				pbStr(8, "Done. The scraper lives in main.go.")),
			userStep(5, at(30), "now add tests", true),
			plannerStep(6, at(40),
				pbStr(8, "تمت إضافة الاختبارات"),
				toolCall(7, "tc000003", "write_to_file", `{"TargetFile":"C:\\work\\alpha one\\main_test.go","CodeContent":"package main"}`)),
			otherStep(7, 101, at(41), pbMsg(114, pbStr(1, "task notification text"), pbStr(3, "task_notification"))),
		}, false)

	// C: created but never used (0 steps).
	writeConversationDB(t, filepath.Join(convDir, idC+".db"), idC,
		trajectoryBlob(idC, "file:///C:/work/gamma", at(500)), nil, false)

	// D: live WAL database, damaged metadata blob, one damaged step.
	writeConversationDB(t, filepath.Join(convDir, idD+".db"), idD,
		[]byte{0xff, 0xff, 0xff, 0xff},
		[]step{
			userStep(0, at(3600), "hello from a live database", false),
			{idx: 1, typ: 15, metadata: []byte{0xff, 0xff}, payload: []byte{0xff, 0xff, 0xff}},
			plannerStep(2, at(3601), pbStr(1, "hi")),
		}, true)
	return root
}

func bigPrompt() string {
	var b strings.Builder
	b.WriteString("big   prompt start ")
	for b.Len() < 2<<20 {
		b.WriteString("lorem ipsum dolor sit amet ")
	}
	return b.String()
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

type fileState struct {
	size  int64
	mtime time.Time
}

// snapshot records every file's size and mtime and every folder's presence.
// Folder mtimes are not compared: on NTFS the copy in the parent's index is
// updated lazily, and any file created inside shows up as a new entry anyway.
// Files are stat'ed directly (not via DirEntry.Info, which reads that index).
func snapshot(t *testing.T, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[p] = fileState{}
			return nil
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		out[p] = fileState{info.Size(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func find(t *testing.T, sessions []model.Session, id string, origin model.Origin) model.Session {
	t.Helper()
	for _, s := range sessions {
		if s.NativeID == id && s.Origin == origin {
			return s
		}
	}
	t.Fatalf("no session %s with origin %s", id, origin)
	return model.Session{}
}

func TestIdentityAndCaps(t *testing.T) {
	a := New()
	if a.ID() != "agy" || a.Name() != "Antigravity CLI" || a.Version() < 1 {
		t.Fatalf("identity: %q %q %d", a.ID(), a.Name(), a.Version())
	}
	c := a.Caps()
	if !reflect.DeepEqual(c.Resume, []string{"agy", "--conversation", "{id}"}) {
		t.Errorf("Resume = %q", c.Resume)
	}
	if !reflect.DeepEqual(c.SkipPermissions, []string{"--dangerously-skip-permissions"}) {
		t.Errorf("SkipPermissions = %q", c.SkipPermissions)
	}
	if !reflect.DeepEqual(c.ProgramCandidates, []string{`%LOCALAPPDATA%\agy\bin\agy.exe`}) {
		t.Errorf("ProgramCandidates = %q", c.ProgramCandidates)
	}
	if c.Fork != nil || c.App != nil || c.RootEnv != "" {
		t.Errorf("unexpected caps: %+v", c)
	}
}

func TestSources(t *testing.T) {
	root := buildFixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	agyDir := filepath.Join(env.Home, ".gemini", "antigravity-cli")
	conv := filepath.Join(agyDir, "conversations")
	var paths []string
	for _, s := range srcs {
		paths = append(paths, s.Path)
		if s.Adapter != "agy" || s.Root != "" {
			t.Errorf("source %s: adapter %q root %q", s.Path, s.Adapter, s.Root)
		}
		if len(s.Files) == 0 || s.Files[0] != s.Path {
			t.Errorf("source %s: Files must start with Path: %q", s.Path, s.Files)
		}
	}
	want := []string{
		filepath.Join(conv, idA+".db"),
		filepath.Join(conv, idC+".db"),
		filepath.Join(conv, idD+".db"),
		filepath.Join(agyDir, "history.jsonl"),
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("source paths\n got %q\nwant %q", paths, want)
	}
	msgDir := filepath.Join(agyDir, "brain", idA, ".system_generated", "messages")
	wantA := []string{
		filepath.Join(conv, idA+".db"),
		filepath.Join(msgDir, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.json"),
		filepath.Join(msgDir, "read.json"),
	}
	if !reflect.DeepEqual(srcs[0].Files, wantA) {
		t.Errorf("A files\n got %q\nwant %q", srcs[0].Files, wantA)
	}
	// The live WAL database lists its sidecars.
	wantD := []string{filepath.Join(conv, idD+".db"), filepath.Join(conv, idD+".db-wal"), filepath.Join(conv, idD+".db-shm")}
	if !reflect.DeepEqual(srcs[2].Files, wantD) {
		t.Errorf("D files\n got %q\nwant %q", srcs[2].Files, wantD)
	}

	// A missing agy folder is not an error.
	empty := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	if srcs, err := New().Sources(empty); err != nil || len(srcs) != 0 {
		t.Errorf("missing root: %v %v", srcs, err)
	}
}

func TestReadFixture(t *testing.T) {
	root := buildFixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	before := snapshot(t, filepath.Join(root, "home"))

	sessions, skipped := adapterstest.ReadAll(t, New(), env)

	// Read-only: nothing in the agent folder changed or appeared.
	if after := snapshot(t, filepath.Join(root, "home")); !reflect.DeepEqual(before, after) {
		t.Errorf("fixture tree changed during the read:\nbefore %v\nafter  %v", before, after)
	}

	// history: no conversation id (1) + not JSON (1); D: damaged blob (1) + damaged step (1).
	if skipped != 4 {
		t.Errorf("skipped = %d, want 4", skipped)
	}
	if len(sessions) != 5 {
		for _, s := range sessions {
			t.Logf("session %s %s", s.NativeID, s.Origin)
		}
		t.Fatalf("sessions = %d, want 5", len(sessions))
	}
	for _, s := range sessions {
		if s.Agent != "agy" || s.Root != "" || s.Machine != "" {
			t.Errorf("%s: agent %q root %q machine %q", s.NativeID, s.Agent, s.Root, s.Machine)
		}
		for _, m := range s.Messages {
			if strings.Contains(m.Text, thinkingMarker) {
				t.Errorf("%s: thinking text leaked into a message: %q", s.NativeID, m.Text)
			}
			if m.Kind == model.KindToolOutput && len(m.Text) > model.MaxToolOutput {
				t.Errorf("%s: tool output longer than MaxToolOutput", s.NativeID)
			}
		}
	}

	// A from its database: the full conversation.
	a := find(t, sessions, idA, model.OriginLive)
	if a.Cwd != `C:\work\alpha one` {
		t.Errorf("A cwd = %q", a.Cwd)
	}
	if a.Title != "build a tiny scraper for the alpha site" || a.TitleSource != model.TitlePrompt {
		t.Errorf("A title = %q (%s)", a.Title, a.TitleSource)
	}
	if !a.StartedAt.Equal(at(1.25)) || !a.UpdatedAt.Equal(at(41)) {
		t.Errorf("A times = %v .. %v", a.StartedAt, a.UpdatedAt)
	}
	if a.Coverage != model.CoverageFull {
		t.Errorf("A coverage = %s", a.Coverage)
	}
	wantMsgs := []model.Message{
		{Role: model.RoleUser, Kind: model.KindText, At: at(10), Text: "build a tiny scraper for the alpha site"},
		{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at(12), Text: "run_command: go mod init alpha"},
		{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at(12), Text: `view_file: C:\work\alpha one\main.go`},
		{Role: model.RoleTool, Kind: model.KindToolOutput, At: at(14.5), Text: "Background command finished: exit code 0"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: at(20), Text: "Done. The scraper lives in main.go."},
		{Role: model.RoleUser, Kind: model.KindText, At: at(30), Text: "now add tests"},
		{Role: model.RoleAssistant, Kind: model.KindText, At: at(40), Text: "تمت إضافة الاختبارات"},
		{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at(40), Text: `write_to_file: C:\work\alpha one\main_test.go`},
	}
	if !reflect.DeepEqual(a.Messages, wantMsgs) {
		t.Errorf("A messages:\n got %s\nwant %s", dumpMsgs(a.Messages), dumpMsgs(wantMsgs))
	}

	// A from history.jsonl: the prompt only (slash commands ignored).
	ah := find(t, sessions, idA, model.OriginPromptLog)
	if ah.Coverage != model.CoveragePrompts || ah.Cwd != `C:\work\alpha one` || len(ah.Messages) != 1 ||
		ah.Messages[0].Text != "build a tiny scraper for the alpha site" || !ah.Messages[0].At.Equal(at(10)) {
		t.Errorf("A prompt-log = %+v", ah)
	}

	// B: history only; Arabic title with whitespace collapsed; half-written last line ignored.
	b := find(t, sessions, idB, model.OriginPromptLog)
	if b.Cwd != `D:\beta` || b.Coverage != model.CoveragePrompts {
		t.Errorf("B = %q %s", b.Cwd, b.Coverage)
	}
	if b.Title != "مرحبا، رتّب ملفات المشروع" || b.TitleSource != model.TitlePrompt {
		t.Errorf("B title = %q (%s)", b.Title, b.TitleSource)
	}
	if len(b.Messages) != 2 || b.Messages[0].Role != model.RoleUser || b.Messages[1].Text != "then write a summary" {
		t.Errorf("B messages = %s", dumpMsgs(b.Messages))
	}
	b0 := time.Date(2026, 6, 18, 9, 0, 1, 0, time.UTC)
	if !b.StartedAt.Equal(b0) || !b.UpdatedAt.Equal(b0.Add(99*time.Second)) {
		t.Errorf("B times = %v .. %v", b.StartedAt, b.UpdatedAt)
	}

	// D: the rows still in the -wal are read; damaged parts skipped.
	d := find(t, sessions, idD, model.OriginLive)
	if d.Cwd != "" || !d.StartedAt.Equal(at(3600)) || !d.UpdatedAt.Equal(at(3601)) || d.Coverage != model.CoverageFull {
		t.Errorf("D = cwd %q %v..%v %s", d.Cwd, d.StartedAt, d.UpdatedAt, d.Coverage)
	}
	if len(d.Messages) != 2 || d.Messages[0].Text != "hello from a live database" || d.Messages[1].Text != "hi" {
		t.Errorf("D messages = %s", dumpMsgs(d.Messages))
	}
	if d.Title != "hello from a live database" {
		t.Errorf("D title = %q", d.Title)
	}

	// E: the ~2 MB prompt arrives whole; the title is its first 100 runes.
	e := find(t, sessions, idE, model.OriginPromptLog)
	if len(e.Messages) != 1 || len(e.Messages[0].Text) < 2<<20 || e.Messages[0].Text != bigPrompt() {
		t.Errorf("E message length = %d", len(e.Messages[0].Text))
	}
	if e.Cwd != `C:\work\big` || len([]rune(e.Title)) != 100 || !strings.HasPrefix(e.Title, "big prompt start lorem") {
		t.Errorf("E cwd %q title %q", e.Cwd, e.Title)
	}

	// C (0 steps, not in history) produced nothing.
	for _, s := range sessions {
		if s.NativeID == idC {
			t.Errorf("session for the empty database C")
		}
	}
}

func TestReadCancelled(t *testing.T) {
	root := buildFixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Read(ctx, env, srcs[0]); err == nil {
		t.Error("Read with a cancelled context succeeded")
	}
}

func TestReadUnknownSource(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	_, err := New().Read(context.Background(), env, adapters.Source{Adapter: "agy", Path: filepath.Join(env.Home, "x.txt")})
	if err == nil {
		t.Error("unknown source accepted")
	}
}

func dumpMsgs(ms []model.Message) string {
	var lines []string
	for _, m := range ms {
		txt := m.Text
		if len(txt) > 60 {
			txt = txt[:60] + "…"
		}
		lines = append(lines, "\n  "+string(m.Role)+"/"+string(m.Kind)+" "+m.At.UTC().Format(time.RFC3339Nano)+" "+txt)
	}
	return strings.Join(lines, "")
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }

// A notification file that is not JSON yet and changed moments ago is still
// being written: the conversation is retried on the next sync. Once it is old,
// it is skipped instead.
func TestReadHalfWrittenNotification(t *testing.T) {
	root := buildFixture(t)
	env := adapterstest.FixtureEnv(t, root, nil)
	bad := filepath.Join(env.Home, ".gemini", "antigravity-cli", "brain", idA, ".system_generated", "messages", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.json")
	if err := os.WriteFile(bad, []byte(`{"timestamp":"2026-06-17T08:00:15Z","content":"half`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := adapters.Source{Adapter: "agy", Path: filepath.Join(env.Home, ".gemini", "antigravity-cli", "conversations", idA+".db")}
	if _, err := New().Read(context.Background(), env, src); !errors.Is(err, adapters.ErrRetryLater) {
		t.Fatalf("fresh half-written notification: err = %v, want ErrRetryLater", err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(bad, old, old); err != nil {
		t.Fatal(err)
	}
	res, err := New().Read(context.Background(), env, src)
	if err != nil || len(res.Sessions) != 1 || res.Skipped != 1 {
		t.Fatalf("old damaged notification: sessions %d skipped %d err %v", len(res.Sessions), res.Skipped, err)
	}
}
