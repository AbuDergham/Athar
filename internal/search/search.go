// Package search holds the pure (SQL-free) parts of search: text
// normalization for Arabic and Latin scripts, query parsing and snippets.
package search

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Normalize folds text for indexing and matching (spec §5.2):
// NFKC; remove Arabic diacritics U+064B–U+065F, U+0670, Quranic marks
// U+06D6–U+06ED and tatweel U+0640; أ إ آ ٱ → ا, ة → ه, ى → ي, ؤ → و, ئ → ي,
// Persian ک → ك and ی → ي; Arabic-Indic digits → ASCII; Latin case folding.
// The same function is applied to stored text and to queries.
//
// Beyond §5.2 it also removes invisible format characters (bidi marks and
// embeddings, zero-width characters, BOM, soft hyphen; see isInvisible) so
// that text copied from right-to-left contexts still matches, and it turns
// invalid UTF-8 bytes into U+FFFD. The result is NFKC and Normalize is
// idempotent: Normalize(Normalize(s)) == Normalize(s).
func Normalize(s string) string {
	out, _, _ := normalize(s)
	return out
}

// IndexText is Normalize with every run of white space and control
// characters turned into one space: the text the full-text index holds, so
// that a quoted phrase (whose words Parse joins with one space) also matches
// across a line break, a CRLF or a double space.
func IndexText(s string) string {
	n := Normalize(s)
	if !strings.ContainsFunc(n, func(r rune) bool { return isSeparator(r) && r != ' ' }) && !strings.Contains(n, "  ") {
		return n
	}
	return strings.Join(strings.FieldsFunc(n, isSeparator), " ")
}

// normalize implements Normalize. It also reports how many extra
// NFKC + fold passes were needed (0 in all but exotic cases) and whether the
// first NFKC step left s unchanged (true for most text), which lets
// sourceOffset map positions without re-running NFKC.
func normalize(s string) (out string, extra int, nfkcNoop bool) {
	nf := norm.NFKC.String(s) // returns s itself when already NFKC
	nfkcNoop = nf == s
	out, recheck := fold(nf)
	// Removing a character or lowering a letter can, in rare cases, expose a
	// new canonical composition (e.g. "J" + U+030C lowers to the
	// decomposition of U+01F0). Repeat until the text is NFKC again.
	for ; recheck && extra < maxExtraPasses && !norm.NFKC.IsNormalString(out); extra++ {
		out, recheck = fold(norm.NFKC.String(out))
	}
	return out, extra, nfkcNoop
}

// maxExtraPasses bounds the re-normalization loop of Normalize and normalizeMap.
const maxExtraPasses = 3

// drop is returned by foldRune for characters that are removed.
const drop rune = -1

// foldRune maps one rune of NFKC text (step 2–5 of spec §5.2, plus invisible
// format characters). It returns drop to remove the rune.
func foldRune(r rune) rune {
	if r < utf8.RuneSelf {
		if 'A' <= r && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}
	switch {
	case r >= 0x064B && r <= 0x065F, // Arabic diacritics (harakat, shadda, sukun, hamza marks)
		r == 0x0670,                // superscript alef
		r >= 0x06D6 && r <= 0x06ED, // Quranic annotation marks
		r == 0x0640:                // tatweel
		return drop
	case r >= 0x0660 && r <= 0x0669: // Arabic-Indic digits
		return '0' + r - 0x0660
	case r >= 0x06F0 && r <= 0x06F9: // Extended (Persian) Arabic-Indic digits
		return '0' + r - 0x06F0
	case isInvisible(r):
		return drop
	}
	switch r {
	case 0x0623, 0x0625, 0x0622, 0x0671: // أ إ آ ٱ
		return 0x0627 // ا
	case 0x0629: // ة
		return 0x0647 // ه
	case 0x0649, 0x0626, 0x06CC: // ى ئ ی
		return 0x064A // ي
	case 0x0624: // ؤ
		return 0x0648 // و
	case 0x06A9: // ک
		return 0x0643 // ك
	}
	if r >= caselessLo && r <= caselessHi {
		return r // skips the case-table search for Arabic text
	}
	return unicode.ToLower(r)
}

// Hebrew, Arabic, Syriac, Arabic Supplement, Thaana and NKo have no case.
const caselessLo, caselessHi = 0x0590, 0x07FF

// isInvisible reports format characters that never change what a reader sees
// in a search result: soft hyphen, Arabic letter mark, zero-width space and
// (non-)joiners, LRM/RLM, bidi embeddings, overrides and isolates, word
// joiner and invisible operators, and BOM / zero-width no-break space.
func isInvisible(r rune) bool {
	switch {
	case r == 0x00AD, r == 0x061C, r == 0xFEFF:
		return true
	case r >= 0x200B && r <= 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2060 && r <= 0x2064:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// fold applies foldRune to every rune of s (which is NFKC). It returns s
// itself when nothing changes, so already-folded text is not copied.
// recheck is true when the result may no longer be NFKC; see foldFrom.
func fold(s string) (out string, recheck bool) {
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if 'A' <= c && c <= 'Z' {
				return foldFrom(s, i)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if size == 1 || foldRune(r) != r { // size 1 here means an invalid byte
			return foldFrom(s, i)
		}
		i += size
	}
	return s, false
}

// foldFrom returns s[:i] followed by the folded runes of s[i:].
//
// Folding NFKC text can only break NFKC where a rune was removed or changed
// and the next rune written can combine with what precedes it (a combining
// mark, or e.g. a Hangul vowel jamo); recheck reports whether that happened.
// Checking only there avoids a second full scan of every text.
func foldFrom(s string, i int) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString(s[:i])
	recheck, changed := false, false
	for j := i; j < len(s); {
		if c := s[j]; c < utf8.RuneSelf { // ASCII never combines backward
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			changed = c != s[j]
			b.WriteByte(c)
			j++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[j:]) // an invalid byte gives utf8.RuneError
		j += size
		m := foldRune(r)
		if m == drop {
			changed = true
			continue
		}
		if changed && !recheck && !boundaryBefore(m) {
			recheck = true
		}
		changed = m != r
		b.WriteRune(m)
	}
	return b.String(), recheck
}

// boundaryBefore reports whether r never combines with the rune before it
// under NFKC (no rune below U+0300 does).
func boundaryBefore(r rune) bool {
	if r < 0x300 {
		return true
	}
	var buf [utf8.UTFMax]byte
	return norm.NFKC.Properties(buf[:utf8.EncodeRune(buf[:], r)]).BoundaryBefore()
}

// normalizeMap returns Normalize(s) together with a map back to s: offs has
// len(out)+1 entries, offs[i] is the byte offset in s of the input that
// produced output byte i (the start of its normalization segment when NFKC
// changed that segment), and offs[len(out)] == len(s). Offsets are
// non-decreasing and always on rune boundaries of s.
func normalizeMap(s string) (string, []int) {
	out, offs := foldMapPass(s)
	for i := 0; i < maxExtraPasses && !norm.NFKC.IsNormalString(out); i++ {
		next, offs2 := foldMapPass(out)
		for k, o := range offs2 {
			offs2[k] = offs[o]
		}
		out, offs = next, offs2
	}
	return out, offs
}

// foldMapPass is one NFKC + fold pass over s that records source offsets.
// Its output equals fold(norm.NFKC.String(s)).
func foldMapPass(s string) (string, []int) {
	b := make([]byte, 0, len(s))
	offs := make([]int, 0, len(s)+1)
	eachFolded(s, func(src int, m rune) bool {
		n := len(b)
		b = utf8.AppendRune(b, m)
		for ; n < len(b); n++ {
			offs = append(offs, src)
		}
		return true
	})
	offs = append(offs, len(s))
	return string(b), offs
}

// eachFolded calls fn, in order, for every rune m of fold(norm.NFKC.String(s))
// with the byte offset src in s that produced it, until fn returns false.
func eachFolded(s string, fn func(src int, m rune) bool) {
	var it norm.Iter
	it.InitString(norm.NFKC, s)
	for !it.Done() {
		start := it.Pos()
		seg := it.Next()
		// A segment that NFKC left unchanged maps rune by rune; a changed one
		// (composed, decomposed, or part of a multi-segment expansion, where
		// Pos does not advance) maps entirely to its start.
		exact := string(seg) == s[start:it.Pos()]
		for k := 0; k < len(seg); {
			r, size := utf8.DecodeRune(seg[k:])
			src := start
			if exact {
				src = start + k
			}
			k += size
			if m := foldRune(r); m != drop && !fn(src, m) {
				return
			}
		}
	}
}

// sourceOffset returns the byte offset in s of the input that produced byte
// p of Normalize(s) (as normalizeMap would), walking only as far as needed.
// extra and nfkcNoop are what normalize reported for s.
func sourceOffset(s string, p, extra int, nfkcNoop bool) int {
	switch {
	case extra > 0: // rare: compose the maps of every pass
		_, offs := normalizeMap(s)
		return offs[min(p, len(offs)-1)]
	case nfkcNoop: // NFKC changed nothing: fold maps rune by rune
		n := 0
		for i, r := range s {
			if m := foldRune(r); m != drop {
				if n += utf8.RuneLen(m); n > p {
					return i
				}
			}
		}
		return len(s)
	}
	at, n := len(s), 0
	eachFolded(s, func(src int, m rune) bool {
		if n += utf8.RuneLen(m); n > p {
			at = src
			return false
		}
		return true
	})
	return at
}

// Term is one unit of a parsed query.
type Term struct {
	Text   string // already normalized
	Phrase bool   // came from "double quotes": must match inside one message
	Short  bool   // fewer than 3 runes: matches titles and project names only
}

// Parse splits a raw query into normalized terms. Whitespace separates terms;
// "double quoted" text is one phrase term. Empty terms are dropped.
//
// Details: the whole query is normalized first (so a full-width quote or
// space acts like the ASCII one); control characters separate like
// whitespace; a quote also ends the word before it (foo"bar baz" is foo and
// "bar baz"); an unterminated quote makes a phrase up to the end; inside a
// phrase, runs of whitespace become one space and the ends are trimmed.
// Short is set for any term, phrase or not, of fewer than 3 runes. Repeated
// terms (same Text and Phrase) are kept once, in first-seen order.
func Parse(raw string) []Term {
	q := Normalize(raw)
	var terms []Term
	add := func(text string, phrase bool) {
		if phrase {
			text = strings.Join(strings.FieldsFunc(text, isSeparator), " ")
		}
		if text == "" {
			return
		}
		t := Term{Text: text, Phrase: phrase, Short: utf8.RuneCountInString(text) < 3}
		for _, seen := range terms {
			if seen == t {
				return
			}
		}
		terms = append(terms, t)
	}
	inPhrase := false
	start := 0
	for i, r := range q {
		switch {
		case r == '"':
			add(q[start:i], inPhrase)
			inPhrase = !inPhrase
			start = i + 1
		case !inPhrase && isSeparator(r):
			add(q[start:i], false)
			start = i + utf8.RuneLen(r)
		}
	}
	add(q[start:], inPhrase)
	return terms
}

// isSeparator reports runes that separate query words and that snippets
// show as a single space: Unicode white space and control characters.
func isSeparator(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r)
}

// isMark reports combining marks, which belong to the character before them.
func isMark(r rune) bool {
	return unicode.Is(unicode.M, r)
}

// isBidiControl reports bidi embedding, override and isolate controls and
// LRM/RLM/ALM, which snippets drop so they cannot reorder a list row.
func isBidiControl(r rune) bool {
	return r == 0x061C || r == 0x200E || r == 0x200F ||
		(r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// Ellipsis marks the side of a snippet where text was cut.
const Ellipsis = "…"

// Snippet returns about width characters of the ORIGINAL text around the
// earliest place where one of the terms matches, so diacritics, case and
// presentation forms are kept as written. Matching is done on normalized
// text, like the index; terms with Short set are only used when no other
// term matches. About a third of the width goes before the match (more when
// the text ends sooner). Runs of white space and control characters become
// one space, bidi controls are dropped, the result is trimmed, and Ellipsis
// marks each side that was cut. Combining marks stay with their base
// character and do not count toward the width.
//
// ok is false when no term matches (or terms is empty); the snippet then
// shows the start of the text. A width below 1 is treated as 1.
func Snippet(text string, terms []Term, width int) (snippet string, ok bool) {
	if width < 1 {
		width = 1
	}
	start, before := 0, 0
	if len(terms) > 0 && text != "" {
		n, extra, noop := normalize(text)
		p := firstMatch(n, terms, false)
		if p < 0 {
			p = firstMatch(n, terms, true)
		}
		if p >= 0 {
			start, before, ok = sourceOffset(text, p, extra, noop), width/3, true
		}
	}
	// Never start inside a character: a match that begins with a combining
	// mark starts at the mark's base character.
	for start > 0 {
		if r, _ := utf8.DecodeRuneInString(text[start:]); !isMark(r) {
			break
		}
		_, size := utf8.DecodeLastRuneInString(text[:start])
		start -= size
	}
	end, used := forward(text, start, width-before)
	begin := backward(text, start, width-used)

	var b strings.Builder
	if strings.TrimRightFunc(text[:begin], isSeparator) != "" {
		b.WriteString(Ellipsis)
	}
	b.WriteString(collapse(text[begin:end]))
	if strings.TrimLeftFunc(text[end:], isSeparator) != "" {
		b.WriteString(Ellipsis)
	}
	return b.String(), ok
}

// firstMatch returns the smallest byte offset in n of any term whose Short
// flag equals short, or -1.
func firstMatch(n string, terms []Term, short bool) int {
	best := -1
	for _, t := range terms {
		if t.Short != short || t.Text == "" {
			continue
		}
		i := strings.Index(n, t.Text)
		if t.Phrase && strings.Contains(t.Text, " ") {
			i = phraseIndex(n, t.Text) // the index matches the words across any white space
		}
		if i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// phraseIndex returns the offset in n of the words of phrase separated by
// any run of separators, or -1.
func phraseIndex(n, phrase string) int {
	words := strings.Split(phrase, " ")
	for i := range words {
		words[i] = regexp.QuoteMeta(words[i])
	}
	loc := regexp.MustCompile(strings.Join(words, `[\p{Z}\p{Cc}]+`)).FindStringIndex(n)
	if loc == nil {
		return -1
	}
	return loc[0]
}

// forward walks from byte i over at most budget units (a character with its
// combining marks, or a run of separators) and returns the end offset and
// the number of units used.
func forward(s string, i, budget int) (int, int) {
	used := 0
	for ; i < len(s) && used < budget; used++ {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if isSeparator(r) {
			for i < len(s) {
				r, size = utf8.DecodeRuneInString(s[i:])
				if !isSeparator(r) {
					break
				}
				i += size
			}
			continue
		}
		for i < len(s) {
			r, size = utf8.DecodeRuneInString(s[i:])
			if !isMark(r) {
				break
			}
			i += size
		}
	}
	return i, used
}

// backward walks back from byte i over at most budget units (see forward)
// and returns the new start offset.
func backward(s string, i, budget int) int {
	for used := 0; i > 0 && used < budget; used++ {
		var size int
		r, _ := utf8.DecodeLastRuneInString(s[:i])
		if isSeparator(r) {
			for i > 0 {
				r, size = utf8.DecodeLastRuneInString(s[:i])
				if !isSeparator(r) {
					break
				}
				i -= size
			}
			continue
		}
		// trailing combining marks, then the character they belong to
		for i > 0 {
			r, size = utf8.DecodeLastRuneInString(s[:i])
			i -= size
			if !isMark(r) {
				break
			}
		}
	}
	return i
}

// collapse turns runs of separators into one space, drops bidi controls and
// trims both ends.
func collapse(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pending := false
	for _, r := range s {
		switch {
		case isSeparator(r):
			pending = true
		case isBidiControl(r):
		default:
			if pending && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pending = false
			b.WriteRune(r)
		}
	}
	return b.String()
}
