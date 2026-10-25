package imports

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/adapterstest"
	"github.com/AbuDergham/Athar/internal/adapters/claude"
	"github.com/AbuDergham/Athar/internal/model"
)

// TestLiveTwin proves that an imported row and the live adapter's session for
// the same transcript share the archive key (agent, native id) and the root,
// so the archive collapses them into one session (spec §4.5).
func TestLiveTwin(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("imported roots use Windows separators; the live adapter matches them only on Windows")
	}
	env, p := fixtureEnv(t)
	transcript := func(path, id, cwd string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		line := func(typ, role string, content any, ts, uuid string) string {
			b, err := json.Marshal(map[string]any{
				"type": typ, "message": map[string]any{"role": role, "content": content},
				"cwd": cwd, "entrypoint": "cli", "isSidechain": false, "sessionId": id,
				"timestamp": ts, "uuid": uuid,
			})
			if err != nil {
				t.Fatal(err)
			}
			return string(b) + "\n"
		}
		body := line("user", "user", "Fix the build please", "2026-09-01T08:00:01.000Z", "u0000000-0000-4000-8000-000000000001") +
			line("assistant", "assistant", []any{map[string]any{"type": "text", "text": "Done."}}, "2026-09-01T08:00:04.000Z", "u0000000-0000-4000-8000-000000000002")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	transcript(p.alpha, idAlpha, `C:\work\alpha`)
	transcript(p.gamma, idGamma, `C:\work\gamma`)

	live, _ := adapterstest.ReadAll(t, claude.New(), env)
	type key struct{ agent, id, root string }
	liveKeys := map[key]bool{}
	for _, s := range live {
		if s.Origin == model.OriginLive {
			liveKeys[key{s.Agent, s.NativeID, s.Root}] = true
		}
	}
	for _, k := range []key{{"claude", idAlpha, ""}, {"claude", idGamma, filepath.Join(env.Home, ".claude-local")}} {
		if !liveKeys[k] {
			t.Fatalf("live claude adapter did not produce %+v (got %v)", k, liveKeys)
		}
	}

	for _, c := range []struct {
		a  adapters.Adapter
		db string
	}{
		{NewAgentsView(), filepath.Join(env.Home, ".agentsview", "sessions.db")},
		{NewPSM(), filepath.Join(env.Home, ".pi", "agent", "sessions", "sessions.db")},
	} {
		res, err := c.a.Read(context.Background(), env, adapters.Source{Adapter: c.a.ID(), Path: c.db, Files: []string{c.db}})
		if err != nil {
			t.Fatal(err)
		}
		twins := 0
		for _, s := range res.Sessions {
			if liveKeys[key{s.Agent, s.NativeID, s.Root}] {
				twins++
			}
		}
		if twins != 2 {
			t.Errorf("%s: %d imported sessions match a live twin, want 2", c.a.ID(), twins)
		}
	}
}
