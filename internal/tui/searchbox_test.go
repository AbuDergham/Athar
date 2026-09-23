package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func typeText(b *searchBox, s string) {
	for _, r := range s {
		b.key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func press(b *searchBox, code rune, mod tea.KeyMod) bool {
	return b.key(tea.KeyPressMsg{Code: code, Mod: mod})
}

func TestSearchBoxEditing(t *testing.T) {
	var b searchBox
	typeText(&b, "abc")
	if b.Value() != "abc" || b.Position() != 3 {
		t.Fatalf("after typing: %q pos %d", b.Value(), b.Position())
	}
	press(&b, tea.KeyLeft, 0)
	typeText(&b, "X")
	if b.Value() != "abXc" || b.Position() != 3 {
		t.Fatalf("insert in the middle: %q pos %d", b.Value(), b.Position())
	}
	press(&b, tea.KeyBackspace, 0)
	if b.Value() != "abc" || b.Position() != 2 {
		t.Fatalf("backspace: %q pos %d", b.Value(), b.Position())
	}
	press(&b, tea.KeyDelete, 0)
	if b.Value() != "ab" || b.Position() != 2 {
		t.Fatalf("delete: %q pos %d", b.Value(), b.Position())
	}
	press(&b, 'a', tea.ModCtrl)
	if b.Position() != 0 {
		t.Fatalf("ctrl+a: pos %d", b.Position())
	}
	press(&b, 'e', tea.ModCtrl)
	if b.Position() != 2 {
		t.Fatalf("ctrl+e: pos %d", b.Position())
	}
	if press(&b, tea.KeyUp, 0) {
		t.Error("up must not be handled by the search box")
	}
}

func TestSearchBoxWords(t *testing.T) {
	var b searchBox
	b.SetValue("hello big world")
	press(&b, 'w', tea.ModCtrl)
	if b.Value() != "hello big " {
		t.Fatalf("ctrl+w: %q", b.Value())
	}
	press(&b, tea.KeyLeft, tea.ModCtrl)
	if b.Position() != 6 {
		t.Fatalf("ctrl+left: pos %d", b.Position())
	}
	press(&b, tea.KeyRight, tea.ModCtrl)
	if b.Position() != 9 {
		t.Fatalf("ctrl+right: pos %d", b.Position())
	}
	press(&b, 'u', tea.ModCtrl)
	if b.Value() != " " || b.Position() != 0 {
		t.Fatalf("ctrl+u: %q pos %d", b.Value(), b.Position())
	}
}

func TestSearchBoxPasteAndArabic(t *testing.T) {
	var b searchBox
	b.paste("tele\ngram\tbot")
	if b.Value() != "tele gram bot" {
		t.Fatalf("paste: %q", b.Value())
	}
	b.SetValue("")
	typeText(&b, "سلام")
	if b.Value() != "سلام" || b.Position() != 4 {
		t.Fatalf("arabic typing: %q pos %d", b.Value(), b.Position())
	}
	// moving through and deleting an Arabic query works on logical positions.
	press(&b, tea.KeyLeft, 0)
	press(&b, tea.KeyLeft, 0)
	press(&b, tea.KeyBackspace, 0)
	if b.Value() != "سام" || b.Position() != 1 {
		t.Fatalf("arabic delete: %q pos %d", b.Value(), b.Position())
	}
}

func TestSearchBoxView(t *testing.T) {
	var b searchBox
	if v := b.view(20, arabicShaped, "type here"); !strings.Contains(v, "type here") {
		t.Errorf("placeholder view = %q", v)
	}
	b.SetValue(strings.Repeat("x", 30) + "END")
	v := b.view(10, arabicShaped, "")
	if cellWidth(v) > 10 || !strings.Contains(v, "END") {
		t.Errorf("long value must scroll to the cursor: %q (width %d)", v, cellWidth(v))
	}
	b.SetValue("سلام")
	v = b.view(20, arabicShaped, "")
	if !strings.Contains(v, Visual(Shape("سلام"))) {
		t.Errorf("shaped arabic view = %q", v)
	}
	v = b.view(20, arabicRaw, "")
	if !strings.Contains(v, "سلام") {
		t.Errorf("raw arabic view = %q", v)
	}
}
