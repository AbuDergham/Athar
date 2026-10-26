package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

func TestApplyInsertStoresEverything(t *testing.T) {
	d := openTemp(t)
	repo := filepath.Join(t.TempDir(), "TelegramVideos")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := live("s1", "Download videos", "download every video", "on it")
	s.Cwd = filepath.Join(repo, "cmd")
	s.Root = `C:\Users\x\.claude-local`
	s.Messages = append(s.Messages,
		model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, Text: "Bash: go test ./..."},
		model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, Text: "ok"}, // At unknown
	)
	rep := mustApply(t, d, src("a.jsonl"), s)
	if rep.Inserted != 1 || rep.Updated != 0 || rep.Shrunk != 0 {
		t.Errorf("report = %+v", rep)
	}
	r := session(t, d, "s1")
	want := Row{
		SessionID: r.SessionID, Agent: "claude", NativeID: "s1", Title: "Download videos",
		ProjectName: "TelegramVideos", ProjectPath: repo, Cwd: s.Cwd, Root: s.Root, Machine: "M1",
		Coverage: model.CoverageFull, Origin: model.OriginLive,
		StartedAt: s.StartedAt, UpdatedAt: s.UpdatedAt, MessageCount: 4, HitSeq: -1,
	}
	if r != want {
		t.Errorf("row =\n%+v\nwant\n%+v", r, want)
	}
	got, err := d.Messages(r.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("%d messages", len(got))
	}
	for i := range got {
		if got[i] != s.Messages[i] {
			t.Errorf("message %d = %+v, want %+v", i, got[i], s.Messages[i])
		}
	}
}

func TestApplyPrefixAppends(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s1", "t", "one", "two"))
	rep := mustApply(t, d, src("a"), live("s1", "t", "one", "two", "three", "four"))
	if rep.Updated != 1 || rep.Shrunk != 0 {
		t.Errorf("report = %+v", rep)
	}
	r := session(t, d, "s1")
	if got := strings.Join(texts(t, d, r.SessionID), ","); got != "one,two,three,four" {
		t.Errorf("messages = %s", got)
	}
	if r.MessageCount != 4 {
		t.Errorf("MessageCount = %d", r.MessageCount)
	}
	// Re-applying the same content changes nothing.
	rep = mustApply(t, d, src("a"), live("s1", "t", "one", "two", "three", "four"))
	if rep != (ApplyReport{}) {
		t.Errorf("identical re-apply report = %+v", rep)
	}
	if got := len(texts(t, d, r.SessionID)); got != 4 {
		t.Errorf("%d messages after identical re-apply", got)
	}
}

func TestApplyShrinkKeepsArchivedCopy(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s1", "t", "m1", "m2", "m3", "m4", "m5"))
	rep := mustApply(t, d, src("a"), live("s1", "t", "x1", "x2", "x3"))
	if rep.Shrunk != 1 || rep.Updated != 1 {
		t.Errorf("report = %+v", rep)
	}
	r := session(t, d, "s1")
	if got := strings.Join(texts(t, d, r.SessionID), ","); got != "m1,m2,m3,m4,m5,x1,x2,x3" {
		t.Errorf("messages = %s", got)
	}
	if r.MessageCount != 8 {
		t.Errorf("MessageCount = %d", r.MessageCount)
	}
	st := sourceStatus(t, d, "M1", "a")
	if !strings.Contains(strings.Join(st.Warnings, "|"), "source shrank, archived copy kept") {
		t.Errorf("warnings = %v", st.Warnings)
	}

	// The source keeps growing after the shrink: only unseen messages are appended.
	mustApply(t, d, src("a"), live("s1", "t", "x1", "x2", "x3", "x4"))
	if got := strings.Join(texts(t, d, r.SessionID), ","); got != "m1,m2,m3,m4,m5,x1,x2,x3,x4" {
		t.Errorf("messages after regrowth = %s", got)
	}

	// Truncated to a prefix: nothing lost, nothing added.
	mustApply(t, d, src("b"), live("s2", "t", "a", "b", "c"))
	rep = mustApply(t, d, src("b"), live("s2", "t", "a"))
	if rep.Shrunk != 1 {
		t.Errorf("truncation report = %+v", rep)
	}
	if got := strings.Join(texts(t, d, session(t, d, "s2").SessionID), ","); got != "a,b,c" {
		t.Errorf("messages after truncation = %s", got)
	}
}

func TestApplyRepeatedMessagesAreCounted(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s1", "t", "yes", "ok", "yes"))
	// Not a prefix (first message edited): two "yes" are stored, the third is new.
	mustApply(t, d, src("a"), live("s1", "t", "edited", "ok", "yes", "ok", "yes"))
	got := texts(t, d, session(t, d, "s1").SessionID)
	if strings.Join(got, ",") != "yes,ok,yes,edited,ok" {
		t.Errorf("messages = %v", got)
	}
}

func TestApplyRoleAndKindAreDistinct(t *testing.T) {
	d := openTemp(t)
	s := live("s1", "t", "same")
	mustApply(t, d, src("a"), s)
	s2 := live("s1", "t", "other")
	s2.Messages = append(s2.Messages, model.Message{Role: model.RoleAssistant, Kind: model.KindText, Text: "same"})
	mustApply(t, d, src("a"), s2)
	// "same" by the user is stored; "same" by the assistant is a different message.
	if got := strings.Join(texts(t, d, session(t, d, "s1").SessionID), ","); got != "same,other,same" {
		t.Errorf("messages = %s", got)
	}
}

func imported(s model.Session) model.Session {
	s.Origin = model.OriginAgentsView
	return s
}

func promptLog(id string, prompts ...string) model.Session {
	s := model.Session{Agent: "claude", NativeID: id, Cwd: `C:\work\` + id, Origin: model.OriginPromptLog,
		Coverage: model.CoveragePrompts, Title: prompts[0], TitleSource: model.TitlePrompt,
		StartedAt: t0, UpdatedAt: t0.Add(time.Duration(len(prompts)) * time.Minute)}
	for i, p := range prompts {
		s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: t0.Add(time.Duration(2*i) * time.Minute), Text: p})
	}
	return s
}

func TestLiveBeatsImportInAnyOrder(t *testing.T) {
	full := live("s1", "Agent title", "u1", "a1", "u2", "a2")
	imp := imported(live("s1", "Imported title", "u1", "differently formatted"))

	for _, order := range []string{"import first", "live first"} {
		t.Run(order, func(t *testing.T) {
			d := openTemp(t)
			if order == "import first" {
				mustApply(t, d, src("agentsview.db"), imp)
				mustApply(t, d, src("live.jsonl"), full)
			} else {
				mustApply(t, d, src("live.jsonl"), full)
				mustApply(t, d, src("agentsview.db"), imp)
			}
			rows := mustSearch(t, d, Filter{})
			if len(rows) != 1 {
				t.Fatalf("%d sessions, want one per (agent, native id)", len(rows))
			}
			r := rows[0]
			if r.Origin != model.OriginLive || r.Title != "Agent title" {
				t.Errorf("row = %+v", r)
			}
			if got := strings.Join(texts(t, d, r.SessionID), ","); got != "u1,a1,u2,a2" {
				t.Errorf("messages = %s", got)
			}
			// The replaced import text is no longer indexed.
			if rows := mustSearch(t, d, Filter{Query: "differently"}); len(rows) != 0 {
				t.Errorf("replaced text still found: %+v", rows)
			}
			if rows := mustSearch(t, d, Filter{Query: "u2"}); len(rows) != 0 {
				t.Errorf("2-char term matched a message: %v", ids(rows))
			}
			if rows := mustSearch(t, d, Filter{Query: "agent title"}); len(rows) != 1 || rows[0].HitSeq != -1 {
				t.Errorf("title search: %+v", rows)
			}
		})
	}
}

func TestLongSessionsKeepOrder(t *testing.T) {
	d := openTemp(t)
	var many []string
	for i := range 3000 {
		many = append(many, fmt.Sprintf("message number %04d", i))
	}
	mustApply(t, d, src("a"), live("big", "t", many[:1000]...), live("small", "t", "x", "y"))
	mustApply(t, d, src("a"), live("big", "t", many...))
	got := texts(t, d, session(t, d, "big").SessionID)
	if len(got) != 3000 || got[0] != many[0] || got[2999] != many[2999] || got[1500] != many[1500] {
		t.Fatalf("big session: %d messages", len(got))
	}
	rows := mustSearch(t, d, Filter{Query: `"number 2345"`})
	if len(rows) != 1 || rows[0].HitSeq != 2345 {
		t.Errorf("rows = %+v", rows)
	}
	if got := strings.Join(texts(t, d, session(t, d, "small").SessionID), ","); got != "x,y" {
		t.Errorf("small session = %s", got)
	}
}

func TestImportFillsSessionsWithoutLiveSource(t *testing.T) {
	d := openTemp(t)
	imp := imported(live("old", "Old chat", "q", "a"))
	imp.Machine = "OTHER-PC"
	mustApply(t, d, src("agentsview.db"), imp)
	r := session(t, d, "old")
	if r.Origin != model.OriginAgentsView || r.Machine != "OTHER-PC" || r.MessageCount != 2 {
		t.Errorf("row = %+v", r)
	}
	// An import with more messages than a prompt-log session replaces the prompts.
	mustApply(t, d, src("h.jsonl"), promptLog("p1", "q1", "q2"))
	mustApply(t, d, src("agentsview.db"), imported(live("p1", "Imported", "q1", "a1", "q2", "a2")))
	r = session(t, d, "p1")
	if got := strings.Join(texts(t, d, r.SessionID), ","); got != "q1,a1,q2,a2" || r.Origin != model.OriginAgentsView {
		t.Errorf("messages = %s, row %+v", got, r)
	}
}

func TestPromptLogMergedIntoFullSession(t *testing.T) {
	full := live("s1", "Agent title", "fix the bug", "done", "thanks", "welcome")
	pl := promptLog("s1", "fix the bug", "thanks", "/clear")
	pl.StartedAt = t0.Add(-time.Hour) // the prompt log saw an earlier prompt time

	for _, order := range []string{"prompt log first", "transcript first"} {
		t.Run(order, func(t *testing.T) {
			d := openTemp(t)
			if order == "prompt log first" {
				mustApply(t, d, src("history.jsonl"), pl)
				mustApply(t, d, src("s1.jsonl"), full)
			} else {
				mustApply(t, d, src("s1.jsonl"), full)
				mustApply(t, d, src("history.jsonl"), pl)
			}
			rows := mustSearch(t, d, Filter{})
			if len(rows) != 1 {
				t.Fatalf("%d sessions", len(rows))
			}
			r := rows[0]
			if r.Coverage != model.CoverageFull || r.Origin != model.OriginLive || r.Title != "Agent title" {
				t.Errorf("row = %+v", r)
			}
			if !r.StartedAt.Equal(t0.Add(-time.Hour)) || !r.UpdatedAt.Equal(full.UpdatedAt) {
				t.Errorf("times = %v .. %v", r.StartedAt, r.UpdatedAt)
			}
			if got := strings.Join(texts(t, d, r.SessionID), ","); got != "fix the bug,done,thanks,welcome" {
				t.Errorf("messages = %s", got)
			}
			// The prompt log keeps growing: the full session is not touched.
			pl2 := promptLog("s1", "fix the bug", "thanks", "/clear", "more")
			mustApply(t, d, src("history.jsonl"), pl2)
			if got := len(texts(t, d, r.SessionID)); got != 4 {
				t.Errorf("%d messages after prompt-log growth", got)
			}
		})
	}
}

func TestUpgradeWithFewerMessagesKeepsPrompts(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("history.jsonl"), promptLog("s1", "p1", "p2", "p3"))
	// The transcript holds less than the prompt log (e.g. only its start).
	rep := mustApply(t, d, src("s1.jsonl"), live("s1", "Agent title", "p1"))
	if rep.Shrunk != 0 {
		t.Errorf("an upgrade was reported as a shrink: %+v", rep)
	}
	r := session(t, d, "s1")
	if got := strings.Join(texts(t, d, r.SessionID), ","); got != "p1,p2,p3" || r.Origin != model.OriginLive {
		t.Errorf("messages = %s, row %+v", got, r)
	}
}

func TestPromptLogOnlySession(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("history.jsonl"), promptLog("p1", "first prompt", "second"))
	mustApply(t, d, src("history.jsonl"), promptLog("p1", "first prompt", "second", "third"))
	r := session(t, d, "p1")
	if r.Coverage != model.CoveragePrompts || r.Origin != model.OriginPromptLog || r.Title != "first prompt" || r.MessageCount != 3 {
		t.Errorf("row = %+v", r)
	}
}

func TestCwdNeverOverwritten(t *testing.T) {
	d := openTemp(t)
	base := t.TempDir()
	first, second, later := filepath.Join(base, "first"), filepath.Join(base, "second"), filepath.Join(base, "later", "proj")
	a := live("s1", "t", "m")
	a.Cwd = first
	mustApply(t, d, src("a"), a)
	b := live("s1", "t", "m", "n")
	b.Cwd = second
	mustApply(t, d, src("a"), b)
	if r := session(t, d, "s1"); r.Cwd != first || r.ProjectName != "first" {
		t.Errorf("cwd = %q project %q", r.Cwd, r.ProjectName)
	}

	// An empty Cwd is filled later, and the project is computed then.
	e := live("s2", "t", "m")
	e.Cwd = ""
	mustApply(t, d, src("b"), e)
	if r := session(t, d, "s2"); r.Cwd != "" || r.ProjectName != NoProject || r.ProjectPath != "" {
		t.Errorf("empty cwd row = %+v", r)
	}
	e.Cwd = later
	mustApply(t, d, src("b"), e)
	if r := session(t, d, "s2"); r.Cwd != later || r.ProjectName != "proj" {
		t.Errorf("filled cwd row = %+v", r)
	}
	// ...and never emptied again.
	e.Cwd = ""
	mustApply(t, d, src("b"), e)
	if r := session(t, d, "s2"); r.Cwd != later {
		t.Errorf("cwd emptied: %+v", r)
	}
}

func TestTitlePriority(t *testing.T) {
	d := openTemp(t)
	apply := func(title string, ts model.TitleSource) string {
		s := live("s1", title, "m")
		s.TitleSource = ts
		mustApply(t, d, src("a"), s)
		return session(t, d, "s1").Title
	}
	steps := []struct {
		title string
		ts    model.TitleSource
		want  string
	}{
		{"first prompt", model.TitlePrompt, "first prompt"},
		{"", model.TitleNone, "first prompt"},
		{"AI title", model.TitleAgent, "AI title"},
		{"a later prompt", model.TitlePrompt, "AI title"},
		{"newer AI title", model.TitleAgent, "newer AI title"},
		{"My name", model.TitleUser, "My name"},
		{"even newer AI title", model.TitleAgent, "My name"},
		{"Renamed", model.TitleUser, "Renamed"},
	}
	for i, s := range steps {
		if got := apply(s.title, s.ts); got != s.want {
			t.Errorf("step %d (%q %s): title = %q, want %q", i, s.title, s.ts, got, s.want)
		}
	}
	// The title is searchable, the old one no longer is.
	if rows := mustSearch(t, d, Filter{Query: "renamed"}); len(rows) != 1 {
		t.Errorf("new title not found: %v", ids(rows))
	}
	if rows := mustSearch(t, d, Filter{Query: "newer"}); len(rows) != 0 {
		t.Errorf("old title still found: %v", ids(rows))
	}
}

func TestTimesAndCoverageMerge(t *testing.T) {
	d := openTemp(t)
	a := live("s1", "t", "m")
	a.Coverage = model.CoveragePartial
	a.StartedAt, a.UpdatedAt = t0, t0.Add(time.Hour)
	mustApply(t, d, src("a"), a)
	b := live("s1", "t", "m")
	b.Coverage = model.CoverageTitles
	b.StartedAt, b.UpdatedAt = t0.Add(-time.Hour), t0.Add(30*time.Minute)
	mustApply(t, d, src("a"), b)
	r := session(t, d, "s1")
	if r.Coverage != model.CoveragePartial || !r.StartedAt.Equal(t0.Add(-time.Hour)) || !r.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("row = %+v", r)
	}
}

func TestApplyFillsMissingTimesAndDefaults(t *testing.T) {
	d := openTemp(t)
	s := model.Session{NativeID: "bare", Messages: msgs("q", "a", "q2")} // no agent, times, coverage, origin
	mustApply(t, d, src("a"), s, model.Session{Agent: "claude"})         // the second has no id
	r := session(t, d, "bare")
	if r.Agent != "claude" || r.Origin != model.OriginLive || r.Coverage != model.CoverageMeta {
		t.Errorf("defaults: %+v", r)
	}
	if !r.StartedAt.Equal(t0) || !r.UpdatedAt.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("times from messages: %v .. %v", r.StartedAt, r.UpdatedAt)
	}
	if st := sourceStatus(t, d, "M1", "a"); !strings.Contains(strings.Join(st.Warnings, "|"), "without an id") {
		t.Errorf("warnings = %v", st.Warnings)
	}
}

func TestSessionMovesBetweenSources(t *testing.T) {
	d := openTemp(t)
	s := live("s1", "t", "a", "b")
	mustApply(t, d, src(`sessions\rollout.jsonl`), s)
	// `codex archive` moved the file.
	mustApply(t, d, src(`archived_sessions\rollout.jsonl`), s)
	n, err := d.FinishSync("M1", map[string]bool{`archived_sessions\rollout.jsonl`: true}, time.Now())
	if err != nil || n != 0 {
		t.Fatalf("FinishSync = %d, %v", n, err)
	}
	r := session(t, d, "s1")
	if r.Missing || r.MessageCount != 2 {
		t.Errorf("moved session: %+v", r)
	}
	if st := sourceStatus(t, d, "M1", `archived_sessions\rollout.jsonl`); st.Sessions != 1 {
		t.Errorf("new source status %+v", st)
	}
	if list, _ := d.Sources("M1"); len(list) != 1 {
		t.Errorf("the vanished source is still listed: %+v", list)
	}
}

func TestOlderCopyDoesNotOverrideNewer(t *testing.T) {
	d := openTemp(t)
	newer := live("s1", "Newer title", "a", "b", "c")
	newer.Root = `C:\Users\x\.claude-local`
	older := live("s1", "Older title", "a")
	older.UpdatedAt = newer.UpdatedAt.Add(-time.Hour)
	mustApply(t, d, src("copy-new"), newer)
	rep := mustApply(t, d, src("copy-old"), older)
	if rep.Shrunk != 0 {
		t.Errorf("older copy counted as a shrink: %+v", rep)
	}
	r := session(t, d, "s1")
	if r.Title != "Newer title" || r.MessageCount != 3 || r.Root != newer.Root {
		t.Errorf("row = %+v", r)
	}
}

func TestFinishSyncMarksMissing(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s1", "t", "m"))
	mustApply(t, d, src("b"), live("s2", "t", "m"))
	other := live("s3", "t", "m")
	other.Machine = "OTHER-PC"
	mustApply(t, d, src("imports.db"), imported(other))

	n, err := d.FinishSync("M1", map[string]bool{"a": true, "b": true, "imports.db": true}, t0)
	if err != nil || n != 0 {
		t.Fatalf("all seen: n=%d err=%v", n, err)
	}
	// b disappeared, the import index too.
	n, err = d.FinishSync("M1", map[string]bool{"a": true}, t0)
	if err != nil || n != 1 {
		t.Fatalf("b unseen: n=%d err=%v, want 1", n, err)
	}
	rows := mustSearch(t, d, Filter{})
	for _, r := range rows {
		want := r.NativeID == "s2"
		if r.Missing != want {
			t.Errorf("%s Missing = %v, want %v", r.NativeID, r.Missing, want)
		}
	}
	// Marking is not repeated.
	if n, _ := d.FinishSync("M1", map[string]bool{"a": true}, t0); n != 0 {
		t.Errorf("second FinishSync marked %d again", n)
	}
	// The missing session is still listed and readable.
	r := session(t, d, "s2")
	if len(texts(t, d, r.SessionID)) != 1 {
		t.Error("missing session lost its messages")
	}
	// It comes back when its source reappears.
	mustApply(t, d, src("b"), live("s2", "t", "m"))
	if session(t, d, "s2").Missing {
		t.Error("reappeared session still missing")
	}
}

func TestFinishSyncSessionDroppedFromMultiSessionSource(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("state.db"), live("s1", "t", "m"), live("s2", "t", "m"))
	mustApply(t, d, src("state.db"), live("s1", "t", "m")) // the agent deleted s2
	n, err := d.FinishSync("M1", map[string]bool{"state.db": true}, t0)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if !session(t, d, "s2").Missing || session(t, d, "s1").Missing {
		t.Error("wrong session marked")
	}
	if len(texts(t, d, session(t, d, "s2").SessionID)) != 1 {
		t.Error("dropped session lost its messages")
	}
}

func TestFinishSyncPromptLogDoesNotKeepTranscriptAlive(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("s1.jsonl"), live("s1", "t", "q", "a"))
	mustApply(t, d, src("history.jsonl"), promptLog("s1", "q"), promptLog("p2", "only a prompt"))
	// The transcript was deleted by the agent; its prompts are still in the log.
	n, err := d.FinishSync("M1", map[string]bool{"history.jsonl": true}, t0)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if !session(t, d, "s1").Missing {
		t.Error("session whose transcript is gone is not marked missing")
	}
	if session(t, d, "p2").Missing {
		t.Error("prompt-log session with its log present marked missing")
	}
}

func TestFailedSourceKeepsSessionsPresent(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s1", "t", "m"))
	if err := d.RecordSourceError("M1", "claude", "a", os.ErrPermission); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.FinishSync("M1", map[string]bool{"a": true}, t0); n != 0 {
		t.Errorf("a seen but failed source marked %d sessions missing", n)
	}
}

func TestSourcePaths(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s1", "t", "m"))
	mustApply(t, d, src("b"), live("s2", "t", "m"))
	cx := src("c")
	cx.Adapter = "codex"
	mustApply(t, d, cx, live("s3", "t", "m"))
	got, err := d.SourcePaths("M1", "claude")
	if err != nil || strings.Join(got, ",") != "a,b" {
		t.Errorf("SourcePaths = %v, %v", got, err)
	}
}
