package launch

import "testing"

// An id from agent data must never be read as an option of the agent program
// (e.g. a crafted session id "--dangerously-skip-permissions").
func TestIDNeverAnOption(t *testing.T) {
	for _, id := range []string{"--dangerously-skip-permissions", "-c", "--yolo", ".hidden", ":x", ""} {
		if validID(id) {
			t.Errorf("validID(%q) = true, want false", id)
		}
	}
	for _, id := range []string{"1e20bd33-93f6-45b4-8c9e-1b2c3d4e5f60", "ses_abc.1", "T-123:4"} {
		if !validID(id) {
			t.Errorf("validID(%q) = false, want true", id)
		}
	}
}
