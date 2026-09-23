package qwen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/model"
)

const (
	id1 = "11111111-1111-4111-8111-111111111111"
	id2 = "22222222-2222-4222-8222-222222222222"
	id3 = "33333333-3333-4333-8333-333333333333"
	id5 = "55555555-5555-4555-8555-555555555555"
	id7 = "77777777-7777-4777-8777-777777777777"
	id8 = "88888888-8888-4888-8888-888888888888"
	id9 = "99999999-9999-4999-8999-999999999999"
	idA = "aaaaaaaa-0000-4000-8000-000000000001"

	unknownHash = "0f0e0d0c0b0a09080706050403020100f0e0d0c0b0a090807060504030201000"
)

// longOutput is a tool output over model.MaxToolOutput bytes made of 2-byte
// runes, so truncation must land on a rune boundary.
var longOutput = strings.Repeat("سطر ", 600)

func hexHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// setup copies testdata/tree into a temp dir. Folder names that are project
// hashes cannot be committed (they depend on the temp path), so placeholder
// folder names are renamed to the sha256 of the folder they stand for.
func setup(t *testing.T) (adapters.Env, string) {
	t.Helper()
	setNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	root := t.TempDir()
	home := filepath.Join(root, "home")
	names := map[string]string{
		"HASH-legacy":     hexHash(filepath.Join(home, "work", "legacy")),
		"HASHLOWER-mid":   hexHash(strings.ToLower(filepath.Join(home, "work", "mid"))),
		"HASHLOWER-alpha": hexHash(strings.ToLower(`C:\work\alpha`)),
	}
	contents := map[string]string{"LONG_OUTPUT_PLACEHOLDER": longOutput}
	src := filepath.Join("testdata", "tree")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		parts := strings.Split(rel, string(filepath.Separator))
		for i, part := range parts {
			if n, ok := names[part]; ok {
				parts[i] = n
			}
		}
		dst := filepath.Join(append([]string{root}, parts...)...)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := string(b)
		for k, v := range contents {
			s = strings.ReplaceAll(s, k, v)
		}
		return os.WriteFile(dst, []byte(s), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapterstest.FixtureEnv(t, root, nil), root
}

// setNow pins the clock used by the "modified in the last 5 s" rule.
func setNow(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

func find(t *testing.T, ss []model.Session, id string, origin model.Origin) model.Session {
	t.Helper()
	for _, s := range ss {
		if s.NativeID == id && s.Origin == origin {
			return s
		}
	}
	t.Fatalf("session %s (%s) not found", id, origin)
	return model.Session{}
}

func msg(role model.Role, kind model.Kind, at, text string) model.Message {
	return model.Message{Role: role, Kind: kind, At: ts(at), Text: text}
}

func checkMessages(t *testing.T, id string, got, want []model.Message) {
	t.Helper()
	if len(got) != len(want) {
		for i, m := range got {
			t.Logf("got[%d] = %s/%s %s %q", i, m.Role, m.Kind, m.At.Format(time.RFC3339), m.Text)
		}
		t.Fatalf("%s: %d messages, want %d", id, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s message %d:\n got %s/%s %s %q\nwant %s/%s %s %q", id, i,
				got[i].Role, got[i].Kind, got[i].At.Format(time.RFC3339), got[i].Text,
				want[i].Role, want[i].Kind, want[i].At.Format(time.RFC3339), want[i].Text)
		}
	}
}

type header struct {
	Cwd, Title  string
	TitleSource model.TitleSource
	Started     time.Time
	Updated     time.Time
	Coverage    model.Coverage
	Root        string
}

func checkHeader(t *testing.T, s model.Session, want header) {
	t.Helper()
	got := header{s.Cwd, s.Title, s.TitleSource, s.StartedAt, s.UpdatedAt, s.Coverage, s.Root}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s (%s) header:\n got %+v\nwant %+v", s.NativeID, s.Origin, got, want)
	}
	if s.Agent != "qwen" {
		t.Errorf("%s agent = %q", s.NativeID, s.Agent)
	}
	if s.StartedAt.Location() != time.UTC || s.UpdatedAt.Location() != time.UTC {
		t.Errorf("%s times are not UTC", s.NativeID)
	}
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.ID() != "qwen" || a.Name() != "Qwen Code" || a.Version() < 1 {
		t.Fatalf("identity = %q %q %d", a.ID(), a.Name(), a.Version())
	}
}

func TestSources(t *testing.T) {
	env, _ := setup(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(env.Home, ".qwen")
	home := env.Home
	want := map[string]string{
		filepath.Join(base, "projects", "c--work-alpha", "chats", id1+".jsonl"):                                 kindChat,
		filepath.Join(base, "projects", "c--work-alpha", "chats", id3+".jsonl"):                                 kindChat,
		filepath.Join(base, "projects", "c--work-alpha", "chats", "44444444-4444-4444-8444-444444444444.jsonl"): kindChat,
		filepath.Join(base, "projects", "c--work-alpha", "chats", "archive", id5+".jsonl"):                      kindChat,
		filepath.Join(base, "projects", "d--code-beta", "chats", id2+".jsonl"):                                  kindChat,
		filepath.Join(base, "tmp", unknownHash, "logs.json"):                                                    kindLogs,
		filepath.Join(base, "tmp", hexHash(filepath.Join(home, "work", "legacy")), "logs.json"):                 kindLogs,
		filepath.Join(base, "tmp", hexHash(strings.ToLower(`C:\work\alpha`)), "logs.json"):                      kindLogs,
		filepath.Join(base, "tmp", hexHash(strings.ToLower(filepath.Join(home, "work", "mid"))), "chats",
			"session-2025-09-01T10-00-aaaaaaaa.json"): kindSession,
		filepath.Join(base, "tmp", hexHash(strings.ToLower(filepath.Join(home, "work", "mid"))), "chats",
			"session-2025-09-02T11-00-bbbbbbbb.json"): kindSession,
	}
	got := map[string]string{}
	for _, s := range srcs {
		got[s.Path] = s.Extra["kind"]
		if s.Adapter != "qwen" || s.Root != "" || !reflect.DeepEqual(s.Files, []string{s.Path}) {
			t.Errorf("source %s: adapter=%q root=%q files=%v", s.Path, s.Adapter, s.Root, s.Files)
		}
	}
	if !reflect.DeepEqual(got, want) {
		for p, k := range got {
			if want[p] != k {
				t.Errorf("unexpected source %s (%s)", p, k)
			}
		}
		for p, k := range want {
			if got[p] != k {
				t.Errorf("missing source %s (%s)", p, k)
			}
		}
	}
	if !sort.SliceIsSorted(srcs, func(i, j int) bool { return srcs[i].Path < srcs[j].Path }) {
		t.Error("sources are not sorted by path")
	}
}

func TestSourcesMissingRoot(t *testing.T) {
	env := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 0 {
		t.Fatalf("Sources on an empty home = %v, %v; want none, nil", srcs, err)
	}
}

func TestReadFixtureTree(t *testing.T) {
	env, root := setup(t)
	home := filepath.Join(root, "home")
	ss, skipped := adapterstest.ReadAll(t, New(), env)

	if skipped != 3 {
		t.Errorf("skipped = %d, want 3 (one broken JSONL line, two invalid logs.json entries)", skipped)
	}
	if len(ss) != 9 {
		for _, s := range ss {
			t.Logf("session %s %s", s.NativeID, s.Origin)
		}
		t.Fatalf("%d sessions, want 9", len(ss))
	}
	for _, s := range ss {
		if s.NativeID == "44444444-4444-4444-8444-444444444444" || strings.HasPrefix(s.NativeID, "bbbbbbbb") {
			t.Errorf("session %s has no messages and must be skipped", s.NativeID)
		}
		if s.NativeID == "66666666-6666-4666-8666-666666666666" || s.NativeID == "notes" {
			t.Errorf("session %s comes from an ignored file", s.NativeID)
		}
	}

	U, A, T := model.RoleUser, model.RoleAssistant, model.RoleTool
	X, C, O := model.KindText, model.KindToolCall, model.KindToolOutput

	s := find(t, ss, id1, model.OriginLive)
	checkHeader(t, s, header{`C:\work\alpha`, "Alpha release plan", model.TitleUser,
		ts("2026-09-01T10:00:00Z"), ts("2026-09-01T10:02:05Z"), model.CoverageFull, ""})
	checkMessages(t, id1, s.Messages, []model.Message{
		msg(U, X, "2026-09-01T10:00:05Z", "Plan the alpha release @notes.md"),
		msg(A, X, "2026-09-01T10:00:10Z", "I will list the files."),
		msg(A, C, "2026-09-01T10:00:10Z", "run_shell_command: dir /b"),
		msg(T, O, "2026-09-01T10:00:12Z", "README.md\nmain.go"),
		msg(U, X, "2026-09-01T10:01:00Z", "Ship it to staging"),
		msg(A, X, "2026-09-01T10:02:00Z", "Deployed to staging."),
	})

	s = find(t, ss, id2, model.OriginLive)
	checkHeader(t, s, header{`D:\code\beta`, "Login bug fix", model.TitleAgent,
		ts("2026-09-02T08:00:00Z"), ts("2026-09-02T08:03:00Z"), model.CoverageFull, ""})
	if len(s.Messages) != 9 {
		t.Fatalf("%s: %d messages, want 9", id2, len(s.Messages))
	}
	long := s.Messages[6]
	if long.Kind != O || len(long.Text) > model.MaxToolOutput || len(long.Text) < model.MaxToolOutput-4 ||
		!utf8.ValidString(long.Text) || !strings.HasPrefix(longOutput, long.Text) {
		t.Errorf("long tool output not truncated to %d bytes on a rune boundary: kind=%s len=%d", model.MaxToolOutput, long.Kind, len(long.Text))
	}
	s.Messages[6].Text = "(long)"
	checkMessages(t, id2, s.Messages, []model.Message{
		msg(U, X, "2026-09-02T08:00:00Z", "Fix the login bug"),
		msg(A, X, "2026-09-02T08:00:30Z", "Found it."),
		msg(U, X, "2026-09-02T08:00:40Z", "also check logout"),
		msg(U, X, "2026-09-02T08:01:00Z", "spoken hello"),
		msg(A, C, "2026-09-02T08:02:00Z", `read_file: D:\code\beta\login.go`),
		msg(A, C, "2026-09-02T08:02:00Z", `todo_write: {"todos":[{"id":"1","content":"check logout"}]}`),
		msg(T, O, "2026-09-02T08:02:01Z", "(long)"),
		msg(T, O, "2026-09-02T08:02:01Z", "todo list rejected"),
		msg(T, O, "2026-09-02T08:03:00Z", "display only output"),
	})

	s = find(t, ss, id3, model.OriginLive)
	checkHeader(t, s, header{`C:\work\alpha`, "Explain the build script please", model.TitlePrompt,
		ts("2026-09-03T12:00:00Z"), ts("2026-09-03T12:00:40Z"), model.CoverageFull, ""})
	checkMessages(t, id3, s.Messages, []model.Message{
		msg(U, X, "2026-09-03T12:00:00Z", "   Explain\n\n the   build\tscript   please "),
		msg(U, X, "2026-09-03T12:00:20Z", "[image]"),
		msg(A, X, "2026-09-03T12:00:30Z", "It compiles the app."),
	})

	s = find(t, ss, id5, model.OriginLive)
	checkHeader(t, s, header{`C:\work\alpha`, "Archived question about مشروع التطبيق", model.TitlePrompt,
		ts("2026-08-20T07:00:00Z"), ts("2026-08-20T07:00:09Z"), model.CoverageFull, ""})
	checkMessages(t, id5, s.Messages, []model.Message{
		msg(U, X, "2026-08-20T07:00:00Z", "Archived question about مشروع التطبيق"),
		msg(A, X, "2026-08-20T07:00:09Z", "Archived answer."),
	})

	// Prompt logs. The alpha folder hash is resolved from the cwd recorded in
	// projects/ (newer versions hash the lowercased path).
	s = find(t, ss, id1, model.OriginPromptLog)
	checkHeader(t, s, header{`C:\work\alpha`, "Plan the alpha release @notes.md", model.TitlePrompt,
		ts("2026-09-01T10:00:04Z"), ts("2026-09-01T10:00:59Z"), model.CoveragePrompts, ""})
	checkMessages(t, id1+" prompts", s.Messages, []model.Message{
		msg(U, X, "2026-09-01T10:00:04Z", "Plan the alpha release @notes.md"),
		msg(U, X, "2026-09-01T10:00:59Z", "Ship it to staging"),
	})

	s = find(t, ss, id7, model.OriginPromptLog)
	checkHeader(t, s, header{`C:\work\alpha`, "Summarize the changelog", model.TitlePrompt,
		ts("2026-09-03T09:00:00Z"), ts("2026-09-03T09:00:10Z"), model.CoveragePrompts, ""})
	checkMessages(t, id7, s.Messages, []model.Message{
		msg(U, X, "2026-09-03T09:00:00Z", "/auth"),
		msg(U, X, "2026-09-03T09:00:10Z", "Summarize the   changelog"),
	})

	// 0.0.8 hashed the exact path; the folder is found under the home folder.
	s = find(t, ss, id8, model.OriginPromptLog)
	checkHeader(t, s, header{filepath.Join(home, "work", "legacy"), "كيف أبدأ مشروعاً جديداً؟", model.TitlePrompt,
		ts("2025-08-25T18:00:00Z"), ts("2025-08-25T18:05:00Z"), model.CoveragePrompts, ""})
	checkMessages(t, id8, s.Messages, []model.Message{
		msg(U, X, "2025-08-25T18:00:00Z", "كيف أبدأ مشروعاً جديداً؟"),
		msg(U, X, "2025-08-25T18:05:00Z", "!git status"),
	})

	s = find(t, ss, id9, model.OriginPromptLog)
	checkHeader(t, s, header{"", "prompt from a folder that no longer exists", model.TitlePrompt,
		ts("2025-08-26T08:00:00Z"), ts("2025-08-26T08:00:00Z"), model.CoveragePrompts, ""})

	// 0.0.14-0.3 whole-file session JSON under tmp/<hash>/chats/.
	s = find(t, ss, idA, model.OriginLive)
	checkHeader(t, s, header{filepath.Join(home, "work", "mid"), "Write a haiku about rain", model.TitlePrompt,
		ts("2025-09-01T10:00:00Z"), ts("2025-09-01T10:05:00Z"), model.CoverageFull, ""})
	checkMessages(t, idA, s.Messages, []model.Message{
		msg(U, X, "2025-09-01T10:00:01Z", "Write a haiku about rain"),
		msg(A, X, "2025-09-01T10:00:05Z", "Soft rain on the roof"),
		msg(A, C, "2025-09-01T10:00:06Z", `write_file: C:\work\mid\haiku.txt`),
		msg(T, O, "2025-09-01T10:00:06Z", "Wrote 1 file"),
		msg(A, C, "2025-09-01T10:00:07Z", "run_shell_command: type haiku.txt"),
		msg(T, O, "2025-09-01T10:00:07Z", "access denied"),
		msg(U, X, "2025-09-01T10:01:00Z", "Now in Arabic"),
		msg(A, X, "2025-09-01T10:01:05Z", "مطر ناعم على السطح"),
	})

	for _, s := range ss {
		for _, m := range s.Messages {
			if strings.Contains(m.Text, "private reasoning") || strings.Contains(m.Text, "subagent chatter") ||
				strings.Contains(m.Text, "synthetic file body") || strings.Contains(m.Text, "half written") ||
				strings.Contains(m.Text, "Background task finished") || strings.Contains(m.Text, "model-b") {
				t.Errorf("%s: message must not be archived: %q", s.NativeID, m.Text)
			}
		}
	}
}

// TestReadOnly checks that reading the whole fixture tree changes nothing in it.
func TestReadOnly(t *testing.T) {
	env, root := setup(t)
	before := snapshot(t, root)
	adapterstest.ReadAll(t, New(), env)
	after := snapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("fixture tree changed:\nbefore %v\nafter  %v", before, after)
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Directory mtimes from a listing lag behind on NTFS; the set of
			// paths is what proves nothing was created.
			out[p] = "dir"
			return nil
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		out[p] = fmt.Sprintf("size=%d mtime=%s", info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestLargeRecord reads a transcript whose single record is over 2 MB.
func TestLargeRecord(t *testing.T) {
	setNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	root := t.TempDir()
	chats := filepath.Join(root, "home", ".qwen", "projects", "c--big", "chats")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("0123456789abcdef", 2<<20/16) // 2 MiB
	lines := `{"uuid":"u1","parentUuid":null,"sessionId":"` + idA + `","timestamp":"2026-09-05T10:00:00Z","type":"user","cwd":"C:\\big","message":{"role":"user","parts":[{"text":"summarize the dump"}]}}` + "\n" +
		`{"uuid":"u2","parentUuid":"u1","sessionId":"` + idA + `","timestamp":"2026-09-05T10:00:09Z","type":"assistant","cwd":"C:\\big","message":{"role":"model","parts":[{"text":"` + big + `"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(chats, idA+".jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	ss, skipped := adapterstest.ReadAll(t, New(), adapterstest.FixtureEnv(t, root, nil))
	if skipped != 0 || len(ss) != 1 || len(ss[0].Messages) != 2 {
		t.Fatalf("got %d sessions, skipped %d", len(ss), skipped)
	}
	if got := ss[0].Messages[1].Text; len(got) != len(big) {
		t.Errorf("2 MB record text length = %d, want %d", len(got), len(big))
	}
}

// TestEnvRoots checks QWEN_RUNTIME_DIR / QWEN_HOME roots and deduplication.
func TestEnvRoots(t *testing.T) {
	env, root := setup(t)
	alt := filepath.Join(root, "alt-qwen")
	chats := filepath.Join(alt, "projects", "c--elsewhere", "chats")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"uuid":"z1","parentUuid":null,"sessionId":"` + idA + `","timestamp":"2026-09-06T10:00:00Z","type":"user","cwd":"C:\\elsewhere","message":{"role":"user","parts":[{"text":"from another root"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(chats, idA+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{
		"QWEN_RUNTIME_DIR": alt,
		"QWEN_HOME":        "~/.qwen", // the default root again: must not be listed twice
	}
	env.Getenv = func(k string) string { return vars[k] }
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 11 {
		t.Fatalf("%d sources, want 11 (10 default + 1 alt)", len(srcs))
	}
	var altSrc adapters.Source
	for _, s := range srcs {
		if strings.HasPrefix(s.Path, alt) {
			altSrc = s
		} else if s.Root != "" {
			t.Errorf("default-root source %s has Root %q", s.Path, s.Root)
		}
	}
	if altSrc.Root != alt {
		t.Fatalf("alt source root = %q, want %q", altSrc.Root, alt)
	}
	res, err := New().Read(context.Background(), env, altSrc)
	if err != nil || len(res.Sessions) != 1 {
		t.Fatalf("Read alt = %v, %v", res, err)
	}
	if s := res.Sessions[0]; s.Root != alt || s.Cwd != `C:\elsewhere` || s.Title != "from another root" {
		t.Errorf("alt session = root %q cwd %q title %q", s.Root, s.Cwd, s.Title)
	}
}

// TestFreshWholeFileJSON: whole-file JSON sources modified within the last
// 5 s are not read (the agent may be rewriting them); JSONL is always read.
func TestFreshWholeFileJSON(t *testing.T) {
	env, _ := setup(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		info, err := os.Stat(s.Path)
		if err != nil {
			t.Fatal(err)
		}
		setNow(t, info.ModTime().Add(2*time.Second))
		_, err = New().Read(context.Background(), env, s)
		if whole := s.Extra["kind"] != kindChat; whole != errors.Is(err, adapters.ErrRetryLater) || (!whole && err != nil) {
			t.Errorf("%s (%s) modified 2 s ago: err = %v, want ErrRetryLater only for whole-file JSON", filepath.Base(s.Path), s.Extra["kind"], err)
		}
		setNow(t, info.ModTime().Add(6*time.Second))
		if _, err = New().Read(context.Background(), env, s); err != nil {
			t.Errorf("%s modified 6 s ago: %v", filepath.Base(s.Path), err)
		}
	}
}

func TestBrokenWholeFileJSON(t *testing.T) {
	setNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	root := t.TempDir()
	env := adapterstest.FixtureEnv(t, root, nil)
	dir := filepath.Join(env.Home, ".qwen", "tmp", unknownHash)
	if err := os.MkdirAll(filepath.Join(dir, "chats"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		filepath.Join(dir, "logs.json"):                                "[\n  {\n    \"sessionId\": \"torn",
		filepath.Join(dir, "chats", "session-2025-01-01T00-00-x.json"): `{"sessionId": 5, "messages": "no"}`,
	}
	for p, body := range cases {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srcs, err := New().Sources(env)
	if err != nil || len(srcs) != 2 {
		t.Fatalf("Sources = %d, %v", len(srcs), err)
	}
	for _, s := range srcs {
		if _, err := New().Read(context.Background(), env, s); err == nil || errors.Is(err, adapters.ErrRetryLater) {
			t.Errorf("%s: broken JSON must fail the source with a real error, got %v", s.Path, err)
		}
	}
	// An empty prompt log ("[]", as Qwen creates it) is fine.
	if err := os.WriteFile(filepath.Join(dir, "logs.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New().Read(context.Background(), env, adapters.Source{Adapter: "qwen", Path: filepath.Join(dir, "logs.json"),
		Files: []string{filepath.Join(dir, "logs.json")}, Extra: map[string]string{"kind": kindLogs}})
	if err != nil || len(res.Sessions) != 0 {
		t.Errorf("empty logs.json = %v, %v", res, err)
	}
}

func TestCaps(t *testing.T) {
	env, _ := setup(t)
	c := capsFor(env)
	if !reflect.DeepEqual(c.Resume, []string{"qwen", "--resume", "{id}"}) ||
		!reflect.DeepEqual(c.Fork, []string{"--fork-session"}) ||
		!reflect.DeepEqual(c.SkipPermissions, []string{"--yolo"}) ||
		c.RootEnv != "QWEN_RUNTIME_DIR" || c.App != nil ||
		!reflect.DeepEqual(c.ProgramCandidates, []string{`%APPDATA%\npm\qwen.cmd`}) {
		t.Errorf("caps with projects/ transcripts = %+v", c)
	}

	// Only legacy prompt logs (qwen 0.0.8): nothing can be resumed.
	legacy := adapterstest.FixtureEnv(t, t.TempDir(), nil)
	dir := filepath.Join(legacy.Home, ".qwen", "tmp", unknownHash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	c = capsFor(legacy)
	if c.Resume != nil || c.Fork != nil || c.SkipPermissions != nil {
		t.Errorf("caps with only legacy logs = %+v, want no resume", c)
	}
	// Must not panic on the real machine; logs only whether resume is offered.
	t.Logf("this machine: qwen resume offered = %v", New().Caps().Resume != nil)
}

func TestHashVariants(t *testing.T) {
	got := hashVariants(`C:\Work\Alpha`)
	want := []string{
		hexHash(`C:\Work\Alpha`),
		hexHash(`c:\work\alpha`),
		hexHash(`c:\Work\Alpha`),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hashVariants = %v, want %v", got, want)
	}
}

// TestConcurrentReads reads every source from several goroutines at once (the
// syncer reads sources in parallel; the lookup caches are shared).
func TestConcurrentReads(t *testing.T) {
	env, _ := setup(t)
	srcs, err := New().Sources(env)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := adapterstest.ReadAll(t, New(), env)
	results := make(chan []model.Session, 4*len(srcs))
	errs := make(chan error, 4*len(srcs))
	for range 4 {
		for _, s := range srcs {
			go func() {
				res, err := New().Read(context.Background(), env, s)
				errs <- err
				results <- res.Sessions
			}()
		}
	}
	var got []model.Session
	for range 4 * len(srcs) {
		if err := <-errs; err != nil {
			t.Error(err)
		}
		got = append(got, <-results...)
	}
	if len(got) != 4*len(want) {
		t.Fatalf("%d sessions from concurrent reads, want %d", len(got), 4*len(want))
	}
	cwds := map[string]string{}
	for _, s := range want {
		cwds[s.NativeID+string(s.Origin)] = s.Cwd
	}
	for _, s := range got {
		if cwds[s.NativeID+string(s.Origin)] != s.Cwd {
			t.Errorf("%s: concurrent cwd %q, sequential %q", s.NativeID, s.Cwd, cwds[s.NativeID+string(s.Origin)])
		}
	}
}

func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }
