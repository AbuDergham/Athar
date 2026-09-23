package search

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		// plan table (spec §5.2)
		{"المشروع", "المشروع"},
		{"أحمد", "احمد"}, {"إسلام", "اسلام"}, {"آمال", "امال"}, {"ٱلله", "الله"},
		{"مدرسة", "مدرسه"}, {"مستشفى", "مستشفي"}, {"مسؤول", "مسوول"}, {"قائمة", "قايمه"},
		{"ك\u064fت\u064fب", "كتب"}, {"بـــرمجة", "برمجه"}, {"\ufefb", "لا"}, // presentation form via NFKC
		{"کتاب", "كتاب"}, {"یک", "يك"},
		{"٢٠٢٦", "2026"}, {"۱۲۳", "123"},
		{"Deploy", "deploy"}, {"\u00c9COLE", "\u00e9cole"},

		// more of §5.2
		{"", ""},
		{"already normal 123", "already normal 123"},
		{"MiXeD Case", "mixed case"},
		{"\ufdf2", "الله"},                 // U+FDF2 ligature: NFKC + alef folding
		{"\ufef7", "لا"}, {"\ufefb", "لا"}, // lam-alef ligatures (with hamza / plain)
		{"\ufe91\ufeb4\ufee2", "بسم"},            // initial/medial/final presentation forms
		{"ا\u0654", "ا"},                         // decomposed hamza: composed by NFKC, then folded
		{"ر\u064eح\u0652م\u064e\u0670ن", "رحمن"}, // fatha, sukun, superscript alef U+0670
		{"ذ\u064e\u0670ل\u0650ك\u064e ٱل\u0652ك\u0650ت\u064e\u0670ب\u064f", "ذلك الكتب"}, // Quranic spelling
		{"الم\u06d6 ذلك\u06da", "الم ذلك"},                                               // Quranic marks U+06D6–U+06ED
		{"\u06edۥ", ""}, // ends of the Quranic range
		{"\u064b\u064c\u064d\u064e\u064f\u0650\u0651\u0652\u0655\u065f", ""}, // U+064B–U+065F
		{"٠١٢٣٤٥٦٧٨٩", "0123456789"},
		{"۰۱۲۳۴۵۶۷۸۹", "0123456789"},
		{"\uff21\uff22\uff23\uff11\uff12\uff13", "abc123"}, // full-width, NFKC
		{"\ufb01le", "file"},                               // ligature, NFKC
		{"e\u0301", "\u00e9"},                              // composed by NFKC
		{"\u03a3Ί\u03a3ΥΦΟ\u03a3 Straße", "σίσυφοσ straße"},

		// folding exposes a composition: needs a second NFKC pass
		{"J\u030c", "\u01f0"},            // no capital J-caron exists; lowercase composes
		{"aـ\u0301", "\u00e1"},           // tatweel removed between base and mark
		{"e\u0654\u0301", "\u00e9"},      // hamza above removed; acute was blocked by it
		{"\uac00\u200b\u11a8", "\uac01"}, // ZWSP removed between Hangul LV and T jamo
		{"\u0130\u0301", "\u00ed"},       // U+0130 lowers to i, which composes with acute

		// invisible format characters are removed (bidi marks, zero-width, BOM, soft hyphen)
		{"مر\u200fحبا", "مرحبا"},
		{"\u200eabc\u200b\u200c\u200d\u2060def\ufeff", "abcdef"},
		{"\u202bنص\u202c \u2067x\u2069", "نص x"},
		{"\u061cعربي", "عربي"},
		{"co\u00adoperate", "cooperate"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeInvalidUTF8(t *testing.T) {
	// Invalid bytes become U+FFFD so the output is always valid UTF-8.
	for _, s := range []string{"a\xffB", "\xc3", "ok\xe2\x80", "\xed\xa0\x80x"} {
		n := Normalize(s)
		if !utf8.ValidString(n) {
			t.Errorf("Normalize(%q) = %q is not valid UTF-8", s, n)
		}
		checkNormalizeProperties(t, s)
	}
	if got := Normalize("a\xffB"); got != "a\ufffdb" {
		t.Errorf("Normalize(a\\xffB) = %q", got)
	}
}

// TestShortcutsMatchUnicode guards the fast paths of foldRune and
// boundaryBefore against the Unicode tables they skip.
func TestShortcutsMatchUnicode(t *testing.T) {
	for r := rune(caselessLo); r <= caselessHi; r++ {
		if unicode.ToLower(r) != r {
			t.Errorf("U+%04X has a lowercase mapping", r)
		}
	}
	for r := rune(0); r < 0x300; r++ {
		var buf [utf8.UTFMax]byte
		if !norm.NFKC.Properties(buf[:utf8.EncodeRune(buf[:], r)]).BoundaryBefore() {
			t.Errorf("U+%04X combines with the rune before it", r)
		}
	}
}

func TestNormalizeLongMarkRuns(t *testing.T) {
	// NFKC inserts U+034F after 30 non-starters (stream-safe text); runs of
	// removed and kept marks around that limit must stay consistent.
	mark := func(r rune, n int) string { return strings.Repeat(string(r), n) }
	for _, s := range []string{
		"a" + mark(0x0301, 35),
		"ب" + mark(0x064E, 40) + "x",
		"a" + mark(0x0654, 20) + mark(0x0301, 20) + "b",
		"J" + mark(0x0640, 3) + mark(0x030C, 31),
		mark(0x0651, 70),
	} {
		checkNormalizeProperties(t, s)
	}
	// The U+034F that NFKC inserted stays when the marks around it are
	// removed; harmless, since no real text has 30+ marks in a row.
	if got, want := Normalize("ب"+mark(0x064E, 40)+"x"), "ب"+string(rune(0x034F))+"x"; got != want {
		t.Errorf("40 fathas: got %q, want %q", got, want)
	}
}

func TestNormalizeMapOffsets(t *testing.T) {
	cases := []struct {
		in, want string
		offs     []int
	}{
		{"AB", "ab", []int{0, 1, 2}},
		{"xأ\u064eحمد", "xاحمد", []int{0, 1, 1, 5, 5, 7, 7, 9, 9, 11}}, // fatha dropped
		{"a\ufefbb", "aلاb", []int{0, 1, 1, 1, 1, 4, 5}},               // ligature expands
		{"e\u0301X", "\u00e9x", []int{0, 0, 3, 4}},                     // composed pair
		{"\u200fab", "ab", []int{3, 4, 5}},                             // RLM dropped
		{"a\xffB", "a\ufffdb", []int{0, 1, 1, 1, 2, 3}},                // invalid byte
	}
	for _, c := range cases {
		got, offs := normalizeMap(c.in)
		if got != c.want || !reflect.DeepEqual(offs, c.offs) {
			t.Errorf("normalizeMap(%q) = %q, %v; want %q, %v", c.in, got, offs, c.want, c.offs)
		}
	}
}

// TestNormalizeFinds checks the search examples of spec §5.2: the normalized
// query is a substring of the normalized stored text.
func TestNormalizeFinds(t *testing.T) {
	cases := []struct{ query, stored string }{
		{"مشروع", "المشروع"},
		{"مشروع", "بمشروعي"},
		{"احمد", "أحمد"},
		{"أحمد", "احمد"},
		{"إسلام", "وإسلام"},
		{"اسلام", "و\u064eإ\u0650س\u0652ل\u064eام"},
		{"كتب", "ك\u064fت\u064fب"},
		{"deploy", "Deployment"},
		{"DEPLOY", "deployment"},
		{"٢٠٢٦", "released in 2026"},
		{"2026", "سنة ٢٠٢٦"},
		{"٢٣", "Sprint 23 notes"},
		{"مدرسة", "المدرسه"},
		{"قائمة", "القايمة"},
	}
	for _, c := range cases {
		q, s := Normalize(c.query), Normalize(c.stored)
		if !strings.Contains(s, q) {
			t.Errorf("%q should find %q: Normalize gave %q in %q", c.query, c.stored, q, s)
		}
	}
}

// interesting holds runes that exercise every branch of Normalize.
var interesting = []rune(
	"aAzZ\u00e9\u00c9ß\u0130\u03a3\u03c2 ٠٩۰۹" + // Latin, Greek, digits
		"ابتةىيؤئأإآٱکیكهو" + // Arabic letters
		"\u064b\u064e\u0651\u0652\u0653\u0654\u0655\u065f\u0670ـ\u06d6\u06ddۥ\u06ed" + // marks
		"\u0300\u0301\u0308\u0327" + // Latin combining marks
		"\ufefb\ufef7\ufdf2\ufe91\ufeb4\ufee2\ufe77\ufc5e\ufe71\ufb01\uff21\uff11…½" + // compatibility forms
		"\u200b\u200c\u200d\u200e\u200f\u061c\u202b\u2067\ufeff\u00ad" + // invisible
		"\u00a0\u3000\"\t\n")

func randomString(rng *rand.Rand) string {
	n := rng.Intn(12)
	var b strings.Builder
	for i := 0; i < n; i++ {
		if rng.Intn(8) == 0 {
			b.WriteRune(rune(rng.Intn(0x3000)))
		} else {
			b.WriteRune(interesting[rng.Intn(len(interesting))])
		}
	}
	return b.String()
}

func TestNormalizeProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 200000; i++ {
		s := randomString(rng)
		checkNormalizeProperties(t, s)
		if t.Failed() {
			return
		}
	}
}

func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"", "أحمد", "Deploy", "aـ\u0301", "\ufef7\u0654", "x\ufe77\u0301", "e\u0654\u0301"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		checkNormalizeProperties(t, s)
	})
}

// checkNormalizeProperties asserts that Normalize is idempotent, returns NFKC
// text, and agrees with normalizeMap, whose offsets must be monotonic, on rune
// boundaries of the input and within range.
func checkNormalizeProperties(t *testing.T, s string) {
	t.Helper()
	n := Normalize(s)
	if n2 := Normalize(n); n2 != n {
		t.Errorf("not idempotent: %q -> %q -> %q", s, n, n2)
	}
	if !norm.NFKC.IsNormalString(n) {
		t.Errorf("Normalize(%q) = %q is not NFKC", s, n)
	}
	m, offs := normalizeMap(s)
	if m != n {
		t.Errorf("normalizeMap(%q) = %q, Normalize = %q", s, m, n)
	}
	if len(offs) != len(m)+1 {
		t.Fatalf("normalizeMap(%q): %d offsets for %d bytes", s, len(offs), len(m))
	}
	if offs[len(m)] != len(s) {
		t.Errorf("normalizeMap(%q): last offset %d, want %d", s, offs[len(m)], len(s))
	}
	_, extra, noop := normalize(s)
	for p := 0; p < len(m); p++ {
		if got := sourceOffset(s, p, extra, noop); got != offs[p] {
			t.Fatalf("sourceOffset(%q, %d) = %d, normalizeMap says %d", s, p, got, offs[p])
		}
	}
	boundary := map[int]bool{len(s): true} // where Go's UTF-8 decoding starts a rune
	for i := range s {
		boundary[i] = true
	}
	for i, o := range offs {
		if o < 0 || o > len(s) || (i > 0 && o < offs[i-1]) {
			t.Fatalf("normalizeMap(%q): bad offsets %v", s, offs)
		}
		if !boundary[o] {
			t.Fatalf("normalizeMap(%q): offset %d is inside a rune", s, o)
		}
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		raw  string
		want []Term
	}{
		{`telegram  "Video Download" أحمد ab`, []Term{
			{Text: "telegram"},
			{Text: "video download", Phrase: true},
			{Text: "احمد"},
			{Text: "ab", Short: true},
		}},
		{"", nil},
		{"   \t\n ", nil},
		{`""`, nil},
		{`"   "`, nil},
		{`ـــ`, nil}, // only tatweel: normalizes to nothing
		{"a", []Term{{Text: "a", Short: true}}},
		{"abc", []Term{{Text: "abc"}}},
		{"ٱ \ufefb", []Term{{Text: "ا", Short: true}, {Text: "لا", Short: true}}},
		{"٢٣ ٢٠٢٦", []Term{{Text: "23", Short: true}, {Text: "2026"}}},
		{`"ab"`, []Term{{Text: "ab", Phrase: true, Short: true}}},
		{`"a b"`, []Term{{Text: "a b", Phrase: true}}},
		{`"Hello   big` + "\t\n" + `World"`, []Term{{Text: "hello big world", Phrase: true}}},
		{`  "  padded phrase  "  `, []Term{{Text: "padded phrase", Phrase: true}}},
		{`foo "unterminated phrase here`, []Term{{Text: "foo"}, {Text: "unterminated phrase here", Phrase: true}}},
		{`foo"bar baz"qux`, []Term{{Text: "foo"}, {Text: "bar baz", Phrase: true}, {Text: "qux"}}},
		{`"one" "two"`, []Term{{Text: "one", Phrase: true}, {Text: "two", Phrase: true}}},
		{"foo\u3000bar\u00a0baz", []Term{{Text: "foo"}, {Text: "bar"}, {Text: "baz"}}}, // Unicode spaces
		{"\uff02full width\uff02", []Term{{Text: "full width", Phrase: true}}},         // NFKC quote
		{"Foo foo FOO", []Term{{Text: "foo"}}},                                         // duplicates dropped
		{`foo "foo"`, []Term{{Text: "foo"}, {Text: "foo", Phrase: true}}},
		{"ك\u064fت\u064fب م\u064eدرسة", []Term{{Text: "كتب"}, {Text: "مدرسه"}}},
		{"a\u200fb", []Term{{Text: "ab", Short: true}}},
		{"x\x00y\x1bz", []Term{{Text: "x", Short: true}, {Text: "y", Short: true}, {Text: "z", Short: true}}},
		{"-foo* OR bar:baz", []Term{{Text: "-foo*"}, {Text: "or", Short: true}, {Text: "bar:baz"}}},
	}
	for _, c := range cases {
		got := Parse(c.raw)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) =\n  %#v\nwant\n  %#v", c.raw, got, c.want)
		}
	}
}

func TestParseTermsAreNormalized(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 50000; i++ {
		raw := randomString(rng)
		for _, term := range Parse(raw) {
			if term.Text == "" {
				t.Fatalf("Parse(%q): empty term", raw)
			}
			if Normalize(term.Text) != term.Text {
				t.Fatalf("Parse(%q): term %q is not normalized", raw, term.Text)
			}
			if strings.ContainsRune(term.Text, '"') {
				t.Fatalf("Parse(%q): term %q contains a quote", raw, term.Text)
			}
			if !term.Phrase && strings.IndexFunc(term.Text, isSeparator) >= 0 {
				t.Fatalf("Parse(%q): word term %q contains a separator", raw, term.Text)
			}
			if term.Short != (utf8.RuneCountInString(term.Text) < 3) {
				t.Fatalf("Parse(%q): term %+v has a wrong Short flag", raw, term)
			}
		}
	}
}

func TestSnippet(t *testing.T) {
	long := strings.Repeat("alpha beta ", 20) + "Telegram VIDEO download" + strings.Repeat(" gamma delta", 20)
	cases := []struct {
		name   string
		text   string
		query  string
		width  int
		want   string
		wantOK bool
	}{
		{"short text, no cut", "Download every Video now", "video", 60,
			"Download every Video now", true},
		{"keeps original diacritics", "قال: و\u064eإ\u0650س\u0652ل\u064eام\u064fه\u064f ح\u064eس\u064eن\u064c جدا\u064b", "اسلام", 60,
			"قال: و\u064eإ\u0650س\u0652ل\u064eام\u064fه\u064f ح\u064eس\u064eن\u064c جدا\u064b", true},
		{"cuts both sides with ellipses", long, "video", 20,
			"…egram VIDEO download…", true},
		{"cut at start only", "Some text before the word TARGET", "target", 12,
			"…word TARGET", true},
		{"collapses whitespace and controls", "line one\n\n\tline\x1b[31m two", "two", 60,
			"line one line [31m two", true},
		{"earliest match of any term", "zeta first then alpha", "alpha zeta", 60,
			"zeta first then alpha", true},
		{"phrase", "the video download done", `"video download"`, 60,
			"the video download done", true},
		{"no match shows the start", "nothing to see here at all", "absent", 10,
			"nothing to…", false},
		{"no terms", "hello world", "", 60, "hello world", false},
		{"short terms are a fallback", "xx ab yy", "ab", 60, "xx ab yy", true},
		{"match inside a presentation form", "كلمة \ufefb أخرى", "لا", 60, "كلمة \ufefb أخرى", true},
		{"empty text", "", "foo", 60, "", false},
		{"match starting with a mark keeps its base", "q\u0301z", "\u0301z", 1, "q\u0301…", true},
		{"leading and trailing whitespace trimmed", "\n\n  hello there  \n", "there", 60, "hello there", true},
	}
	for _, c := range cases {
		got, ok := Snippet(c.text, Parse(c.query), c.width)
		if got != c.want || ok != c.wantOK {
			t.Errorf("%s: Snippet = %q, %v; want %q, %v", c.name, got, ok, c.want, c.wantOK)
		}
	}
}

func TestSnippetDoesNotSplitMarks(t *testing.T) {
	// "ك\u064fت\u064fب" repeated: every cut point must avoid starting on a combining mark.
	text := strings.Repeat("ك\u064fت\u064fب\u064c ", 30) + "هدف" + strings.Repeat(" ك\u064fت\u064fب\u064c", 30)
	for width := 5; width < 40; width++ {
		got, ok := Snippet(text, Parse("هدف"), width)
		if !ok || !strings.Contains(got, "هدف") {
			t.Fatalf("width %d: %q, %v", width, got, ok)
		}
		body := strings.Trim(got, "…")
		r, _ := utf8.DecodeRuneInString(body)
		if isMark(r) {
			t.Fatalf("width %d: snippet starts with a combining mark: %q", width, got)
		}
	}
}

func TestSnippetProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 50000; i++ {
		text := randomString(rng) + randomString(rng) + randomString(rng)
		query := randomString(rng)
		checkSnippetProperties(t, text, query, 1+rng.Intn(80))
		if t.Failed() {
			return
		}
	}
}

func FuzzSnippet(f *testing.F) {
	f.Add("Download every Video now", "video", 60)
	f.Add("ك\u064fت\u064fب \ufefb x", "لا", 3)
	f.Fuzz(func(t *testing.T, text, query string, width int) {
		if !utf8.ValidString(text) || !utf8.ValidString(query) {
			return
		}
		checkSnippetProperties(t, text, query, width%200)
	})
}

// checkSnippetProperties asserts that Snippet never panics, returns valid
// UTF-8 of at most width units (characters with their marks, a space for a
// separator run), and, when the whole text fits, contains the match.
func checkSnippetProperties(t *testing.T, text, query string, width int) {
	t.Helper()
	terms := Parse(query)
	got, ok := Snippet(text, terms, width)
	if !utf8.ValidString(got) {
		t.Fatalf("Snippet(%q, %q, %d) = %q is not valid UTF-8", text, query, width, got)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(got, Ellipsis), Ellipsis)
	units := 0
	for _, r := range body {
		if !isMark(r) {
			units++
		}
	}
	if w := max(width, 1); units > w {
		t.Fatalf("Snippet(%q, %q, %d) = %q has %d units", text, query, width, got, units)
	}
	if !ok {
		return
	}
	if len(terms) == 0 {
		t.Fatalf("Snippet(%q, %q, %d): ok without terms", text, query, width)
	}
	if strings.HasPrefix(got, Ellipsis) || strings.HasSuffix(got, Ellipsis) {
		return // cut: the match may be partly outside the window
	}
	n := Normalize(body)
	for _, term := range terms {
		if strings.Contains(n, term.Text) {
			return
		}
	}
	t.Fatalf("Snippet(%q, %q, %d) = %q contains no term of %v", text, query, width, got, terms)
}

func BenchmarkNormalizeASCII(b *testing.B) {
	s := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		Normalize(s)
	}
}

func BenchmarkNormalizeArabic(b *testing.B) {
	s := strings.Repeat("قال\u064e أحمد\u064f: إن\u0651 المشروع\u064e الجديد\u064e جاهز\u064c للنشر\u0650 في ٢٠٢٦. ", 100)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		Normalize(s)
	}
}

func BenchmarkSnippet(b *testing.B) {
	s := strings.Repeat("قال\u064e أحمد\u064f: إن\u0651 المشروع\u064e الجديد\u064e جاهز\u064c للنشر\u0650. ", 100) + "telegram video"
	terms := Parse("video")
	for i := 0; i < b.N; i++ {
		Snippet(s, terms, 60)
	}
}
