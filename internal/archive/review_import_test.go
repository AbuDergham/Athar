package archive

import (
	"strings"
	"testing"

	"github.com/AbuDergham/Athar/internal/model"
)

func imp(s model.Session) model.Session {
	s.Origin = model.OriginAgentsView
	return s
}

// Spec §4.5: import rows "fill sessions that have no live source or have
// fewer messages". A session read live at 2 messages whose transcript the
// agent then deleted (Claude's 30-day cleanup) while AgentsView kept the
// whole conversation.
func TestReviewImportFillsSessionWhoseLiveSourceIsGone(t *testing.T) {
	d := openTemp(t)
	mustApply(t, d, src("live.jsonl"), live("s1", "t", "one", "two"))
	if _, err := d.FinishSync("M1", map[string]bool{}, t0); err != nil { // the transcript was deleted
		t.Fatal(err)
	}
	if !session(t, d, "s1").Missing {
		t.Fatal("not missing")
	}
	isrc := SourceRecord{Machine: "M1", Adapter: "agentsview", Path: "sessions.db", Fingerprint: "x", AdapterVersion: 1}
	mustApply(t, d, isrc, imp(live("s1", "t", "one", "two", "three", "four")))
	r := session(t, d, "s1")
	got := strings.Join(texts(t, d, r.SessionID), ",")
	t.Logf("messages = %s (count %d)", got, r.MessageCount)
	if got != "one,two,three,four" {
		t.Errorf("SPEC DEVIATION: the import's extra messages are never archived: %s", got)
	}
	rows := mustSearch(t, d, Filter{Query: "four"})
	if len(rows) == 0 {
		t.Errorf("text only the import holds is not searchable")
	}
}
