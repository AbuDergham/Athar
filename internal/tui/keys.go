package tui

import (
	"unicode"
	"unicode/utf8"
)

// menuItemID identifies an entry of the action menu (spec §2.3).
type menuItemID int

const (
	itemResume menuItemID = iota // R: resume in a new tab / open in the app
	itemFork                     // F: resume as a new session id
	itemSkip                     // !: resume without permission prompts
	itemRead                     // O: read in the browser
	itemCopy                     // C: copy the command
	itemFolder                   // D: open the project folder
)

// arabic101 maps a Latin letter key to the character the same physical key
// produces on the Windows Arabic (101) layout, so command keys work without
// switching the keyboard layout.
var arabic101 = map[rune]rune{
	'q': 'ض', 'w': 'ص', 'e': 'ث', 'r': 'ق', 't': 'ف', 'y': 'غ', 'u': 'ع', 'i': 'ه', 'o': 'خ', 'p': 'ح',
	'a': 'ش', 's': 'س', 'd': 'ي', 'f': 'ب', 'g': 'ل', 'h': 'ا', 'j': 'ت', 'k': 'ن', 'l': 'م',
	'z': 'ئ', 'x': 'ء', 'c': 'ؤ', 'v': 'ر', 'n': 'ى', 'm': 'ة',
}

// isLetterKey reports whether the typed text is the given (lower-case) Latin
// letter in either case, or the Arabic (101) character on the same key.
func isLetterKey(text string, latin rune) bool {
	r, size := utf8.DecodeRuneInString(text)
	if size == 0 || size != len(text) {
		return false
	}
	return unicode.ToLower(r) == latin || r == arabic101[latin]
}

// itemForKey maps typed text to the menu entry it selects.
func itemForKey(text string) (menuItemID, bool) {
	if text == "!" {
		return itemSkip, true
	}
	for _, k := range []struct {
		latin rune
		id    menuItemID
	}{{'r', itemResume}, {'f', itemFork}, {'o', itemRead}, {'c', itemCopy}, {'d', itemFolder}} {
		if isLetterKey(text, k.latin) {
			return k.id, true
		}
	}
	return 0, false
}

// isYes reports the confirming answer to a y/N question (y or غ).
func isYes(text string) bool { return isLetterKey(text, 'y') }
