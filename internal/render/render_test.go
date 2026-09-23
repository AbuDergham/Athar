package render

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

// fixturePage returns a synthetic conversation covering every message kind.
func fixturePage() Page {
	t0 := time.Date(2026, 9, 20, 18, 30, 0, 0, time.UTC)
	return Page{
		AgentName:   "Claude Code",
		Title:       "تنزيل الفيديوهات من القناة",
		ProjectPath: `D:\Work\Demo Project`,
		StartedAt:   t0,
		UpdatedAt:   t0.Add(95 * time.Minute),
		Coverage:    model.CoverageFull,
		Messages: []model.Message{
			{Role: model.RoleUser, Kind: model.KindText, At: t0, Text: "مرحبا، أريد تنزيل كل الفيديوهات\nthen upload them"},
			{Role: model.RoleAssistant, Kind: model.KindText, At: t0.Add(time.Minute), Text: "Sure. <script>alert(1)</script> & done"},
			{Role: model.RoleAssistant, Kind: model.KindToolCall, At: t0.Add(2 * time.Minute), Text: "Bash: go test ./..."},
			{Role: model.RoleTool, Kind: model.KindToolOutput, At: t0.Add(3 * time.Minute), Text: "ok  \x1b[32mPASS\x1b[0m\nline two"},
			{Role: model.RoleUser, Kind: model.KindText, At: t0.Add(4 * time.Minute), Text: "the video download works"},
		},
		ScrollTo: 4,
		Hits:     []int{0, 4},
	}
}

// writeRead renders p into a fresh temp dir and returns the HTML.
func writeRead(t *testing.T, p Page) string {
	t.Helper()
	path, err := Write(p, t.TempDir())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	return string(b)
}

func TestWriteFileNameAndDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "athar", "read") // does not exist yet
	p1, err := Write(fixturePage(), dir)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	p2, err := Write(fixturePage(), dir)
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if p1 == p2 {
		t.Fatalf("two writes returned the same path %q", p1)
	}
	re := regexp.MustCompile(`^athar-\d+\.html$`)
	for _, p := range []string{p1, p2} {
		if filepath.Dir(p) != dir {
			t.Errorf("file %q not in %q", p, dir)
		}
		if !re.MatchString(filepath.Base(p)) {
			t.Errorf("file name %q does not match athar-<unix-nanos>.html", filepath.Base(p))
		}
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Errorf("file %q missing or empty: %v", p, err)
		}
	}
}

func TestNoExternalResources(t *testing.T) {
	p := fixturePage()
	p.Messages = append(p.Messages,
		model.Message{Role: model.RoleUser, Kind: model.KindText, Text: `<img src="https://example.com/x.png"> see http://example.org/a`},
		model.Message{Role: model.RoleAssistant, Kind: model.KindText, Text: `<link rel="stylesheet" href="https://example.com/a.css">`},
	)
	out := writeRead(t, p)

	// Every http(s):// in the output must come from escaped message text:
	// remove the escaped message bodies and nothing must remain.
	stripped := out
	for _, s := range []string{
		`&lt;img src=&#34;https://example.com/x.png&#34;&gt; see http://example.org/a`,
		`&lt;link rel=&#34;stylesheet&#34; href=&#34;https://example.com/a.css&#34;&gt;`,
	} {
		if !strings.Contains(stripped, s) {
			t.Fatalf("escaped message text %q not found in output", s)
		}
		stripped = strings.ReplaceAll(stripped, s, "")
	}
	for _, bad := range []string{"http://", "https://", "<img", "<link", "src=", "href=", "@import", "url("} {
		if strings.Contains(stripped, bad) {
			t.Errorf("output references an external resource: found %q", bad)
		}
	}
	if !strings.Contains(out, `http-equiv="Content-Security-Policy"`) {
		t.Errorf("missing Content-Security-Policy meta")
	}
}

func TestArabicPreserved(t *testing.T) {
	out := writeRead(t, fixturePage())
	for _, s := range []string{"تنزيل الفيديوهات من القناة", "مرحبا، أريد تنزيل كل الفيديوهات"} {
		if !strings.Contains(out, s) {
			t.Errorf("Arabic text %q not preserved", s)
		}
	}
	if !strings.Contains(out, `<meta charset="utf-8">`) {
		t.Errorf("missing utf-8 charset")
	}
}

func TestHitAnchor(t *testing.T) {
	out := writeRead(t, fixturePage())
	if n := strings.Count(out, `id="hit"`); n != 1 {
		t.Fatalf(`id="hit" occurs %d times, want 1`, n)
	}
	// The anchor is on the ScrollTo message (index 4).
	i := strings.Index(out, `id="hit"`)
	rest := out[i:]
	if end := strings.Index(rest, "</article>"); end < 0 || !strings.Contains(rest[:end], "the video download works") {
		t.Errorf(`id="hit" is not on the ScrollTo message`)
	}
	for _, js := range []string{"document.getElementById('hit')", "el.scrollIntoView({block: tall ? 'start' : 'center'", "if (start) show(start, false);"} {
		if !strings.Contains(out, js) {
			t.Errorf("scroll-to-hit script lacks %q", js)
		}
	}

	for _, st := range []int{-1, 99, -5} {
		p := fixturePage()
		p.ScrollTo = st
		if out := writeRead(t, p); strings.Contains(out, `id="hit"`) {
			t.Errorf(`ScrollTo=%d: output has id="hit"`, st)
		}
	}
}

func TestHitsMarkedAndNavigator(t *testing.T) {
	out := writeRead(t, fixturePage())
	articles := articleTags(out)
	if len(articles) != 5 {
		t.Fatalf("got %d articles, want 5", len(articles))
	}
	for i, tag := range articles {
		isHit := strings.Contains(tag, " hit")
		if want := i == 0 || i == 4; isHit != want {
			t.Errorf("article %d hit=%v, want %v: %s", i, isHit, want, tag)
		}
	}
	if !strings.Contains(out, `id="hitnav"`) {
		t.Fatalf("hit navigator missing")
	}
	// ScrollTo (4) is the 2nd of 2 hits.
	if !regexp.MustCompile(`hit <b id="hitpos">2</b>/<span id="hitcount">2</span>`).MatchString(out) {
		t.Errorf("navigator does not show hit 2/2")
	}
	if !strings.Contains(out, "▲") || !strings.Contains(out, "▼") {
		t.Errorf("navigator buttons missing")
	}

	p := fixturePage()
	p.Hits = nil
	if out := writeRead(t, p); strings.Contains(out, `id="hitnav"`) {
		t.Errorf("navigator shown without hits")
	}

	// Out-of-range and duplicate hits are ignored.
	p = fixturePage()
	p.Hits = []int{4, 4, -1, 50, 0}
	out = writeRead(t, p)
	if !strings.Contains(out, `<span id="hitcount">2</span>`) {
		t.Errorf("invalid/duplicate hits not ignored")
	}
}

func TestScriptEscaped(t *testing.T) {
	out := writeRead(t, fixturePage())
	if !strings.Contains(out, "&lt;script&gt;alert(1)&lt;/script&gt; &amp; done") {
		t.Errorf("message <script> not escaped")
	}
	if strings.Contains(out, "<script>alert(1)") {
		t.Errorf("raw <script> from message text in output")
	}
	if n := strings.Count(out, "<script"); n != 1 {
		t.Errorf("found %d <script elements, want exactly the page's own 1", n)
	}
}

func TestArticlesDirAutoAndRoles(t *testing.T) {
	out := writeRead(t, fixturePage())
	arts := articleTags(out)
	wantClass := []string{"role-user", "role-assistant", "role-assistant", "role-tool", "role-user"}
	for i, tag := range arts {
		if !strings.HasPrefix(tag, `<article dir="auto" class="msg `) {
			t.Errorf("article %d lacks dir=auto/msg class: %s", i, tag)
		}
		if !strings.Contains(tag, wantClass[i]) {
			t.Errorf("article %d: %s lacks %s", i, tag, wantClass[i])
		}
	}
	for _, label := range []string{">You<", ">Claude Code<", ">Tool<"} {
		if !strings.Contains(out, label) {
			t.Errorf("role label %q missing", label)
		}
	}
	// Message time shown in local time.
	at := fixturePage().Messages[1].At.Local().Format("15:04")
	if !strings.Contains(out, at) {
		t.Errorf("message time %s missing", at)
	}
}

func TestToolMessagesCollapsed(t *testing.T) {
	out := writeRead(t, fixturePage())
	if n := strings.Count(out, "<details"); n != 2 {
		t.Fatalf("got %d <details>, want 2 (one tool call, one tool output)", n)
	}
	arts := articleBodies(out)
	if !strings.Contains(arts[2], "<details") || !strings.Contains(arts[2], "Bash: go test ./...") {
		t.Errorf("tool call not inside details: %s", arts[2])
	}
	if !strings.Contains(arts[3], "<details") {
		t.Errorf("tool output not inside details")
	}
	if strings.Contains(arts[3], "\x1b") || !strings.Contains(arts[3], "PASS") {
		t.Errorf("ANSI escapes not stripped from tool output: %q", arts[3])
	}
	// A tool message that is the scroll target or a hit is opened.
	p := fixturePage()
	p.ScrollTo = 2
	arts = articleBodies(writeRead(t, p))
	if !strings.Contains(arts[2], "<details open") {
		t.Errorf("scroll-target tool call is not expanded")
	}
}

func TestTruncatesLargeToolOutput(t *testing.T) {
	big := strings.Repeat("a", 20*1024) + "TAILMARK" + strings.Repeat("b", 10*1024-len("TAILMARK"))
	p := fixturePage()
	p.Messages = append(p.Messages, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, Text: big})
	out := writeRead(t, p)
	if !strings.Contains(out, "(10 KB more, truncated)") {
		t.Errorf("truncation note missing")
	}
	if strings.Contains(out, "TAILMARK") {
		t.Errorf("text after 20 KB not truncated")
	}
	if !strings.Contains(out, strings.Repeat("a", 20*1024)) {
		t.Errorf("first 20 KB not kept")
	}

	// A normal text message of the same size is not truncated.
	p = fixturePage()
	p.Messages = append(p.Messages, model.Message{Role: model.RoleAssistant, Kind: model.KindText, Text: big})
	if out := writeRead(t, p); !strings.Contains(out, "TAILMARK") || strings.Contains(out, "truncated)") {
		t.Errorf("text message was truncated")
	}
}

func TestTruncateRuneBoundary(t *testing.T) {
	cases := []struct {
		in       string
		max      int
		wantKept string
		wantMore int
	}{
		{"hello", 10, "hello", 0},
		{"hello", 5, "hello", 0},
		{"hello", 3, "hel", 2},
		{"اب", 3, "ا", 2}, // each letter is 2 bytes; never split a rune
		{"", 3, "", 0},
	}
	for _, c := range cases {
		kept, more := truncate(c.in, c.max)
		if kept != c.wantKept || more != c.wantMore {
			t.Errorf("truncate(%q,%d) = %q,%d want %q,%d", c.in, c.max, kept, more, c.wantKept, c.wantMore)
		}
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain عربي", "plain عربي"},
		{"a\r\nb\rc", "a\nb\nc"},
		{"\x1b[1;31mred\x1b[0m", "red"},
		{"\x1b]8;;http://example.com\x07link\x1b]8;;\x07", "link"}, // OSC hyperlink, BEL
		{"\x1b]0;title\x1b\\after", "after"},                       // OSC, ST terminator
		{"x\x1b7y", "xy"},                                          // two-byte ESC sequence
		{"\x1b(Bz", "z"},                                           // charset selection
		{"nul\x00bell\x07del\x7fc1\u0085\ttab\nnl", "nulbelldelc1\ttab\nnl"},
		{"bad\xff\xfeend", "bad�end"},
	}
	for _, c := range cases {
		if got := cleanText(c.in); got != c.want {
			t.Errorf("cleanText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	long := strings.Repeat("x", 3<<20) // 3 MB single line
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"\n\n  go   test\t./...  \nsecond", 50, "go test ./..."},
		{"abcdef", 3, "abc…"},
		{"أحمد محمد", 4, "أحمد…"},
		{"", 10, ""},
		{"\n \n", 10, ""},
		{long, 5, "xxxxx…"},
		{"a" + strings.Repeat(" ", 100) + "b", 10, "a…"}, // cut before collapsing: still marked
	}
	for _, c := range cases {
		got := firstLine(c.in, c.max)
		if got != c.want {
			g, w := got, c.want
			if len(g) > 40 {
				g = g[:40] + "..."
			}
			if len(w) > 40 {
				w = w[:40] + "..."
			}
			t.Errorf("firstLine(len %d, %d) = %q (len %d), want %q (len %d)", len(c.in), c.max, g, len(got), w, len(c.want))
		}
	}
}

func TestKB(t *testing.T) {
	cases := map[int]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 10 * 1024: "10 KB", 20*1024 + 700: "21 KB"}
	for n, want := range cases {
		if got := kb(n); got != want {
			t.Errorf("kb(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestHeader(t *testing.T) {
	p := fixturePage()
	p.Coverage = model.CoveragePrompts
	out := writeRead(t, p)
	for _, s := range []string{
		"Claude Code",
		`D:\Work\Demo Project`,
		p.StartedAt.Local().Format("2006-01-02 15:04"),
		p.UpdatedAt.Local().Format("2006-01-02 15:04"),
		"prompts",
		"5 messages",
		"prefers-color-scheme: dark",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("header/page lacks %q", s)
		}
	}
}

func TestEmptyConversation(t *testing.T) {
	p := Page{AgentName: "Windsurf", Coverage: model.CoverageMeta, ScrollTo: -1}
	out := writeRead(t, p)
	if !strings.Contains(out, "No messages") {
		t.Errorf("empty-state notice missing")
	}
	if strings.Contains(out, "<article") {
		t.Errorf("unexpected article in empty page")
	}
	if !strings.Contains(out, "(no project)") || !strings.Contains(out, "(untitled)") {
		t.Errorf("placeholders for empty project/title missing")
	}
}

func TestInvalidUTF8AndControls(t *testing.T) {
	p := fixturePage()
	p.Messages = append(p.Messages, model.Message{Role: model.RoleAssistant, Kind: model.KindText, Text: "bad\xff\xfebyte\x00nul\x07bell\ttab"})
	out := writeRead(t, p)
	if strings.ContainsAny(out, "\x00\x07") {
		t.Errorf("control characters leaked into output")
	}
	if !strings.Contains(out, "bad\uFFFDbyte") || !strings.Contains(out, "\ttab") {
		t.Errorf("invalid UTF-8 not replaced or tab lost")
	}
}

// articleTags returns the opening <article …> tags in order.
func articleTags(out string) []string {
	return regexp.MustCompile(`<article[^>]*>`).FindAllString(out, -1)
}

// articleBodies returns each <article>…</article> in order.
func articleBodies(out string) []string {
	return regexp.MustCompile(`(?s)<article.*?</article>`).FindAllString(out, -1)
}
