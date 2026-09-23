package tui

import (
	"reflect"
	"testing"
)

func menuKeys(es []menuEntry) string {
	s := ""
	for _, e := range es {
		s += e.key
	}
	return s
}

func TestMenuEntries(t *testing.T) {
	claude := menuInput{cliResume: true, canFork: true, canSkip: true, local: true, folder: true, command: true}
	cases := []struct {
		name    string
		in      menuInput
		keys    string
		warning bool
	}{
		{"claude idle", claude, "R!OCD", false},
		{"claude live interactive: fork first, warning", with(claude, func(m *menuInput) { m.live, m.interactive = true, true }), "FR!OCD", true},
		{"claude live background: no resume", with(claude, func(m *menuInput) { m.live = true }), "F!OCD", false},
		{"codex: no fork", menuInput{cliResume: true, canSkip: true, local: true, folder: true, command: true}, "R!OCD", false},
		{"gui app: no skip", menuInput{app: "Cursor", local: true, folder: true, command: true}, "ROCD", false},
		{"other machine or archive-only", with(claude, func(m *menuInput) { m.local = false }), "OC", false},
		{"no resume command", menuInput{local: true, folder: true}, "OD", false},
		{"folder gone", with(claude, func(m *menuInput) { m.folder = false }), "R!OC", false},
		{"nothing but read", menuInput{}, "O", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			es, warn := menuEntries(c.in)
			if got := menuKeys(es); got != c.keys {
				t.Errorf("keys = %q, want %q", got, c.keys)
			}
			if (warn != "") != c.warning {
				t.Errorf("warning = %q, want present=%v", warn, c.warning)
			}
		})
	}
}

func with(m menuInput, f func(*menuInput)) menuInput {
	f(&m)
	return m
}

func TestMenuLabels(t *testing.T) {
	es, _ := menuEntries(menuInput{app: "Cursor", local: true})
	if es[0].id != itemResume || es[0].label != "Open the project in Cursor" {
		t.Errorf("GUI resume entry = %+v", es[0])
	}
	es, _ = menuEntries(menuInput{cliResume: true, canFork: true, canSkip: true, local: true, live: true})
	var ids []menuItemID
	for _, e := range es {
		ids = append(ids, e.id)
	}
	if !reflect.DeepEqual(ids, []menuItemID{itemFork, itemSkip, itemRead}) {
		t.Errorf("ids = %v", ids)
	}
	if es[1].label != "Fork without permission prompts" {
		t.Errorf("skip label when fork is the primary action = %q", es[1].label)
	}
}

func TestSkipAction(t *testing.T) {
	if got := skipAction(menuInput{cliResume: true, canFork: true, canSkip: true, local: true}); got != itemResume {
		t.Errorf("idle session: ! runs %v, want resume", got)
	}
	if got := skipAction(menuInput{cliResume: true, canFork: true, canSkip: true, local: true, live: true, interactive: true}); got != itemFork {
		t.Errorf("live session: ! runs %v, want fork", got)
	}
}
