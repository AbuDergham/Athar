package antigravity

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
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

// Synthetic conversation ids.
const (
	idA = "aaaaaaaa-0000-4000-8000-00000000000a" // main root: artifacts, agyhub summary
	idB = "bbbbbbbb-0000-4000-8000-00000000000b" // backup only: two summaries, no brain
	idC = "cccccccc-0000-4000-8000-00000000000c" // ide only: full step log
	idD = "dddddddd-0000-4000-8000-00000000000d" // main root: nothing known
	idE = "eeeeeeee-0000-4000-8000-00000000000e" // brain folder only (no .pb)
)

// oldTime is the mtime given to every fixture file (older than the settle window).
var oldTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// --- protobuf encoding helpers (fixtures are encoded, never copied) ---

func pbBytes(b []byte, num protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func pbVarint(b []byte, num protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func pbTime(b []byte, num protowire.Number, t time.Time) []byte {
	var ts []byte
	ts = pbVarint(ts, 1, uint64(t.Unix()))
	ts = pbVarint(ts, 2, uint64(t.Nanosecond()))
	return pbBytes(b, num, ts)
}

type fixtureSummary struct {
	title                    string
	uri                      string // field 9.1
	fallbackURI              string // field 17.1.1
	created, updated, lastIn time.Time
}

// encodeSummary builds a Summary message with the real field layout.
func encodeSummary(s fixtureSummary) []byte {
	var b []byte
	if s.title != "" {
		b = pbBytes(b, 1, []byte(s.title))
	}
	b = pbVarint(b, 2, 12) // step count
	if !s.updated.IsZero() {
		b = pbTime(b, 3, s.updated)
	}
	b = pbBytes(b, 4, []byte("99999999-0000-4000-8000-000000000099"))
	b = pbVarint(b, 5, 1)
	if !s.created.IsZero() {
		b = pbTime(b, 7, s.created)
	}
	if s.uri != "" {
		var ws []byte
		ws = pbBytes(ws, 1, []byte(s.uri))
		ws = pbBytes(ws, 3, nil)
		b = pbBytes(b, 9, ws)
	}
	if !s.lastIn.IsZero() {
		b = pbTime(b, 10, s.lastIn)
	}
	b = pbBytes(b, 15, nil)
	b = pbVarint(b, 16, 11)
	if s.fallbackURI != "" {
		var inner, p []byte
		inner = pbBytes(inner, 1, []byte(s.fallbackURI))
		p = pbBytes(p, 1, inner)
		p = pbBytes(p, 18, []byte("project-1"))
		b = pbBytes(b, 17, p)
	}
	return pbVarint(b, 22, 4)
}

// encodeAgyhub builds agyhub_summaries_proto.pb: repeated 1 {1: id, 2: Summary}.
func encodeAgyhub(entries map[string]fixtureSummary, order ...string) []byte {
	var b []byte
	for _, cid := range order {
		var e []byte
		e = pbBytes(e, 1, []byte(cid))
		e = pbBytes(e, 2, encodeSummary(entries[cid]))
		b = pbBytes(b, 1, e)
	}
	return b
}

// encodeStateValue builds the state.vscdb value:
// base64(repeated 1 {1: id, 2: {1: base64(Summary)}}).
func encodeStateValue(entries map[string]fixtureSummary, order ...string) string {
	var b []byte
	for _, cid := range order {
		var wrap, e []byte
		wrap = pbBytes(wrap, 1, []byte(base64.StdEncoding.EncodeToString(encodeSummary(entries[cid]))))
		e = pbBytes(e, 1, []byte(cid))
		e = pbBytes(e, 2, wrap)
		b = pbBytes(b, 1, e)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// writeStateDB creates a VS Code state.vscdb with the given ItemTable rows.
func writeStateDB(t *testing.T, path string, rows map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	for k, v := range rows {
		if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)`, k, v); err != nil {
			t.Fatal(err)
		}
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

// ageTree gives every file under root the mtime oldTime.
func ageTree(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return os.Chtimes(p, oldTime, oldTime)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Fixture times.
var (
	aCreated = time.Date(2026, 3, 1, 10, 0, 0, 500, time.UTC)
	aUpdated = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	aLastIn  = time.Date(2026, 3, 1, 11, 30, 0, 0, time.UTC)
	aTaskAt  = time.Date(2026, 3, 1, 11, 45, 0, 123456789, time.UTC)
	bCreated = time.Date(2025, 12, 5, 8, 0, 0, 0, time.UTC)
	bOldUpd  = time.Date(2025, 12, 5, 9, 0, 0, 0, time.UTC)
	bNewUpd  = time.Date(2025, 12, 6, 9, 0, 0, 0, time.UTC)
	cCreated = time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)
	cUpdated = time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC)
	eWalkAt  = time.Date(2026, 4, 1, 7, 0, 0, 0, time.UTC)
)

// bigPrompt is a ~2 MB prompt carried by one step-log line.
var bigPrompt = strings.Repeat("long prompt ", 175000)

// toolOutput is longer than model.MaxToolOutput.
var toolOutput = "ok\n" + strings.Repeat("x", 3000)

// buildTree writes the synthetic Antigravity data under a temp root.
func buildTree(t *testing.T) (adapters.Env, string) {
	t.Helper()
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	gem := filepath.Join(env.Home, ".gemini")
	main, backup, ide := filepath.Join(gem, "antigravity"), filepath.Join(gem, "antigravity-backup"), filepath.Join(gem, "antigravity-ide")

	// Encrypted conversation files (content is never read).
	for _, p := range []string{
		filepath.Join(main, "conversations", idA+".pb"),
		filepath.Join(main, "conversations", idD+".pb"),
		filepath.Join(backup, "conversations", idA+".pb"),
		filepath.Join(backup, "conversations", idB+".pb"),
		filepath.Join(ide, "conversations", idC+".pb"),
		filepath.Join(ide, "conversations", "conversations", idD+".pb"), // nested duplicate folder: ignored
		filepath.Join(main, "conversations", "not-an-id.pb"),
		filepath.Join(main, "implicit", "ffffffff-0000-4000-8000-00000000000f.pb"), // background runs: ignored
	} {
		writeFile(t, p, "\x8a\x13\xfe\x00opaque")
	}

	// A: artifacts in the main brain folder.
	ba := filepath.Join(main, "brain", idA)
	writeFile(t, filepath.Join(ba, "task.md"), "\ufeff# Task\n\n- [x] write the parser\n")
	writeFile(t, filepath.Join(ba, "task.md.metadata.json"), `{"artifactType":"ARTIFACT_TYPE_TASK","summary":"s","updatedAt":"`+aTaskAt.Format(time.RFC3339Nano)+`","version":"3"}`)
	writeFile(t, filepath.Join(ba, "task.md.resolved"), "# Task (old)\n")
	writeFile(t, filepath.Join(ba, "task.md.resolved.0"), "# Task (older)\n")
	writeFile(t, filepath.Join(ba, "implementation_plan.md.resolved.9"), "plan v9")
	writeFile(t, filepath.Join(ba, "implementation_plan.md.resolved.10"), "plan v10")
	writeFile(t, filepath.Join(ba, "notes_on-design.md"), "design notes")
	writeFile(t, filepath.Join(ba, "empty.md"), "  \n")
	writeFile(t, filepath.Join(ba, "picture_1765.png"), "png")
	writeFile(t, filepath.Join(ba, ".tempmediaStorage", "x.md"), "ignored")
	// The backup copy of A's brain is older and must not be used.
	writeFile(t, filepath.Join(backup, "brain", idA, "task.md"), "# stale backup task")

	// C: the main brain folder exists but is empty; the ide one has the step log.
	if err := os.MkdirAll(filepath.Join(main, "brain", idC), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"step_index":0,"type":"CONVERSATION_HISTORY","source":"SYSTEM","status":"DONE","created_at":"2026-05-20T09:00:00Z"}`,
		`{"step_index":1,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-05-20T09:00:01Z","content":"<USER_REQUEST>\nFix   the parser\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\ncursor at line 3\n</ADDITIONAL_METADATA>"}`,
		`{"step_index":2,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-05-20T09:00:02Z","content":"Looking at it.","thinking":"private reasoning","tool_calls":[{"name":"run_command","args":{"CommandLine":"go test ./...","Cwd":"C:\\work\\gamma","toolAction":"Run","toolSummary":"tests"}},{"name":"view_file","args":{"AbsolutePath":"C:\\work\\gamma\\main.go"}},{"name":"write_to_file","args":{"CodeContent":"package main","Overwrite":true}}]}`,
		`{"step_index":3,"type":"RUN_COMMAND","source":"MODEL","status":"RUNNING","created_at":"2026-05-20T09:00:03Z","content":"partial output"}`,
		`{not json`,
		`{"step_index":3,"type":"RUN_COMMAND","source":"MODEL","status":"DONE","created_at":"2026-05-20T09:00:04Z","content":"` + strings.ReplaceAll(toolOutput, "\n", `\n`) + `"}`,
		`{"step_index":4,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-05-20T09:00:05Z","tool_calls":[{"name":"notify_user","args":{"Message":"Done, please review.","BlockedOnUser":true}}]}`,
		`{"step_index":5,"type":"SYSTEM_MESSAGE","source":"SYSTEM","status":"DONE","created_at":"2026-05-20T09:00:06Z","content":"system note"}`,
		`{"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-05-20T09:10:00Z","content":"<USER_SETTINGS_CHANGE>mode changed</USER_SETTINGS_CHANGE>"}`,
		`{"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-05-20T09:20:00Z","content":"<USER_REQUEST>` + bigPrompt + `</USER_REQUEST>"}`,
		`{"step_index":6,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-05-20T09:25:00Z","content":"All good."}`,
	}
	writeFile(t, filepath.Join(ide, "brain", idC, ".system_generated", "logs", "transcript.jsonl"), strings.Join(lines, "\n")+"\n")
	// An empty transcript_full.jsonl (as the CLI leaves them) is passed over.
	writeFile(t, filepath.Join(ide, "brain", idC, ".system_generated", "logs", "transcript_full.jsonl"), "")

	// E: brain folder only, in the ide root.
	writeFile(t, filepath.Join(ide, "brain", idE, "walkthrough.md"), "Walkthrough text")
	writeFile(t, filepath.Join(ide, "brain", idE, "walkthrough.md.metadata.json"), `{"artifactType":"ARTIFACT_TYPE_WALKTHROUGH","updatedAt":"`+eWalkAt.Format(time.RFC3339Nano)+`"}`)
	// A brain folder without text and without .pb is not a conversation.
	if err := os.MkdirAll(filepath.Join(main, "brain", "12345678-0000-4000-8000-000000000000"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(main, "brain", "tempmediaStorage"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Indexes.
	sums := map[string]fixtureSummary{
		idA: {title: "Parser  work", uri: "file:///c%3A/work/alpha", created: aCreated, updated: aUpdated, lastIn: aLastIn},
		idB: {title: "Old title B", uri: "", created: bCreated, updated: bOldUpd},
	}
	writeFile(t, filepath.Join(main, agyhubName), string(encodeAgyhub(sums, idA, idB)))
	state := map[string]fixtureSummary{
		idB: {title: "Backup chat", fallbackURI: "file:///d:/proj/beta/", created: bCreated, updated: bNewUpd},
		idC: {title: "IDE chat", uri: "file://server/share/gamma", created: cCreated, updated: cUpdated},
	}
	writeStateDB(t, filepath.Join(env.AppData, "Antigravity", "User", "globalStorage", "state.vscdb"),
		map[string]string{stateKey: encodeStateValue(state, idB, idC), "other.key": "{}"})
	writeStateDB(t, filepath.Join(env.AppData, "Antigravity IDE", "User", "globalStorage", "state.vscdb"),
		map[string]string{"other.key": "{}"})
	writeFile(t, filepath.Join(env.AppData, "Antigravity-Roaming.zip"), "zip")

	ageTree(t, root)
	return env, root
}

func TestSources(t *testing.T) {
	env, _ := buildTree(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	gem := filepath.Join(env.Home, ".gemini")
	want := map[string]string{
		idA: filepath.Join(gem, "antigravity", "conversations", idA+".pb"),
		idD: filepath.Join(gem, "antigravity", "conversations", idD+".pb"),
		idB: filepath.Join(gem, "antigravity-backup", "conversations", idB+".pb"),
		idC: filepath.Join(gem, "antigravity-ide", "conversations", idC+".pb"),
		idE: filepath.Join(gem, "antigravity-ide", "brain", idE),
	}
	got := map[string]adapters.Source{}
	for _, s := range srcs {
		if s.Adapter != "antigravity" {
			t.Errorf("Adapter = %q", s.Adapter)
		}
		got[s.Extra[extraID]] = s
	}
	if len(srcs) != len(want) || len(got) != len(want) {
		t.Fatalf("got %d sources (%v), want %d", len(srcs), keys(got), len(want))
	}
	for cid, p := range want {
		if got[cid].Path != p {
			t.Errorf("%s: Path = %s, want %s", cid, got[cid].Path, p)
		}
	}
	hub := filepath.Join(gem, "antigravity", agyhubName)
	db := filepath.Join(env.AppData, "Antigravity", "User", "globalStorage", "state.vscdb")
	dbIDE := filepath.Join(env.AppData, "Antigravity IDE", "User", "globalStorage", "state.vscdb")
	for cid, s := range got {
		for _, f := range []string{hub, db, dbIDE} {
			if !contains(s.Files, f) {
				t.Errorf("%s: Files lacks index file %s", cid, f)
			}
		}
	}
	ba := filepath.Join(gem, "antigravity", "brain", idA)
	for _, f := range []string{
		want[idA],
		filepath.Join(ba, "task.md"),
		filepath.Join(ba, "task.md.metadata.json"),
		filepath.Join(ba, "implementation_plan.md.resolved.10"),
		filepath.Join(ba, "notes_on-design.md"),
	} {
		if !contains(got[idA].Files, f) {
			t.Errorf("A: Files lacks %s", f)
		}
	}
	if contains(got[idA].Files, filepath.Join(ba, "task.md.resolved")) {
		t.Error("A: Files lists an unread .resolved version")
	}
	if b := got[idC].Extra[extraBrain]; b != filepath.Join(gem, "antigravity-ide", "brain", idC) {
		t.Errorf("C: brain = %s, want the ide folder that has the log", b)
	}
}

func TestSourcesMissing(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on an empty machine = %v, %v", srcs, err)
	}
}

func TestReadGolden(t *testing.T) {
	env, _ := buildTree(t)
	sessions, skipped := adapterstest.ReadAll(t, New(), env)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the malformed log line)", skipped)
	}
	if len(sessions) != 5 {
		t.Fatalf("got %d sessions, want 5", len(sessions))
	}
	cStep := func(sec int) time.Time { return time.Date(2026, 5, 20, 9, 0, sec, 0, time.UTC) }
	want := []model.Session{
		{
			Agent: "antigravity", NativeID: idA, Cwd: `C:\work\alpha`,
			Title: "Parser work", TitleSource: model.TitleAgent,
			StartedAt: aCreated, UpdatedAt: aUpdated,
			Coverage: model.CoveragePartial, Origin: model.OriginLive,
			Messages: []model.Message{
				{Role: model.RoleAssistant, Kind: model.KindText, At: aTaskAt, Text: "Task (task.md)\n\n# Task\n\n- [x] write the parser"},
				{Role: model.RoleAssistant, Kind: model.KindText, At: oldTime, Text: "Implementation plan (implementation_plan.md)\n\nplan v10"},
				{Role: model.RoleAssistant, Kind: model.KindText, At: oldTime, Text: "Notes on design (notes_on-design.md)\n\ndesign notes"},
			},
		},
		{
			Agent: "antigravity", NativeID: idB, Cwd: `D:\proj\beta`,
			Title: "Backup chat", TitleSource: model.TitleAgent,
			StartedAt: bCreated, UpdatedAt: bNewUpd,
			Coverage: model.CoverageTitles, Origin: model.OriginLive,
		},
		{
			Agent: "antigravity", NativeID: idC, Cwd: `\\server\share\gamma`,
			Title: "IDE chat", TitleSource: model.TitleAgent,
			StartedAt: cCreated, UpdatedAt: cUpdated,
			Coverage: model.CoverageFull, Origin: model.OriginLive,
			Messages: []model.Message{
				{Role: model.RoleUser, Kind: model.KindText, At: cStep(1), Text: "Fix   the parser"},
				{Role: model.RoleAssistant, Kind: model.KindText, At: cStep(2), Text: "Looking at it."},
				{Role: model.RoleAssistant, Kind: model.KindToolCall, At: cStep(2), Text: "run_command: go test ./..."},
				{Role: model.RoleAssistant, Kind: model.KindToolCall, At: cStep(2), Text: `view_file: C:\work\gamma\main.go`},
				{Role: model.RoleAssistant, Kind: model.KindToolCall, At: cStep(2), Text: `write_to_file: {"CodeContent":"package main","Overwrite":true}`},
				{Role: model.RoleTool, Kind: model.KindToolOutput, At: cStep(4), Text: toolOutput[:model.MaxToolOutput]},
				{Role: model.RoleAssistant, Kind: model.KindText, At: cStep(5), Text: "Done, please review."},
				{Role: model.RoleUser, Kind: model.KindText, At: time.Date(2026, 5, 20, 9, 20, 0, 0, time.UTC), Text: strings.TrimSpace(bigPrompt)},
				{Role: model.RoleAssistant, Kind: model.KindText, At: time.Date(2026, 5, 20, 9, 25, 0, 0, time.UTC), Text: "All good."},
			},
		},
		{
			Agent: "antigravity", NativeID: idD,
			TitleSource: model.TitleNone,
			StartedAt:   oldTime, UpdatedAt: oldTime,
			Coverage: model.CoverageMeta, Origin: model.OriginLive,
		},
		{
			Agent: "antigravity", NativeID: idE,
			TitleSource: model.TitleNone,
			StartedAt:   eWalkAt, UpdatedAt: eWalkAt,
			Coverage: model.CoveragePartial, Origin: model.OriginLive,
			Messages: []model.Message{
				{Role: model.RoleAssistant, Kind: model.KindText, At: eWalkAt, Text: "Walkthrough (walkthrough.md)\n\nWalkthrough text"},
			},
		},
	}
	for i := range want {
		g, w := sessions[i], want[i]
		gm, wm := g.Messages, w.Messages
		g.Messages, w.Messages = nil, nil
		if !reflect.DeepEqual(g, w) {
			t.Errorf("session %s:\n got %+v\nwant %+v", w.NativeID, g, w)
		}
		if len(gm) != len(wm) {
			t.Errorf("session %s: %d messages, want %d", w.NativeID, len(gm), len(wm))
			for _, m := range gm {
				t.Logf("  %s/%s %q", m.Role, m.Kind, firstRunes(m.Text, 60))
			}
			continue
		}
		for j := range wm {
			if !reflect.DeepEqual(gm[j], wm[j]) {
				t.Errorf("session %s message %d:\n got %s/%s %v %q\nwant %s/%s %v %q", w.NativeID, j,
					gm[j].Role, gm[j].Kind, gm[j].At, firstRunes(gm[j].Text, 80),
					wm[j].Role, wm[j].Kind, wm[j].At, firstRunes(wm[j].Text, 80))
			}
		}
	}
}

func TestPromptTitleWhenIndexHasNoTitle(t *testing.T) {
	env, _ := buildTree(t)
	// Rewrite the state index without C's title.
	db := filepath.Join(env.AppData, "Antigravity", "User", "globalStorage", "state.vscdb")
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	writeStateDB(t, db, map[string]string{stateKey: encodeStateValue(map[string]fixtureSummary{
		idC: {created: cCreated, updated: cUpdated},
	}, idC)})
	if err := os.Chtimes(db, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	s := readOne(t, env, idC)
	if s.Title != "Fix the parser" || s.TitleSource != model.TitlePrompt {
		t.Errorf("title = %q (%s), want the first prompt", s.Title, s.TitleSource)
	}
	if s.Cwd != "" {
		t.Errorf("Cwd = %q, want empty", s.Cwd)
	}
}

func TestRetryLater(t *testing.T) {
	env, _ := buildTree(t)
	gem := filepath.Join(env.Home, ".gemini")
	a := adapter{now: func() time.Time { return oldTime.Add(2 * time.Second) }}
	src := sourceFor(t, env, idA)
	if _, err := a.Read(context.Background(), env, src); !errors.Is(err, adapters.ErrRetryLater) {
		t.Fatalf("fresh artifacts: err = %v, want ErrRetryLater", err)
	}
	// Artifacts old enough, index fresh.
	a.now = func() time.Time { return oldTime.Add(time.Minute) }
	hub := filepath.Join(gem, "antigravity", agyhubName)
	fresh := oldTime.Add(58 * time.Second)
	if err := os.Chtimes(hub, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(context.Background(), env, sourceFor(t, env, idD)); !errors.Is(err, adapters.ErrRetryLater) {
		t.Fatalf("fresh index: err = %v, want ErrRetryLater", err)
	}
	a.now = func() time.Time { return oldTime.Add(time.Hour) }
	if _, err := a.Read(context.Background(), env, sourceFor(t, env, idD)); err != nil {
		t.Fatalf("settled: %v", err)
	}
}

func TestBrokenIndexIsAWarning(t *testing.T) {
	env, _ := buildTree(t)
	db := filepath.Join(env.AppData, "Antigravity", "User", "globalStorage", "state.vscdb")
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	writeStateDB(t, db, map[string]string{stateKey: "%%% not base64 %%%"})
	hub := filepath.Join(env.Home, ".gemini", "antigravity", agyhubName)
	writeFile(t, hub, "\xff\xff\xff\xff")
	for _, p := range []string{db, hub} {
		if err := os.Chtimes(p, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	res, err := New().Read(context.Background(), env, sourceFor(t, env, idC))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 2 {
		t.Errorf("warnings = %q, want 2", res.Warnings)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].Coverage != model.CoverageFull || res.Sessions[0].Title != "Fix the parser" {
		t.Errorf("session = %+v", res.Sessions)
	}
}

func TestWorkspacePath(t *testing.T) {
	for in, want := range map[string]string{
		"file:///c%3A/Users/x/proj":  `C:\Users\x\proj`,
		"file:///d:/work/a%20b/":     `D:\work\a b`,
		"file:///C:/":                `C:\`,
		"file://server/share/dir":    `\\server\share\dir`,
		"file://localhost/e:/x":      `E:\x`,
		"file:///home/user/p":        "/home/user/p",
		"vscode-remote://wsl+x/home": "vscode-remote://wsl+x/home",
		"file:///c:/bad%zzescape":    `C:\bad%zzescape`,
		"  file:///c%3a/trailing%  ": `C:\trailing%`,
	} {
		if got := workspacePath(in); got != want {
			t.Errorf("workspacePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUserText(t *testing.T) {
	for in, want := range map[string]string{
		"plain prompt": "plain prompt",
		"<USER_REQUEST>\nhello\n</USER_REQUEST>\n<ADDITIONAL_METADATA>x</ADDITIONAL_METADATA>": "hello",
		"<USER_REQUEST>one</USER_REQUEST> <USER_REQUEST>two</USER_REQUEST>":                    "one\n\ntwo",
		"<USER_REQUEST>unclosed\n<ADDITIONAL_METADATA>state":                                   "unclosed",
		"before <ADDITIONAL_METADATA>state</ADDITIONAL_METADATA> after":                        "before  after",
		"<USER_SETTINGS_CHANGE>only settings</USER_SETTINGS_CHANGE>":                           "",
		"text <ADDITIONAL_METADATA>unclosed":                                                   "text",
	} {
		if got := userText(in); got != want {
			t.Errorf("userText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsConversationID(t *testing.T) {
	for in, want := range map[string]bool{
		idA:                                    true,
		"AAAAAAAA-0000-4000-8000-00000000000A": true,
		"tempmediaStorage":                     false,
		"aaaaaaaa-0000-4000-8000-00000000000g": false,
		"aaaaaaaa00000-4000-8000-00000000000a": false,
	} {
		if got := isConversationID(in); got != want {
			t.Errorf("isConversationID(%q) = %v", in, got)
		}
	}
}

func TestCaps(t *testing.T) {
	c := New().Caps()
	if c.Resume != nil || c.App == nil || c.App.Program != "antigravity" || !reflect.DeepEqual(c.App.Args, []string{"{cwd}"}) {
		t.Errorf("Caps = %+v", c)
	}
	if !contains(c.App.Candidates, `%LOCALAPPDATA%\Programs\Antigravity\bin\antigravity.cmd`) {
		t.Errorf("Candidates = %v", c.App.Candidates)
	}
	if a := New(); a.ID() != "antigravity" || a.Name() == "" || a.Version() < 1 {
		t.Errorf("identity = %s %s %d", a.ID(), a.Name(), a.Version())
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }

// --- helpers ---

func sourceFor(t *testing.T, env adapters.Env, cid string) adapters.Source {
	t.Helper()
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		if s.Extra[extraID] == cid {
			return s
		}
	}
	t.Fatalf("no source for %s", cid)
	return adapters.Source{}
}

func readOne(t *testing.T, env adapters.Env, cid string) model.Session {
	t.Helper()
	res, err := New().Read(context.Background(), env, sourceFor(t, env, cid))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("%d sessions", len(res.Sessions))
	}
	return res.Sessions[0]
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func keys(m map[string]adapters.Source) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
