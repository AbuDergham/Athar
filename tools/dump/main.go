// Command dump runs adapters against the current user's real data and prints
// STATISTICS ONLY (never message text): sources, sessions, coverage, message
// counts, date ranges and title lengths. It is a development aid for adapter
// authors; it is read-only like the adapters themselves.
//
//	go run ./tools/dump            # all adapters
//	go run ./tools/dump claude     # one adapter
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/model"
	"github.com/AbuDergham/Athar/internal/registry"
)

func main() {
	tmp, err := os.MkdirTemp("", "athar-dump-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)
	env := adapters.CurrentEnv(tmp)

	want := map[string]bool{}
	for _, a := range os.Args[1:] {
		want[a] = true
	}
	for _, a := range registry.All() {
		if len(want) > 0 && !want[a.ID()] {
			continue
		}
		dump(a, env)
	}
}

func dump(a adapters.Adapter, env adapters.Env) {
	srcs, err := a.Sources(env)
	fmt.Printf("== %s (%s) v%d: %d sources", a.ID(), a.Name(), a.Version(), len(srcs))
	if err != nil {
		fmt.Printf("  SOURCES ERROR: %v", err)
	}
	fmt.Println()
	cov := map[model.Coverage]int{}
	var sessions, msgs, skipped, noCwd, noTitle int
	var first, last time.Time
	for _, s := range srcs {
		start := time.Now()
		res, err := a.Read(context.Background(), env, s)
		if err != nil {
			fmt.Printf("   ERROR %s: %v\n", s.Path, err)
			continue
		}
		skipped += res.Skipped
		for _, w := range res.Warnings {
			fmt.Printf("   warn %s: %s\n", s.Path, w)
		}
		for _, se := range res.Sessions {
			sessions++
			msgs += len(se.Messages)
			cov[se.Coverage]++
			if se.Cwd == "" {
				noCwd++
			}
			if se.Title == "" {
				noTitle++
			}
			if !se.UpdatedAt.IsZero() && (last.IsZero() || se.UpdatedAt.After(last)) {
				last = se.UpdatedAt
			}
			if !se.StartedAt.IsZero() && (first.IsZero() || se.StartedAt.Before(first)) {
				first = se.StartedAt
			}
		}
		if d := time.Since(start); d > 2*time.Second {
			fmt.Printf("   slow %s: %v\n", s.Path, d.Round(time.Millisecond))
		}
	}
	keys := make([]string, 0, len(cov))
	for k := range cov {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	fmt.Printf("   sessions=%d messages=%d skipped=%d noCwd=%d noTitle=%d range=%s..%s coverage=",
		sessions, msgs, skipped, noCwd, noTitle, first.Format("2006-01-02"), last.Format("2006-01-02"))
	for _, k := range keys {
		fmt.Printf("%s:%d ", k, cov[model.Coverage(k)])
	}
	fmt.Println()
}
