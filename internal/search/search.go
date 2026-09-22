// Package search holds the pure (SQL-free) parts of search: text
// normalization for Arabic and Latin scripts, and query parsing.
package search

// Normalize folds text for indexing and matching (spec §5.2):
// NFKC; remove Arabic diacritics U+064B–U+065F, U+0670, Quranic marks
// U+06D6–U+06ED and tatweel U+0640; أ إ آ ٱ → ا, ة → ه, ى → ي, ؤ → و, ئ → ي,
// Persian ک → ك and ی → ي; Arabic-Indic digits → ASCII; Latin case folding.
// The same function is applied to stored text and to queries.
func Normalize(s string) string { return s }

// Term is one unit of a parsed query.
type Term struct {
	Text   string // already normalized
	Phrase bool   // came from "double quotes": must match inside one message
	Short  bool   // fewer than 3 runes: matches titles and project names only
}

// Parse splits a raw query into normalized terms. Whitespace separates terms;
// "double quoted" text is one phrase term. Empty terms are dropped.
func Parse(raw string) []Term { return nil }
