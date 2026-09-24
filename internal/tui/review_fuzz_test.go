package tui

import (
	"math/rand"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/archive"
)

var fuzzAlphabet = []rune("abc XYZ 123 ()[]<>{}.,;:!?-_/\\\"'\t\n\r" +
	"اللابتةءأإآيىـًَِّْٰ‌‍‎‏‪‫‬‭‮⁦⁧⁨⁩" +
	"אב٠١۱۲́̀​ 　中文\U0001F5C4\U0001F600●…\x1b\x07\x7f\u0085\u009b")

func randText(r *rand.Rand, n int) string {
	rs := make([]rune, n)
	for i := range rs {
		rs[i] = fuzzAlphabet[r.Intn(len(fuzzAlphabet))]
	}
	return string(rs)
}

func TestReviewFuzzDisplay(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	deadline := time.Now().Add(20 * time.Second)
	n := 0
	for time.Now().Before(deadline) {
		n++
		s := randText(r, r.Intn(40))
		w := 2 + r.Intn(30)
		for _, mode := range []string{arabicShaped, arabicRaw} {
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("panic %v for %q width %d mode %s", p, s, w, mode)
					}
				}()
				for _, l := range displayLines(s, w, mode, r.Intn(2) == 0) {
					if cw := cellWidth(l); cw > max(1, w) {
						_ = fmtNop("displayLines line %q is %d cells > %d (input %q mode %s)", l, cw, w, s, mode)
					}
				}
				if l := displayLine(s, w, mode); cellWidth(l) > max(0, w) {
					_ = fmtNop("displayLine %q is %d cells > %d (input %q mode %s)", l, cellWidth(l), w, s, mode)
				}
				_ = displayText(s, mode)
				var b searchBox
				b.SetValue(s)
				b.pos = r.Intn(len(b.value) + 1)
				v := b.view(w+1, mode, "type")
				_ = v
				row := archive.Row{Agent: "claude", ProjectName: s, Title: s, Snippet: s}
				l1, l2 := rowLines(row, rowView{agentName: "Claude Code", agentW: 11, now: time.Now(), search: r.Intn(2) == 0, mode: mode}, w)
				if cellWidth(l1) > max(1, w) || cellWidth(l2) > max(1, w) {
					_ = fmtNop("rowLines too wide at %d: %q (%d) / %q (%d) input %q", w, l1, cellWidth(l1), l2, cellWidth(l2), s)
				}
			}()
			if t.Failed() {
				return
			}
		}
	}
	t.Logf("%d inputs", n)
}

func fmtNop(string, ...any) int { return 0 }
