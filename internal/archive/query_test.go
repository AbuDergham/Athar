package archive

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
	"github.com/AbuDergham/Athar/internal/search"
)

// searchFixture is a small archive covering the search rules of spec §5.
func searchFixture(t *testing.T) *DB {
	t.Helper()
	d := openTemp(t)
	at := func(s model.Session, h int) model.Session {
		s.StartedAt = t0.Add(time.Duration(h) * time.Hour)
		s.UpdatedAt = s.StartedAt.Add(10 * time.Minute)
		return s
	}
	ar1 := live("ar1", "", "بدأت العمل على بمشروعي الجديد اليوم", "حسنًا، سأبدأ")
	ar2 := live("ar2", "", "أرسل الملف إلى أحمد", "تم")
	ar3 := live("ar3", "", "اقرأ هذه الكُتُب القديمة من فضلك", "حسنًا")
	en := live("en1", "", "prepare the deployment script", "done")
	and := live("and", "", "write a telegram bot", "ok", "now download the video", "done")
	half := live("half", "", "the video is too long", "trimmed")
	phrase := live("phrase", "", "telegram video downloader", "sure")
	short := live("short", "ab testing ideas", "hello", "hi")
	shortMsg := live("shortmsg", "unrelated", "ab ab ab", "cd")
	proj := live("proj", "", "nothing special", "ok")
	proj.Cwd = `C:\work\TelegramVideos`
	codex := live("cx", "", "deploy with codex", "ok")
	codex.Agent = "codex"
	mustApply(t, d, src("a"),
		at(ar1, 1), at(ar2, 2), at(ar3, 3), at(en, 4), at(and, 5), at(half, 6),
		at(phrase, 7), at(short, 8), at(shortMsg, 9), at(proj, 10), at(codex, 11))
	return d
}

func TestSearchArabicVariants(t *testing.T) {
	d := searchFixture(t)
	for _, c := range []struct{ q, want string }{
		{"مشروع", "ar1"},  // finds بمشروعي
		{"المشروع", ""},   // ال is part of the query, the text has بمشروعي
		{"احمد", "ar2"},   // finds أحمد
		{"أحمد", "ar2"},   // and the other way round
		{"كتب", "ar3"},    // finds كُتُب
		{"الكتب", "ar3"},  // the text has الكُتُب
		{"deploy", "en1"}, // finds deployment (and the codex session)
		{"DEPLOYMENT", "en1"},
	} {
		rows := mustSearch(t, d, Filter{Query: c.q})
		got := ids(rows)
		if c.want == "" {
			if len(rows) != 0 {
				t.Errorf("%q found %v, want nothing", c.q, got)
			}
			continue
		}
		if !slices.Contains(got, c.want) {
			t.Errorf("%q found %v, want %s", c.q, got, c.want)
		}
	}
}

func TestSearchSnippetKeepsOriginalText(t *testing.T) {
	d := searchFixture(t)
	rows := mustSearch(t, d, Filter{Query: "كتب"})
	if len(rows) != 1 {
		t.Fatalf("rows %v", ids(rows))
	}
	r := rows[0]
	if !strings.Contains(r.Snippet, "الكُتُب") {
		t.Errorf("snippet %q lacks the original diacritics", r.Snippet)
	}
	if r.HitSeq != 0 || r.MatchedTerms != 1 || r.TotalTerms != 1 {
		t.Errorf("row = %+v", r)
	}

	rows = mustSearch(t, d, Filter{Query: "Deployment"})
	if r, ok := rowByID(rows, "en1"); !ok || !strings.Contains(r.Snippet, "deployment script") {
		t.Errorf("snippet = %q", r.Snippet)
	}
}

func TestSearchPerSessionAndWithPartialFallback(t *testing.T) {
	d := searchFixture(t)
	rows := mustSearch(t, d, Filter{Query: "telegram video"})
	got := ids(rows)
	// Full matches first: "and" (terms in two messages), "phrase" (one message) and
	// "proj" (telegram in the project name, video too: TelegramVideos).
	full := map[string]bool{"and": true, "phrase": true, "proj": true}
	for i, r := range rows {
		if i < len(full) {
			if !full[r.NativeID] || r.MatchedTerms != 2 {
				t.Errorf("row %d = %s (%d of %d), want a full match; order %v", i, r.NativeID, r.MatchedTerms, r.TotalTerms, got)
			}
		} else if r.MatchedTerms != 1 {
			t.Errorf("row %d = %s matches %d terms, want a partial match", i, r.NativeID, r.MatchedTerms)
		}
		if r.TotalTerms != 2 {
			t.Errorf("TotalTerms = %d", r.TotalTerms)
		}
	}
	if !slices.Contains(got, "half") {
		t.Errorf("partial match missing: %v", got)
	}
	// The hit is the message that matches the most terms.
	if r, _ := rowByID(rows, "phrase"); r.HitSeq != 0 {
		t.Errorf("phrase HitSeq = %d", r.HitSeq)
	}
	if r, _ := rowByID(rows, "and"); r.HitSeq != 0 && r.HitSeq != 2 {
		t.Errorf("and HitSeq = %d", r.HitSeq)
	}
	// A title/project-only match has no hit message.
	if r, _ := rowByID(rows, "proj"); r.HitSeq != -1 || r.Snippet != "" {
		t.Errorf("proj row = %+v", r)
	}
}

func TestSearchHitPrefersMessageWithMostTerms(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s", "", "telegram", "video", "a telegram video here", "telegram again"))
	rows := mustSearch(t, d, Filter{Query: "video telegram"})
	if len(rows) != 1 || rows[0].HitSeq != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if !strings.Contains(rows[0].Snippet, "telegram video") {
		t.Errorf("snippet %q", rows[0].Snippet)
	}
}

func TestSearchPhraseMustBeInOneMessage(t *testing.T) {
	d := searchFixture(t)
	rows := mustSearch(t, d, Filter{Query: `"telegram video"`})
	if got := ids(rows); len(got) != 1 || got[0] != "phrase" {
		t.Errorf(`"telegram video" found %v, want only phrase`, got)
	}
	rows = mustSearch(t, d, Filter{Query: `"Video Downloader" telegram`})
	if got := ids(rows); len(got) == 0 || got[0] != "phrase" || rows[0].MatchedTerms != 2 {
		t.Errorf("phrase + word found %v", got)
	}
}

func TestSearchShortTermsMatchTitlesOnly(t *testing.T) {
	d := searchFixture(t)
	rows := mustSearch(t, d, Filter{Query: "ab"})
	if got := ids(rows); len(got) != 1 || got[0] != "short" {
		t.Errorf("ab found %v, want only the session titled 'ab testing ideas'", got)
	}
	if rows[0].HitSeq != -1 || rows[0].MatchedTerms != 1 {
		t.Errorf("row = %+v", rows[0])
	}
	// A short term combined with a long one: both must match the session.
	rows = mustSearch(t, d, Filter{Query: "ab hello"})
	if len(rows) == 0 || rows[0].NativeID != "short" || rows[0].MatchedTerms != 2 {
		t.Errorf("ab hello found %v", ids(rows))
	}
	// Project names are searched too.
	rows = mustSearch(t, d, Filter{Query: "telegramvideos"})
	if got := ids(rows); len(got) != 1 || got[0] != "proj" {
		t.Errorf("project search found %v", got)
	}
}

func TestSearchNoProjectIsNotIndexed(t *testing.T) {
	d := openTemp(t)
	s := live("np", "", "hello there")
	s.Cwd = ""
	mustApply(t, d, src("a"), s)
	if rows := mustSearch(t, d, Filter{Query: "project"}); len(rows) != 0 {
		t.Errorf("(no project) is searchable: %v", ids(rows))
	}
}

func TestSearchTitleMatchRanksFirst(t *testing.T) {
	d := openTemp(t)
	a := live("msgonly", "Something else", "we discussed the scraper yesterday", "ok")
	b := live("titled", "Scraper plan", "we discussed it yesterday", "ok")
	a.UpdatedAt = b.UpdatedAt.Add(time.Hour) // recency would put a first
	mustApply(t, d, src("a"), a, b)
	rows := mustSearch(t, d, Filter{Query: "scraper"})
	if got := ids(rows); len(got) != 2 || got[0] != "titled" {
		t.Errorf("order %v, want the title match first", got)
	}
}

func TestSearchRecencyBreaksTies(t *testing.T) {
	d := openTemp(t)
	var ss []model.Session
	for i := range 3 {
		s := live(fmt.Sprintf("s%d", i), "", "same words here", "ok")
		s.UpdatedAt = t0.Add(time.Duration(i) * time.Hour)
		ss = append(ss, s)
	}
	mustApply(t, d, src("a"), ss...)
	if got := strings.Join(ids(mustSearch(t, d, Filter{Query: "words"})), ","); got != "s2,s1,s0" {
		t.Errorf("order %s", got)
	}
}

func TestTimelineAndFilters(t *testing.T) {
	d := searchFixture(t)
	rows := mustSearch(t, d, Filter{})
	if len(rows) != 11 {
		t.Fatalf("timeline has %d rows", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].UpdatedAt.After(rows[i-1].UpdatedAt) {
			t.Errorf("timeline not newest first at %d: %v", i, ids(rows))
		}
	}
	for _, r := range rows {
		if r.HitSeq != -1 || r.Snippet != "" || r.TotalTerms != 0 {
			t.Errorf("timeline row %+v", r)
		}
	}
	if rows[0].NativeID != "cx" {
		t.Errorf("newest = %s", rows[0].NativeID)
	}

	// Since keeps sessions updated at or after it.
	since := t0.Add(10*time.Hour + 10*time.Minute) // exactly proj's UpdatedAt
	rows = mustSearch(t, d, Filter{Since: since})
	if got := strings.Join(ids(rows), ","); got != "cx,proj" {
		t.Errorf("since: %s", got)
	}
	// Agent filter, with and without a query.
	rows = mustSearch(t, d, Filter{Agent: "codex"})
	if got := strings.Join(ids(rows), ","); got != "cx" {
		t.Errorf("agent filter: %s", got)
	}
	rows = mustSearch(t, d, Filter{Query: "deploy", Agent: "claude"})
	if got := strings.Join(ids(rows), ","); got != "en1" {
		t.Errorf("deploy in claude: %s", got)
	}
	rows = mustSearch(t, d, Filter{Query: "deploy", Since: t0.Add(10 * time.Hour)})
	if got := strings.Join(ids(rows), ","); got != "cx" {
		t.Errorf("deploy since: %s", got)
	}
	// A query of only quotes and spaces is the timeline.
	if rows := mustSearch(t, d, Filter{Query: ` "" `}); len(rows) != 11 {
		t.Errorf("empty query rows %d", len(rows))
	}
}

func TestSearchPaging(t *testing.T) {
	d := openTemp(t)
	var ss []model.Session
	for i := range 25 {
		s := live(fmt.Sprintf("s%02d", i), "", fmt.Sprintf("common text number %d", i), "ok")
		s.UpdatedAt = t0.Add(time.Duration(i) * time.Minute)
		ss = append(ss, s)
	}
	mustApply(t, d, src("a"), ss...)
	for _, q := range []string{"", "common"} {
		all := mustSearch(t, d, Filter{Query: q})
		if len(all) != 25 {
			t.Fatalf("%q: %d rows", q, len(all))
		}
		var paged []string
		for off := 0; off < 30; off += 10 {
			rows, err := d.Search(Filter{Query: q}, 10, off)
			if err != nil {
				t.Fatal(err)
			}
			paged = append(paged, ids(rows)...)
		}
		if strings.Join(paged, ",") != strings.Join(ids(all), ",") {
			t.Errorf("%q: paged %v != all %v", q, paged, ids(all))
		}
		if rows, _ := d.Search(Filter{Query: q}, 10, 100); len(rows) != 0 {
			t.Errorf("%q: offset past the end gave %d rows", q, len(rows))
		}
	}
}

func TestSearchSeesNewData(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s1", "", "alpha words"))
	if rows := mustSearch(t, d, Filter{Query: "alpha"}); len(rows) != 1 {
		t.Fatalf("rows %v", ids(rows))
	}
	mustApply(t, d, src("b"), live("s2", "", "alpha again"))
	if rows := mustSearch(t, d, Filter{Query: "alpha"}); len(rows) != 2 {
		t.Errorf("cached results after a write: %v", ids(rows))
	}
	if _, err := d.FinishSync("M1", map[string]bool{"a": true}, t0); err != nil {
		t.Fatal(err)
	}
	if r, _ := rowByID(mustSearch(t, d, Filter{Query: "alpha"}), "s2"); !r.Missing {
		t.Error("cached Missing flag after FinishSync")
	}
}

func TestBM25SkippedOnlyForVeryCommonTerms(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s", "", "alpha beta", "alpha"))
	for _, c := range []struct {
		matches, total int
		scored         bool
	}{
		{2, 2, true},           // small archive: always scored
		{5000, 5000, true},     // at the floor
		{6000, 100000, true},   // 6% of the messages
		{6000, 10000, false},   // 60% of a large archive
		{30000, 100000, false}, // 30%
	} {
		scores, err := d.bm25("alpha", c.matches, c.total)
		if err != nil {
			t.Fatal(err)
		}
		if got := scores != nil; got != c.scored {
			t.Errorf("bm25 with %d of %d matching: scored=%v, want %v", c.matches, c.total, got, c.scored)
		}
		if c.scored && len(scores) != 2 {
			t.Errorf("scores = %v", scores)
		}
	}
}

func TestHitSeqs(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s", "", "telegram", "nothing", "a VIDEO", "telegram video", "ab"))
	id := session(t, d, "s").SessionID
	got, err := d.HitSeqs(id, "telegram video ab")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int{0, 2, 3}) {
		t.Errorf("HitSeqs = %v, want [0 2 3] (short terms never match messages)", got)
	}
	if got, _ := d.HitSeqs(id, `"telegram video"`); !slices.Equal(got, []int{3}) {
		t.Errorf("phrase HitSeqs = %v", got)
	}
	if got, _ := d.HitSeqs(id, ""); len(got) != 0 {
		t.Errorf("empty query HitSeqs = %v", got)
	}
}

func TestToolOutputIndexedOnlyFirst2KB(t *testing.T) {
	d := openTemp(t)
	s := live("s", "", "run it")
	s.Messages = append(s.Messages, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput,
		Text: "early marker " + strings.Repeat("x", 3000) + " late marker"})
	mustApply(t, d, src("a"), s)
	if rows := mustSearch(t, d, Filter{Query: "early marker"}); len(rows) != 1 {
		t.Errorf("early text not found")
	}
	if rows := mustSearch(t, d, Filter{Query: `"late marker"`}); len(rows) != 0 {
		t.Errorf("text after 2 KB of a tool output is indexed")
	}
	if got, _ := d.HitSeqs(session(t, d, "s").SessionID, `"late marker"`); len(got) != 0 {
		t.Errorf("HitSeqs disagrees with the index: %v", got)
	}
}

func TestSearchQueryWithQuotesAndOperators(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"), live("s", "", `he said "hi" AND NOT (x OR y) * ^ col:val`))
	for _, q := range []string{`AND`, `NOT (x`, `col:val`, `"hi"`, `said "hi`, `*`, `x OR y`} {
		if _, err := d.Search(Filter{Query: q}, 10, 0); err != nil {
			t.Errorf("Search(%q): %v", q, err)
		}
	}
	if rows := mustSearch(t, d, Filter{Query: "col:val"}); len(rows) != 1 {
		t.Errorf("col:val not found")
	}
}

func TestSearchUsesSnippetRules(t *testing.T) {
	d := openTemp(t)
	long := strings.Repeat("filler words ", 40) + "the needle is here " + strings.Repeat("more filler ", 40)
	mustApply(t, d, src("a"), live("s", "", long))
	rows := mustSearch(t, d, Filter{Query: "needle"})
	if len(rows) != 1 {
		t.Fatal("not found")
	}
	want, _ := search.Snippet(long, search.Parse("needle"), SnippetWidth)
	if rows[0].Snippet != want {
		t.Errorf("snippet %q, want %q", rows[0].Snippet, want)
	}
}
