package tui

// Arabic display for terminals (spec §2.6).
//
// Windows terminals have no bidi reordering, so Arabic printed as-is reads
// backwards (and, depending on the terminal, unjoined). In "shaped" mode Athar
// transforms text for DISPLAY ONLY, in this order:
//
//  1. Shape: replace Arabic letters by their presentation forms (isolated,
//     final, initial, medial; lam-alef ligatures), still in logical order.
//     Harakat and ZWJ/ZWNJ are dropped from the display.
//  2. Wrap the shaped text to the pane width (logical order, so the first
//     words stay on the first line).
//  3. Reorder every visual line with the Unicode bidi algorithm (rules W1–W7,
//     N1–N2, I1–I2, L1–L2 without explicit embeddings), using the character
//     classes of golang.org/x/text/unicode/bidi. Its Ordering type only groups
//     runs by direction (it neither orders runs visually nor keeps levels), so
//     the reordering itself is done here.
//
// Stored and searched text is never transformed.

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"
)

// Arabic display modes ([display] arabic in athar.toml).
const (
	arabicShaped = "shaped"
	arabicRaw    = "raw"
)

// defaultArabic is the display mode when athar.toml does not set one.
//
// §2.6 spike (2026-09-23, Windows Terminal 1.24.11911 with its default
// profile and font, the owner's default terminal): raw Arabic is joined by the
// terminal but laid out left to right, so every word reads backwards and
// lam-alef pairs break. The shaped transform reads correctly, including Latin
// words, numbers, Windows paths and brackets inside Arabic, right-aligned
// preview paragraphs, and the search box (typing, moving the cursor and
// deleting in an Arabic query put the cursor on the right letter). Markers:
// "●" and "…" take one cell; "🗄" also takes one cell but its glyph spills
// into the next one, so it is only drawn at the end of a line.
const defaultArabic = arabicShaped

// joining types (Unicode ArabicShaping.txt).
const (
	jtNone    = iota // U: does not join
	jtRight          // R: joins to the preceding letter only
	jtDual           // D: joins on both sides
	jtCausing        // C: tatweel, ZWJ
)

// arabicForm holds the presentation forms of one letter: isolated, final,
// initial, medial (0 = the letter has no such form).
type arabicForm struct {
	jt                 uint8
	iso, fin, ini, med rune
}

func dual(iso rune) arabicForm  { return arabicForm{jtDual, iso, iso + 1, iso + 2, iso + 3} }
func right(iso rune) arabicForm { return arabicForm{jtRight, iso, iso + 1, 0, 0} }

// arabicForms covers the Arabic letters (Presentation Forms-B) plus the
// Persian/Urdu letters used on the owner's machine (Presentation Forms-A).
var arabicForms = map[rune]arabicForm{
	0x0621: {jtNone, 0xFE80, 0, 0, 0},                   // ء
	0x0622: right(0xFE81),                               // آ
	0x0623: right(0xFE83),                               // أ
	0x0624: right(0xFE85),                               // ؤ
	0x0625: right(0xFE87),                               // إ
	0x0626: dual(0xFE89),                                // ئ
	0x0627: right(0xFE8D),                               // ا
	0x0628: dual(0xFE8F),                                // ب
	0x0629: right(0xFE93),                               // ة
	0x062A: dual(0xFE95),                                // ت
	0x062B: dual(0xFE99),                                // ث
	0x062C: dual(0xFE9D),                                // ج
	0x062D: dual(0xFEA1),                                // ح
	0x062E: dual(0xFEA5),                                // خ
	0x062F: right(0xFEA9),                               // د
	0x0630: right(0xFEAB),                               // ذ
	0x0631: right(0xFEAD),                               // ر
	0x0632: right(0xFEAF),                               // ز
	0x0633: dual(0xFEB1),                                // س
	0x0634: dual(0xFEB5),                                // ش
	0x0635: dual(0xFEB9),                                // ص
	0x0636: dual(0xFEBD),                                // ض
	0x0637: dual(0xFEC1),                                // ط
	0x0638: dual(0xFEC5),                                // ظ
	0x0639: dual(0xFEC9),                                // ع
	0x063A: dual(0xFECD),                                // غ
	0x0640: {jtCausing, 0x0640, 0x0640, 0x0640, 0x0640}, // tatweel
	0x0641: dual(0xFED1),                                // ف
	0x0642: dual(0xFED5),                                // ق
	0x0643: dual(0xFED9),                                // ك
	0x0644: dual(0xFEDD),                                // ل
	0x0645: dual(0xFEE1),                                // م
	0x0646: dual(0xFEE5),                                // ن
	0x0647: dual(0xFEE9),                                // ه
	0x0648: right(0xFEED),                               // و
	0x0649: {jtDual, 0xFEEF, 0xFEF0, 0xFBE8, 0xFBE9},    // ى
	0x064A: dual(0xFEF1),                                // ي
	0x0671: right(0xFB50),                               // ٱ
	0x067E: dual(0xFB56),                                // پ
	0x0686: dual(0xFB7A),                                // چ
	0x0698: right(0xFB8A),                               // ژ
	0x06A4: dual(0xFB6A),                                // ڤ
	0x06A9: dual(0xFB8E),                                // ک
	0x06AF: dual(0xFB92),                                // گ
	0x06CC: dual(0xFBFC),                                // ی
}

// lamAlef maps the alef following a lam to the ligature's isolated form
// (final form = isolated + 1).
var lamAlef = map[rune]rune{
	0x0622: 0xFEF5, // لآ
	0x0623: 0xFEF7, // لأ
	0x0625: 0xFEF9, // لإ
	0x0627: 0xFEFB, // لا
}

const (
	zwnj = '\u200C'
	zwj  = '\u200D'
)

// isHaraka reports Arabic combining marks (harakat, Quranic marks). They are
// transparent for joining and dropped from the shaped display.
func isHaraka(r rune) bool {
	return (r >= 0x0610 && r <= 0x061A) || (r >= 0x064B && r <= 0x065F) || r == 0x0670 ||
		(r >= 0x06D6 && r <= 0x06DC) || (r >= 0x06DF && r <= 0x06E4) || r == 0x06E7 || r == 0x06E8 ||
		(r >= 0x06EA && r <= 0x06ED)
}

// isRTLRune reports runes from right-to-left scripts (Hebrew, Arabic, Syriac,
// Thaana, NKo, ... and the presentation forms).
func isRTLRune(r rune) bool {
	return (r >= 0x0590 && r <= 0x08FF) || (r >= 0xFB1D && r <= 0xFDFF) || (r >= 0xFE70 && r <= 0xFEFF) ||
		(r >= 0x10800 && r <= 0x10FFF) || (r >= 0x1E800 && r <= 0x1EFFF)
}

// hasRTL reports whether s contains right-to-left text.
func hasRTL(s string) bool {
	for _, r := range s {
		if r >= 0x0590 && isRTLRune(r) {
			return true
		}
	}
	return false
}

func joinType(r rune) uint8 {
	if f, ok := arabicForms[r]; ok {
		return f.jt
	}
	if r == zwj {
		return jtCausing
	}
	return jtNone
}

// Shape replaces Arabic letters by their contextual presentation forms, in
// logical order. Harakat, ZWJ and ZWNJ are removed; other text is unchanged.
func Shape(s string) string {
	out, _ := shapeRunes([]rune(s))
	return string(out)
}

// shapeRunes shapes rs and also returns, for every output rune, the index of
// the first input rune it comes from.
func shapeRunes(rs []rune) (out []rune, src []int) {
	out = make([]rune, 0, len(rs))
	src = make([]int, 0, len(rs))
	// neighbour returns the joining type of the nearest non-transparent rune
	// before (dir=-1) or after (dir=+1) index i, and its index.
	neighbour := func(i, dir int) (uint8, int) {
		for j := i + dir; j >= 0 && j < len(rs); j += dir {
			if isHaraka(rs[j]) {
				continue
			}
			return joinType(rs[j]), j
		}
		return jtNone, -1
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if isHaraka(r) || r == zwj || r == zwnj {
			continue
		}
		f, ok := arabicForms[r]
		if !ok {
			out = append(out, r)
			src = append(src, i)
			continue
		}
		prev, _ := neighbour(i, -1)
		joinsPrev := f.jt != jtNone && (prev == jtDual || prev == jtCausing)
		if r == 0x0644 { // lam + alef → ligature
			if _, j := neighbour(i, +1); j >= 0 {
				if lig, ok := lamAlef[rs[j]]; ok {
					if joinsPrev {
						lig++
					}
					out = append(out, lig)
					src = append(src, i)
					i = j
					continue
				}
			}
		}
		next, _ := neighbour(i, +1)
		joinsNext := (f.jt == jtDual || f.jt == jtCausing) &&
			(next == jtRight || next == jtDual || next == jtCausing)
		out = append(out, f.pick(joinsPrev, joinsNext))
		src = append(src, i)
	}
	return out, src
}

func (f arabicForm) pick(joinsPrev, joinsNext bool) rune {
	switch {
	case joinsPrev && joinsNext && f.med != 0:
		return f.med
	case joinsPrev && f.fin != 0:
		return f.fin
	case joinsNext && !joinsPrev && f.ini != 0:
		return f.ini
	default:
		return f.iso
	}
}

// ---- bidi reordering ----

// cluster is a base rune plus the combining marks that follow it.
type cluster struct {
	start, end int // rune indexes [start, end)
	class      bidi.Class
}

func isMark(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me) || r == zwj || r == 0xFE0E || r == 0xFE0F
}

func runeClass(r rune) bidi.Class {
	p, _ := bidi.LookupRune(r)
	switch c := p.Class(); c {
	case bidi.B, bidi.S:
		return bidi.WS
	case bidi.Control, bidi.LRO, bidi.RLO, bidi.LRE, bidi.RLE, bidi.PDF, bidi.LRI, bidi.RLI, bidi.FSI, bidi.PDI:
		return bidi.BN
	default:
		return c
	}
}

// paragraphRTL applies rules P2/P3: the first strong character decides.
func paragraphRTL(s string) bool {
	for _, r := range s {
		switch runeClass(r) {
		case bidi.L:
			return false
		case bidi.R, bidi.AL:
			return true
		}
	}
	return false
}

// Visual reorders one line of text from logical to visual (left-to-right
// display) order. The paragraph direction comes from the first strong character.
func Visual(line string) string {
	if !hasRTL(line) {
		return line
	}
	return visualLine(line, paragraphRTL(line))
}

// visualLine reorders a line of a paragraph whose direction is rtl.
func visualLine(line string, rtl bool) string {
	rs := []rune(line)
	order, cl, levels := bidiOrder(rs, rtl)
	var b strings.Builder
	b.Grow(len(line))
	for _, ci := range order {
		c := cl[ci]
		if c.end-c.start == 1 && levels[ci]%2 == 1 {
			b.WriteRune(mirror(rs[c.start]))
			continue
		}
		for _, r := range rs[c.start:c.end] {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// bidiOrder splits rs into clusters, resolves their embedding levels and
// returns the clusters in visual order (indexes into cl) with their levels.
// Boundary neutrals and explicit formatting characters are dropped.
func bidiOrder(rs []rune, rtl bool) (order []int, cl []cluster, levels []int) {
	for i, r := range rs {
		if len(cl) > 0 && isMark(r) {
			cl[len(cl)-1].end = i + 1
			continue
		}
		c := runeClass(r)
		if c == bidi.BN {
			continue
		}
		cl = append(cl, cluster{start: i, end: i + 1, class: c})
	}
	n := len(cl)
	para := 0
	if rtl {
		para = 1
	}
	sos := bidi.L
	if rtl {
		sos = bidi.R
	}
	t := make([]bidi.Class, n)
	orig := make([]bidi.Class, n)
	for i := range cl {
		t[i] = cl[i].class
		orig[i] = t[i]
	}
	// W1: a mark without a base takes the type of the previous character.
	for i := range t {
		if t[i] == bidi.NSM {
			if i == 0 {
				t[i] = sos
			} else {
				t[i] = t[i-1]
			}
		}
	}
	// W2: European numbers after an Arabic letter are Arabic numbers.
	lastStrong := sos
	for i := range t {
		switch t[i] {
		case bidi.L, bidi.R, bidi.AL:
			lastStrong = t[i]
		case bidi.EN:
			if lastStrong == bidi.AL {
				t[i] = bidi.AN
			}
		}
	}
	// W3
	for i := range t {
		if t[i] == bidi.AL {
			t[i] = bidi.R
		}
	}
	// W4: a single separator between two numbers of the same type.
	for i := 1; i+1 < n; i++ {
		switch {
		case t[i] == bidi.ES && t[i-1] == bidi.EN && t[i+1] == bidi.EN:
			t[i] = bidi.EN
		case t[i] == bidi.CS && t[i-1] == bidi.EN && t[i+1] == bidi.EN:
			t[i] = bidi.EN
		case t[i] == bidi.CS && t[i-1] == bidi.AN && t[i+1] == bidi.AN:
			t[i] = bidi.AN
		}
	}
	// W5: terminators next to European numbers.
	for i := 0; i < n; i++ {
		if t[i] != bidi.ET {
			continue
		}
		j := i
		for j < n && t[j] == bidi.ET {
			j++
		}
		if (i > 0 && t[i-1] == bidi.EN) || (j < n && t[j] == bidi.EN) {
			for k := i; k < j; k++ {
				t[k] = bidi.EN
			}
		}
		i = j - 1
	}
	// W6
	for i := range t {
		if t[i] == bidi.ES || t[i] == bidi.ET || t[i] == bidi.CS {
			t[i] = bidi.ON
		}
	}
	// W7: European numbers after a strong L are L.
	lastStrong = sos
	for i := range t {
		switch t[i] {
		case bidi.L, bidi.R:
			lastStrong = t[i]
		case bidi.EN:
			if lastStrong == bidi.L {
				t[i] = bidi.L
			}
		}
	}
	// N1/N2: neutrals take the direction of their surroundings, else the embedding direction.
	strongDir := func(c bidi.Class) bidi.Class {
		if c == bidi.L {
			return bidi.L
		}
		return bidi.R // R, EN, AN
	}
	isNeutral := func(c bidi.Class) bool { return c == bidi.WS || c == bidi.ON || c == bidi.B || c == bidi.S }
	for i := 0; i < n; i++ {
		if !isNeutral(t[i]) {
			continue
		}
		j := i
		for j < n && isNeutral(t[j]) {
			j++
		}
		before, after := sos, sos
		if i > 0 {
			before = strongDir(t[i-1])
		}
		if j < n {
			after = strongDir(t[j])
		}
		dir := sos
		if before == after {
			dir = before
		}
		for k := i; k < j; k++ {
			t[k] = dir
		}
		i = j - 1
	}
	// I1/I2
	levels = make([]int, n)
	for i := range t {
		l := para
		if para%2 == 0 {
			switch t[i] {
			case bidi.R:
				l++
			case bidi.AN, bidi.EN:
				l += 2
			}
		} else if t[i] == bidi.L || t[i] == bidi.EN || t[i] == bidi.AN {
			l++
		}
		levels[i] = l
	}
	// L1: trailing white space goes back to the paragraph level.
	for i := n - 1; i >= 0 && orig[i] == bidi.WS; i-- {
		levels[i] = para
	}
	// L2: reverse runs from the highest level down to the lowest odd level.
	order = make([]int, n)
	lv := make([]int, n)
	maxL, minOdd := 0, 1<<30
	for i := range order {
		order[i] = i
		lv[i] = levels[i]
		if levels[i] > maxL {
			maxL = levels[i]
		}
		if levels[i]%2 == 1 && levels[i] < minOdd {
			minOdd = levels[i]
		}
	}
	for level := maxL; level >= minOdd && level > 0; level-- {
		for i := 0; i < n; i++ {
			if lv[i] < level {
				continue
			}
			j := i
			for j < n && lv[j] >= level {
				j++
			}
			for a, b := i, j-1; a < b; a, b = a+1, b-1 {
				order[a], order[b] = order[b], order[a]
				lv[a], lv[b] = lv[b], lv[a]
			}
			i = j
		}
	}
	return order, cl, levels
}

var mirrors = map[rune]rune{
	'(': ')', ')': '(', '[': ']', ']': '[', '{': '}', '}': '{', '<': '>', '>': '<',
	'«': '»', '»': '«', '‹': '›', '›': '‹',
}

func mirror(r rune) rune {
	if m, ok := mirrors[r]; ok {
		return m
	}
	return r
}

// ---- display helpers ----

// cellWidth is the number of terminal cells s occupies (ANSI-aware).
func cellWidth(s string) int { return ansi.StringWidth(s) }

// cleanLine makes text safe for a single terminal line: tabs become spaces
// and other control characters are dropped.
func cleanLine(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r == '\n' || r == '\r':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// displayLine prepares s for one terminal line of at most width cells:
// shaped mode shapes, truncates (keeping the beginning) and reorders.
func displayLine(s string, width int, mode string) string {
	s = cleanLine(s)
	if mode == arabicRaw || !hasRTL(s) {
		return ansi.Truncate(s, width, "…")
	}
	rtl := paragraphRTL(s)
	return visualLine(ansi.Truncate(Shape(s), width, "…"), rtl)
}

// displayText prepares one line of UI text that embeds conversation data
// (a title in a status message, a path in an error) for display: shaped mode
// shapes and reorders it; raw mode only cleans it. The caller fits the width.
func displayText(s, mode string) string {
	s = cleanLine(s)
	if mode == arabicRaw || !hasRTL(s) {
		return s
	}
	return visualLine(Shape(s), paragraphRTL(s))
}

// displayLines wraps s to width cells, honouring its newlines. In shaped mode
// RTL paragraphs are shaped, wrapped and reordered line by line; with
// rightAlign their lines are padded on the left to fill width.
func displayLines(s string, width int, mode string, rightAlign bool) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		para = strings.TrimRight(cleanLine(para), " ")
		if mode == arabicRaw || !hasRTL(para) {
			out = append(out, wrapText(para, width)...)
			continue
		}
		rtl := paragraphRTL(para)
		for _, l := range wrapText(Shape(para), width) {
			v := visualLine(strings.TrimRight(l, " "), rtl)
			if rtl && rightAlign {
				if pad := width - cellWidth(v); pad > 0 {
					v = strings.Repeat(" ", pad) + v
				}
			}
			out = append(out, v)
		}
	}
	return out
}

// wrapText word-wraps plain text to width cells (long words are broken).
func wrapText(s string, width int) []string {
	if s == "" {
		return []string{""}
	}
	if cellWidth(s) <= width {
		return []string{s}
	}
	lines := strings.Split(ansi.Wrap(s, width, ""), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

// visualWithCursor returns the display form of a one-line input value and the
// cell column of the character at logical rune position pos: -1 means "before
// the text" (the end of an RTL value), cellWidth(text) means "after the text".
func visualWithCursor(value string, pos int, mode string) (string, int) {
	rs := []rune(cleanLine(value))
	if pos > len(rs) {
		pos = len(rs)
	}
	if mode == arabicRaw || !hasRTL(value) {
		return string(rs), cellWidth(string(rs[:pos]))
	}
	rtl := paragraphRTL(value)
	shaped, src := shapeRunes(rs)
	order, cl, levels := bidiOrder(shaped, rtl)
	// the shaped cluster that shows logical rune pos
	target := -1
	if pos < len(rs) {
		for ci, c := range cl {
			first := src[c.start]
			last := len(rs)
			if c.end < len(src) {
				last = src[c.end]
			}
			if first <= pos && pos < last {
				target = ci
				break
			}
			if first > pos { // pos is a dropped mark before this cluster
				target = ci
				break
			}
		}
	}
	var b strings.Builder
	col, cursorCol := 0, -2
	for _, ci := range order {
		c := cl[ci]
		if ci == target {
			cursorCol = col
		}
		var cs string
		if c.end-c.start == 1 && levels[ci]%2 == 1 {
			cs = string(mirror(shaped[c.start]))
		} else {
			cs = string(shaped[c.start:c.end])
		}
		b.WriteString(cs)
		col += cellWidth(cs)
	}
	text := b.String()
	if cursorCol == -2 { // at the end of the value
		if rtl {
			cursorCol = -1
		} else {
			cursorCol = col
		}
	}
	return text, cursorCol
}
