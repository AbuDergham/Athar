package tui

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/model"
)

func TestReviewNavigationInvariants(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for trial := 0; trial < 30; trial++ {
		n := 1 + r.Intn(500)
		var rows []archive.Row
		tm := testNow
		for i := 0; i < n; i++ {
			tm = tm.Add(-time.Duration(r.Intn(40)) * time.Hour)
			rows = append(rows, archive.Row{SessionID: int64(i + 1), Agent: "claude", NativeID: fmt.Sprint("id-", i), Title: fmt.Sprint("t", i),
				ProjectName: "P", Machine: "M1", Coverage: model.CoverageFull, StartedAt: tm, UpdatedAt: tm})
		}
		h := newHarness(t, rows, map[int64][]model.Message{})
		h.wait = 50 * time.Millisecond
		height := 8 + r.Intn(30)
		h.send(tea.WindowSizeMsg{Width: 100, Height: height})
		keys := []string{"up", "down", "pgup", "pgdown", "home", "end", "ctrl+up", "ctrl+down"}
		for step := 0; step < 60; step++ {
			k := keys[r.Intn(len(keys))]
			switch k {
			case "pgup":
				h.send(tea.KeyPressMsg{Code: tea.KeyPgUp})
			case "pgdown":
				h.send(tea.KeyPressMsg{Code: tea.KeyPgDown})
			default:
				h.key(k)
			}
			v := h.view()
			if !strings.Contains(v, " > ") {
				t.Fatalf("trial %d step %d key %s: selected row not visible (cursor %d top %d rows %d height %d)\n%s", trial, step, k, h.u.cursor, h.u.top, len(h.u.rows), height, v)
			}
			if got, want := len(strings.Split(h.u.View().Content, "\n")), height; got != want {
				t.Fatalf("view has %d lines, want %d", got, want)
			}
		}
	}
}
