package archive

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
	"github.com/AbuDergham/Athar/internal/search"
)

// FTS5 trigram MATCH of a quoted term must equal a substring test on the
// normalized text (HitSeqs, snippets and the per-session AND rely on it).
func TestReviewFTSEqualsSubstring(t *testing.T) {
	for _, withNUL := range []bool{false, true} {
		t.Run(fmt.Sprint("nul=", withNUL), func(t *testing.T) {
			alphabet := []string{"a", "b", "A", " ", "\n", "أ", "ا", "ة", "ه", "ي", "ى", "َ", "ـ", "😀", "𝔸", "́", "e", "ﻻ", "ل", "٢", "2", "�", "ß", "İ", "\t", "\"", "*", "é", "‏", "\r"}
			if withNUL {
				alphabet = append(alphabet, "\x00")
			}
			bad := 0
			for seed := int64(1); seed <= 10 && bad < 5; seed++ {
				d := openTemp(t)
				r := rand.New(rand.NewSource(seed))
				gen := func(n int) string {
					var b strings.Builder
					for i := 0; i < n; i++ {
						b.WriteString(alphabet[r.Intn(len(alphabet))])
					}
					return b.String()
				}
				var ss []model.Session
				var norms []string
				for i := 0; i < 300; i++ {
					txt := gen(5 + r.Intn(25))
					ss = append(ss, live(fmt.Sprintf("s%03d", i), "", txt))
					norms = append(norms, indexBody(model.KindText, txt))
				}
				mustApply(t, d, src("a"), ss...)
				idOf := map[int64]int{}
				for _, row := range mustSearch(t, d, Filter{}) {
					var n int
					fmt.Sscanf(row.NativeID, "s%03d", &n)
					idOf[row.SessionID] = n
				}
				for q := 0; q < 3000 && bad < 5; q++ {
					var term string
					if q%2 == 0 {
						rs := []rune(norms[r.Intn(len(norms))])
						if len(rs) < 3 {
							continue
						}
						i := r.Intn(len(rs) - 2)
						term = string(rs[i:min(i+3+r.Intn(3), len(rs))])
					} else {
						term = search.Normalize(gen(3 + r.Intn(3)))
					}
					if utf8.RuneCountInString(term) < 3 || strings.ContainsRune(term, 0) {
						continue
					}
					ids, err := d.matchIDs(term)
					if err != nil {
						t.Fatalf("matchIDs(%q): %v", term, err)
					}
					var got, want []int
					for _, id := range ids {
						sid, _ := splitMsgID(id)
						got = append(got, idOf[sid])
					}
					for i, n := range norms {
						if strings.Contains(n, term) {
							want = append(want, i)
						}
					}
					sort.Ints(got)
					if fmt.Sprint(got) != fmt.Sprint(want) {
						bad++
						t.Errorf("seed %d term %q: FTS %v, substring %v", seed, term, got, want)
						for _, g := range got {
							t.Logf("  body[%d] = %q", g, norms[g])
						}
					}
				}
			}
		})
	}
}
