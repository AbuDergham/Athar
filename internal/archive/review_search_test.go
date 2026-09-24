package archive

import (
	"testing"

	"github.com/AbuDergham/Athar/internal/model"
)

func TestReviewSearchScenarios(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("a"),
		live("wrap", "Wrapped", "please fix the login\nbug in the form", "done"),
		live("dbl", "Double", "fix the login  bug please", "ok"),
		live("ar", "مشروع المدرسة", "أحمد كتب الكُتُب في ٢٠٢٦", "تمام"),
		live("split", "Deploy notes", "we should deploy", "telegram bot ready"),
		live("tool", "Tool", "run it"),
		live("nbsp", "NBSP", "hello world friends"),
	)
	// A tool output: indexed first 2 KB
	s := live("tool2", "T2", "x")
	s.Messages = append(s.Messages, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, Text: "result: GREEN"})
	mustApply(t, d, src("b"), s)

	for _, q := range []string{
		`"login bug"`, `login bug`, `مشروع`, `احمد`, `كتب`, `2026`, `مدرسه`,
		`deploy telegram`, `"deploy telegram"`, `in`, `green`, `"hello world"`, `في`, `ال`,
	} {
		rows := mustSearch(t, d, Filter{Query: q})
		var out []string
		for _, r := range rows {
			out = append(out, r.NativeID+"("+itoa(r.MatchedTerms)+"/"+itoa(r.TotalTerms)+" seq"+itoa(r.HitSeq)+") "+r.Snippet)
		}
		t.Logf("%-20q -> %q", q, out)
	}
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
