package tui

import "testing"

func TestItemForKey(t *testing.T) {
	cases := []struct {
		text string
		want menuItemID
		ok   bool
	}{
		{"r", itemResume, true}, {"R", itemResume, true}, {"ق", itemResume, true},
		{"f", itemFork, true}, {"F", itemFork, true}, {"ب", itemFork, true},
		{"!", itemSkip, true},
		{"o", itemRead, true}, {"O", itemRead, true}, {"خ", itemRead, true},
		{"c", itemCopy, true}, {"C", itemCopy, true}, {"ؤ", itemCopy, true},
		{"d", itemFolder, true}, {"D", itemFolder, true}, {"ي", itemFolder, true},
		{"x", 0, false}, {"", 0, false}, {"rr", 0, false}, {"غ", 0, false},
	}
	for _, c := range cases {
		got, ok := itemForKey(c.text)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("itemForKey(%q) = %v,%v want %v,%v", c.text, got, ok, c.want, c.ok)
		}
	}
}

func TestIsYes(t *testing.T) {
	for _, s := range []string{"y", "Y", "غ"} {
		if !isYes(s) {
			t.Errorf("isYes(%q) = false", s)
		}
	}
	for _, s := range []string{"n", "N", "", "enter", "yes", "ن"} {
		if isYes(s) {
			t.Errorf("isYes(%q) = true", s)
		}
	}
}

func TestIsLetterKey(t *testing.T) {
	if !isLetterKey("u", 'u') || !isLetterKey("U", 'u') || !isLetterKey("ع", 'u') {
		t.Error("U / ع should match 'u'")
	}
	if !isLetterKey("i", 'i') || !isLetterKey("ه", 'i') {
		t.Error("I / ه should match 'i'")
	}
	if isLetterKey("ع", 'i') || isLetterKey("x", 'u') {
		t.Error("wrong key matched")
	}
}
