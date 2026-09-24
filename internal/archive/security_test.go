package archive

import "testing"

// Sync must never touch the network: only drive-letter paths are probed for a
// .git folder, never UNC, \?\, \??\ or device paths (spec §10).
func TestGitRootSkipsNetworkAndDevicePaths(t *testing.T) {
	p := newProjects()
	for _, dir := range []string{
		`\server\share\proj`,
		`\?\UNC\server\share\proj`,
		`\??\UNC\localhost\athar-no-such-share\proj`,
		`/??/UNC/localhost/athar-no-such-share/proj`,
		`\.\pipe\x`,
		`relative\dir`,
	} {
		if got := p.gitRoot(dir); got != "" {
			t.Errorf("gitRoot(%q) = %q, want \"\" (not probed)", dir, got)
		}
	}
}
