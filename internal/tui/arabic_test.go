package tui

import (
	"strings"
	"testing"
)

func TestShapeJoiningForms(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// ب initial, س medial, م final; ا isolated, ل initial, ل medial, ه final.
		{"bismillah", "بسم الله", "\uFE91\uFEB4\uFEE2 \uFE8D\uFEDF\uFEE0\uFEEA"},
		// lam-alef ligature: isolated at word start, final after a joining letter.
		{"lam-alef isolated", "لا", "\uFEFB"},
		{"lam-alef final", "سلام", "\uFEB3\uFEFC\uFEE1"},
		{"lam-hamza-alef", "لأن", "\uFEF7\uFEE5"},
		// right-joining letters never join forward: د after ع is final, the next د isolated.
		{"right joining", "عدد", "\uFECB\uFEAA\uFEA9"},
		// single letters are isolated.
		{"isolated", "ب", "\uFE8F"},
		// diacritics are dropped (display only) and do not break joining.
		{"diacritics", "كُتُب", "\uFEDB\uFE98\uFE90"},
		// tatweel joins on both sides and is kept.
		{"tatweel", "بـب", "\uFE91ـ\uFE90"},
		// Persian letters from Presentation Forms-A.
		{"persian", "پی", "\uFB58\uFBFD"},
		// Latin text is untouched.
		{"latin", "hello (world)", "hello (world)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Shape(c.in); got != c.want {
				t.Errorf("Shape(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestVisual(t *testing.T) {
	marhaba := "\uFE8E\uFE92\uFEA3\uFEAE\uFEE3" // shaped مرحبا in visual (reversed) order
	cases := []struct {
		name, in, want string
	}{
		{"pure arabic", Shape("بسم الله"), "\uFEEA\uFEE0\uFEDF\uFE8D \uFEE2\uFEB4\uFE91"},
		{"arabic inside english", "Hello " + Shape("مرحبا") + " world", "Hello " + marhaba + " world"},
		{"english inside arabic", Shape("مرحبا Go 123"), "Go 123 " + marhaba},
		{"arabic number after arabic", Shape("عدد 42"), "42 \uFEA9\uFEAA\uFECB"},
		{"number between arabic and english", "x " + Shape("مرحبا") + " 12 abc", "x 12 " + marhaba + " abc"},
		{"brackets mirrored", Shape("(مرحبا)"), "(" + marhaba + ")"},
		{"latin untouched", "plain (text) 123", "plain (text) 123"},
		{"hebrew", "שלום", "םולש"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Visual(c.in); got != c.want {
				t.Errorf("Visual(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestVisualKeepsMarksAfterBase(t *testing.T) {
	// A combining mark stays after its base when an RTL run is reversed.
	in := "אָב" // alef + qamats, bet
	want := "באָ"
	if got := Visual(in); got != want {
		t.Errorf("Visual(%q) = %q, want %q", in, got, want)
	}
}

func TestDisplayLinesShapedWrapsThenReorders(t *testing.T) {
	// Two Arabic words that do not fit on one 4-cell line: the FIRST word must be
	// on the first line (wrap in logical order, then reorder each line).
	lines := displayLines("سلام عليكم", 5, arabicShaped, false)
	if len(lines) != 2 {
		t.Fatalf("got %d lines %q, want 2", len(lines), lines)
	}
	if want := Visual(Shape("سلام")); strings.TrimSpace(lines[0]) != want {
		t.Errorf("line 0 = %q, want %q", lines[0], want)
	}
}

func TestDisplayLinesRawIsUnchanged(t *testing.T) {
	lines := displayLines("سلام عليكم\nsecond", 40, arabicRaw, false)
	if len(lines) != 2 || lines[0] != "سلام عليكم" || lines[1] != "second" {
		t.Errorf("raw lines = %q", lines)
	}
}

func TestDisplayLinesRightAlign(t *testing.T) {
	lines := displayLines("سلام", 10, arabicShaped, true)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "      ") || cellWidth(lines[0]) != 10 {
		t.Errorf("right-aligned line = %q (width %d)", lines[0], cellWidth(lines[0]))
	}
	lines = displayLines("hello", 10, arabicShaped, true)
	if lines[0] != "hello" {
		t.Errorf("LTR line must stay left-aligned, got %q", lines[0])
	}
}

func TestDisplayLineTruncatesInLogicalOrder(t *testing.T) {
	// The beginning of the text is kept; the ellipsis ends up on the left of RTL text.
	got := displayLine("مرحبا بالعالم الكبير", 8, arabicShaped)
	if cellWidth(got) > 8 {
		t.Fatalf("width %d > 8: %q", cellWidth(got), got)
	}
	if !strings.HasPrefix(got, "…") {
		t.Errorf("ellipsis should be on the left of an RTL line: %q", got)
	}
	if !strings.HasSuffix(got, "\uFEE3") { // initial meem of مرحبا is the rightmost glyph
		t.Errorf("first word should be on the right: %q", got)
	}
}

func TestVisualCursor(t *testing.T) {
	// For a pure Arabic query the logical position p is shown at the mirrored column.
	text, col := visualWithCursor("سلام", 0, arabicShaped)
	if text != Visual(Shape("سلام")) {
		t.Fatalf("text = %q", text)
	}
	// cursor at logical 0 = on the first letter, which is the rightmost cell.
	if col != cellWidth(text)-1 {
		t.Errorf("cursor col for pos 0 = %d, want %d", col, cellWidth(text)-1)
	}
	// cursor at the end of RTL text is left of everything.
	if _, col := visualWithCursor("سلام", 4, arabicShaped); col != -1 {
		t.Errorf("cursor col at end = %d, want -1 (before the text)", col)
	}
	// Latin: identity.
	if text, col := visualWithCursor("abc", 1, arabicShaped); text != "abc" || col != 1 {
		t.Errorf("latin = %q %d", text, col)
	}
	if _, col := visualWithCursor("abc", 3, arabicShaped); col != 3 {
		t.Errorf("latin end col = %d, want 3", col)
	}
}
