package archive

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

// Athar installed after Claude Code deleted the transcript: the only traces
// are history.jsonl (all 10 prompts) and an AgentsView copy indexed when the
// session had 6 prompts (12 messages). Both sources are read in the same sync,
// in registry order (the claude adapter's prompt log before the importers), and
// the import must not delete the archived prompts it lacks.
//
// Known limitation (not tested): if an import were applied BEFORE the prompt
// log, prompts 7..10 would not be archived, because a lower-origin copy never
// adds messages to a fuller one (prompt-log text differs from transcript text,
// so appending would duplicate prompts in every live session).
func TestReviewUpgradeDeletesPromptsTheImportLacks(t *testing.T) {
	for _, order := range []string{"prompt-log first"} {
		t.Run(order, func(t *testing.T) {
			d := openTemp(t)
			var prompts, conv []model.Message
			for i := 1; i <= 10; i++ {
				at := t0.Add(time.Duration(i) * time.Minute)
				p := model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: fmt.Sprintf("prompt number %d", i)}
				prompts = append(prompts, p)
				if i <= 6 {
					conv = append(conv, p, model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: at, Text: fmt.Sprintf("reply %d", i)})
				}
			}
			pl := model.Session{Agent: "claude", NativeID: "s1", Origin: model.OriginPromptLog, Coverage: model.CoveragePrompts, Messages: prompts}
			im := model.Session{Agent: "claude", NativeID: "s1", Origin: model.OriginAgentsView, Coverage: model.CoverageFull, Messages: conv}
			plSrc := src(`C:\Users\x\.claude\history.jsonl`)
			imSrc := SourceRecord{Machine: "M1", Adapter: "agentsview", Path: `C:\Users\x\.agentsview\sessions.db`, Fingerprint: "f", AdapterVersion: 1}
			if order == "prompt-log first" {
				mustApply(t, d, plSrc, pl)
				mustApply(t, d, imSrc, im)
			} else {
				mustApply(t, d, imSrc, im)
				mustApply(t, d, plSrc, pl)
			}
			// the next syncs read both again
			mustApply(t, d, plSrc, pl)
			mustApply(t, d, imSrc, im)
			all := strings.Join(texts(t, d, session(t, d, "s1").SessionID), " | ")
			for i := 7; i <= 10; i++ {
				if !strings.Contains(all, fmt.Sprintf("prompt number %d", i)) {
					t.Errorf("BUG: archived prompt %d is gone from the archive (still in history.jsonl)", i)
				}
			}
			if rows := mustSearch(t, d, Filter{Query: `"prompt number 9"`}); len(rows) == 0 {
				t.Errorf("BUG: prompt 9 is not searchable")
			}
		})
	}
}
