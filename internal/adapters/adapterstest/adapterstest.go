// Package adapterstest has helpers shared by adapter tests.
package adapterstest

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/model"
)

// FixtureEnv returns an Env rooted at a testdata tree:
// <root>/home, <root>/appdata, <root>/localappdata, <root>/documents.
// getenv is consulted for environment variables (nil = none set).
func FixtureEnv(t testing.TB, root string, getenv map[string]string) adapters.Env {
	t.Helper()
	return adapters.Env{
		Home:         root + string(os.PathSeparator) + "home",
		AppData:      root + string(os.PathSeparator) + "appdata",
		LocalAppData: root + string(os.PathSeparator) + "localappdata",
		Documents:    root + string(os.PathSeparator) + "documents",
		TempDir:      t.TempDir(),
		Getenv:       func(k string) string { return getenv[k] },
	}
}

// ReadAll runs Sources + Read over every source and returns all sessions
// sorted by (Agent, NativeID), failing the test on any error.
func ReadAll(t testing.TB, a adapters.Adapter, env adapters.Env) ([]model.Session, int) {
	t.Helper()
	srcs, err := a.Sources(env)
	if err != nil {
		t.Fatalf("%s Sources: %v", a.ID(), err)
	}
	var out []model.Session
	skipped := 0
	for _, s := range srcs {
		res, err := a.Read(context.Background(), env, s)
		if err != nil {
			t.Fatalf("%s Read %s: %v", a.ID(), s.Path, err)
		}
		skipped += res.Skipped
		out = append(out, res.Sessions...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].NativeID < out[j].NativeID
	})
	return out, skipped
}

// RealData runs the adapter against the current user's real data when
// ATHAR_REALDATA=1 and logs STATISTICS ONLY (never message text or titles).
// Use it from a TestRealData function in each adapter package.
func RealData(t *testing.T, a adapters.Adapter) {
	if os.Getenv("ATHAR_REALDATA") != "1" {
		t.Skip("set ATHAR_REALDATA=1 to run against the real data on this machine")
	}
	env := adapters.CurrentEnv(t.TempDir())
	srcs, err := a.Sources(env)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	cov := map[model.Coverage]int{}
	var sessions, msgs, skipped, noCwd, noTitle, errs int
	var first, last time.Time
	start := time.Now()
	for _, s := range srcs {
		res, err := a.Read(context.Background(), env, s)
		if err != nil {
			errs++
			t.Logf("ERROR %s: %v", s.Path, err)
			continue
		}
		skipped += res.Skipped
		for _, w := range res.Warnings {
			t.Logf("warn %s: %s", s.Path, w)
		}
		for _, se := range res.Sessions {
			sessions++
			msgs += len(se.Messages)
			cov[se.Coverage]++
			if se.Cwd == "" {
				noCwd++
			}
			if strings.TrimSpace(se.Title) == "" {
				noTitle++
			}
			if !se.UpdatedAt.IsZero() && se.UpdatedAt.After(last) {
				last = se.UpdatedAt
			}
			if !se.StartedAt.IsZero() && (first.IsZero() || se.StartedAt.Before(first)) {
				first = se.StartedAt
			}
		}
	}
	t.Logf("%s: sources=%d errors=%d sessions=%d messages=%d skipped=%d noCwd=%d noTitle=%d range=%s..%s coverage=%v took=%v",
		a.ID(), len(srcs), errs, sessions, msgs, skipped, noCwd, noTitle,
		first.Format("2006-01-02"), last.Format("2006-01-02"), cov, time.Since(start).Round(time.Millisecond))
}
