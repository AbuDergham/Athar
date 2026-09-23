package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// searchBox is the one-line search input. It always has focus (spec §2.2).
// It replaces bubbles' textinput (plan Task 10) because the §2.6 spike needs
// Arabic queries drawn shaped and in visual order with the cursor on the
// letter it logically sits at; textinput draws runes in logical order only.
// Pasted text arrives as tea.PasteMsg, so no clipboard module is needed.
type searchBox struct {
	value []rune
	pos   int // cursor, 0..len(value) (logical order)
}

// Value returns the text.
func (b *searchBox) Value() string { return string(b.value) }

// Position returns the cursor position in runes.
func (b *searchBox) Position() int { return b.pos }

// SetValue replaces the text and puts the cursor at its end.
func (b *searchBox) SetValue(s string) {
	b.value = []rune(sanitizeInput(s))
	b.pos = len(b.value)
}

// sanitizeInput keeps the query on one line.
func sanitizeInput(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

func (b *searchBox) insert(s string) {
	rs := []rune(sanitizeInput(s))
	if len(rs) == 0 {
		return
	}
	v := make([]rune, 0, len(b.value)+len(rs))
	v = append(v, b.value[:b.pos]...)
	v = append(v, rs...)
	v = append(v, b.value[b.pos:]...)
	b.value = v
	b.pos += len(rs)
}

func (b *searchBox) paste(s string) { b.insert(s) }

func (b *searchBox) deleteRange(from, to int) {
	from, to = max(0, from), min(len(b.value), to)
	if from >= to {
		return
	}
	b.value = append(b.value[:from:from], b.value[to:]...)
	if b.pos > to {
		b.pos -= to - from
	} else if b.pos > from {
		b.pos = from
	}
}

func (b *searchBox) wordLeft() int {
	i := b.pos
	for i > 0 && unicode.IsSpace(b.value[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(b.value[i-1]) {
		i--
	}
	return i
}

func (b *searchBox) wordRight() int {
	i := b.pos
	for i < len(b.value) && unicode.IsSpace(b.value[i]) {
		i++
	}
	for i < len(b.value) && !unicode.IsSpace(b.value[i]) {
		i++
	}
	return i
}

// key applies an editing key and reports whether the box used it.
func (b *searchBox) key(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "backspace", "ctrl+h":
		b.deleteRange(b.pos-1, b.pos)
	case "delete", "ctrl+d":
		b.deleteRange(b.pos, b.pos+1)
	case "left", "ctrl+b":
		b.pos = max(0, b.pos-1)
	case "right", "ctrl+f":
		b.pos = min(len(b.value), b.pos+1)
	case "ctrl+left", "alt+left", "alt+b":
		b.pos = b.wordLeft()
	case "ctrl+right", "alt+right", "alt+f":
		b.pos = b.wordRight()
	case "ctrl+backspace", "alt+backspace", "ctrl+w":
		b.deleteRange(b.wordLeft(), b.pos)
	case "ctrl+delete", "alt+delete", "alt+d":
		b.deleteRange(b.pos, b.wordRight())
	case "ctrl+a":
		b.pos = 0
	case "ctrl+e":
		b.pos = len(b.value)
	case "ctrl+u":
		b.deleteRange(0, b.pos)
	case "ctrl+k":
		b.deleteRange(b.pos, len(b.value))
	default:
		if msg.Text == "" {
			return false
		}
		b.insert(msg.Text)
	}
	return true
}

// view renders the box in width cells with a block cursor. In shaped mode
// Arabic is shaped and shown in visual order; a value too long for the box
// is shown in logical order, scrolled so the cursor is visible.
func (b *searchBox) view(width int, mode, placeholder string) string {
	cur := func(s string) string { return styleCursor.Render(s) }
	if len(b.value) == 0 {
		return cur(" ") + styleDim.Render(ansi.Truncate(placeholder, max(0, width-1), "…"))
	}
	if mode != arabicRaw && hasRTL(string(b.value)) {
		text, col := visualWithCursor(string(b.value), b.pos, mode)
		if tw := cellWidth(text); tw+1 <= width {
			switch {
			case col < 0:
				return cur(" ") + text
			case col >= tw:
				return text + cur(" ")
			default:
				return ansi.Cut(text, 0, col) + cur(ansi.Cut(text, col, col+1)) + ansi.Cut(text, col+1, tw)
			}
		}
	}
	// logical order, scrolled so that the cursor fits
	start := 0
	for start < b.pos && cellWidth(string(b.value[start:b.pos]))+1 > width {
		start++
	}
	before := string(b.value[start:b.pos])
	var at, after string
	if b.pos < len(b.value) {
		at = string(b.value[b.pos])
		after = ansi.Truncate(string(b.value[b.pos+1:]), max(0, width-cellWidth(before)-cellWidth(at)), "")
	} else {
		at = " "
	}
	return before + cur(at) + after
}
