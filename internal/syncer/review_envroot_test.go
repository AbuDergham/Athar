package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/claude"
)

// CLAUDE_CONFIG_DIR set in the user's PowerShell profile (UI launched from a
// terminal) but not in the scheduled `athar --sync` task (README schtasks line).
func TestReviewClaudeConfigDirFlip(t *testing.T) {
	h := newHarness(t)
	alt := filepath.Join(t.TempDir(), "claude-alt")
	data, err := os.ReadFile(`../adapters/claude/testdata/altconfig/projects/C--work-delta/88888888-8888-4888-8888-888888888888.jsonl`)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(alt, "projects", "C--work-delta")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "88888888-8888-4888-8888-888888888888.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	withVar := func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return alt
		}
		return ""
	}
	noVar := func(string) string { return "" }
	os.MkdirAll(h.env.Home, 0o755)

	run := func(label string, getenv func(string) string) {
		h.env.Getenv = getenv
		rep := h.run(t, claude.New())
		missing := 0
		for _, r := range h.rows(t) {
			if r.Missing {
				missing++
			}
		}
		t.Logf("%-28s %+v sessions=%d missing=%d", label, countsOf(rep), len(h.rows(t)), missing)
	}
	run("UI from terminal (var set)", withVar)
	run("scheduled --sync (no var)", noVar)
	run("UI from terminal again", withVar)
	run("UI from terminal again", withVar)
	for _, r := range h.rows(t) {
		if r.Missing {
			t.Errorf("BUG: %s/%s shown as 'Archive only: the agent deleted its copy' and cannot be resumed, though its transcript is on disk and listed", r.Agent, r.NativeID)
		}
	}
	_ = adapters.Env{}
}
