//go:build windows

package launch

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Spec §6.5: records with missing fields (pid, sessionId, cwd, status, kind,
// procStart) count as not live.
func TestReviewLiveRecordWithoutKindIsNotLive(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	os.MkdirAll(dir, 0o755)
	ft := strconv.FormatUint(ownCreationFiletime(t), 10)
	body := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"sessionId":"s-nokind","procStart":"` + ft + `"}`
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LiveClaudeSessions([]string{root})
	if li, ok := got["s-nokind"]; ok {
		t.Errorf("record without kind/cwd/status reported live: %+v (the menu then hides R as for a background session)", li)
	}
}
