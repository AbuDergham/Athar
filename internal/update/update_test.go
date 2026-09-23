package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// useAPI points apiBaseURL at srv for the duration of the test.
func useAPI(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = old })
}

// useExe points executablePath at path for the duration of the test.
func useExe(t *testing.T, path string) {
	t.Helper()
	old := executablePath
	executablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { executablePath = old })
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in     string
		want   version
		wantOK bool
	}{
		{"v1.2.3", version{1, 2, 3, ""}, true},
		{"1.2.3", version{1, 2, 3, ""}, true},
		{" v1.2.3 ", version{1, 2, 3, ""}, true},
		{"v10.20.30", version{10, 20, 30, ""}, true},
		{"v1.2.3-rc.1", version{1, 2, 3, "rc.1"}, true},
		{"v1.2.3+build.7", version{1, 2, 3, ""}, true},
		{"v1.2.3-rc.1+build.7", version{1, 2, 3, "rc.1"}, true},
		{"dev", version{}, false},
		{"", version{}, false},
		{"v", version{}, false},
		{"v1.2", version{}, false},
		{"v1.2.3.4", version{}, false},
		{"v1.x.3", version{}, false},
		{"v-1.2.3", version{}, false},
		{"v+1.2.3", version{}, false},
		{"v1..3", version{}, false},
		{"v1.2.3-", version{}, false},
		{"vv1.2.3", version{}, false},
		{"v99999999999999999999.0.0", version{}, false},
	}
	for _, tt := range tests {
		got, ok := parseVersion(tt.in)
		if ok != tt.wantOK || (ok && got != tt.want) {
			t.Errorf("parseVersion(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestNewerThan(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"v1.1.0", "v1.0.0", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v1.1.0", false},
		{"v2.0.0", "v1.9.9", true},
		{"v1.9.9", "v2.0.0", false},
		{"v1.10.0", "v1.9.0", true}, // numeric, not lexical
		{"v1.0.10", "v1.0.9", true},
		{"v1.0.1", "v1.0.0", true},
		{"v1.1.0", "1.0.0", true},
		{"v1.1.0", "v1.1.0-rc.1", false}, // pre-release tags are ignored
		{"v1.1.0-rc.1", "v1.1.0", false},
		{"v1.1.0+meta", "v1.1.0", false},
	}
	for _, tt := range tests {
		a, okA := parseVersion(tt.a)
		b, okB := parseVersion(tt.b)
		if !okA || !okB {
			t.Fatalf("bad test versions %q %q", tt.a, tt.b)
		}
		if got := a.newerThan(b); got != tt.want {
			t.Errorf("%s newerThan %s = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCheck(t *testing.T) {
	const latestPath = "/repos/AbuDergham/Athar/releases/latest"
	release := func(tag string) string {
		return `{"tag_name":"` + tag + `","name":"Athar ` + tag + `","body":"## Changes\n- faster search",` +
			`"html_url":"https://github.com/AbuDergham/Athar/releases/tag/` + tag + `","draft":false,"prerelease":false,"assets":[]}`
	}
	tests := []struct {
		name     string
		current  string
		status   int
		body     string
		want     *Release
		wantErr  bool
		wantHits int32
	}{
		{name: "newer", current: "v1.0.0", status: 200, body: release("v1.1.0"), wantHits: 1,
			want: &Release{Version: "v1.1.0", Notes: "## Changes\n- faster search", URL: "https://github.com/AbuDergham/Athar/releases/tag/v1.1.0"}},
		{name: "newer without v prefix on current", current: "1.0.0", status: 200, body: release("v1.0.1"), wantHits: 1,
			want: &Release{Version: "v1.0.1", Notes: "## Changes\n- faster search", URL: "https://github.com/AbuDergham/Athar/releases/tag/v1.0.1"}},
		{name: "equal", current: "v1.1.0", status: 200, body: release("v1.1.0"), wantHits: 1},
		{name: "older", current: "v1.2.0", status: 200, body: release("v1.1.0"), wantHits: 1},
		{name: "dev build never checks", current: "dev", status: 200, body: release("v9.0.0"), wantHits: 0},
		{name: "empty version never checks", current: "", status: 200, body: release("v9.0.0"), wantHits: 0},
		{name: "pre-release tag ignored", current: "v1.0.0", status: 200, body: release("v1.1.0-rc.1"), wantHits: 1},
		{name: "prerelease flag ignored", current: "v1.0.0", status: 200, wantHits: 1,
			body: `{"tag_name":"v1.1.0","body":"x","html_url":"u","prerelease":true}`},
		{name: "draft ignored", current: "v1.0.0", status: 200, wantHits: 1,
			body: `{"tag_name":"v1.1.0","body":"x","html_url":"u","draft":true}`},
		{name: "no releases yet", current: "v1.0.0", status: 404, body: `{"message":"Not Found"}`, wantHits: 1},
		{name: "server error", current: "v1.0.0", status: 500, body: `oops`, wantErr: true, wantHits: 1},
		{name: "rate limited", current: "v1.0.0", status: 403, body: `{"message":"API rate limit exceeded"}`, wantErr: true, wantHits: 1},
		{name: "unrecognized tag", current: "v1.0.0", status: 200, body: release("nightly"), wantErr: true, wantHits: 1},
		{name: "bad json", current: "v1.0.0", status: 200, body: `{"tag_name":`, wantErr: true, wantHits: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != latestPath {
					t.Errorf("request %s %s, want GET %s", r.Method, r.URL.Path, latestPath)
				}
				if ua, want := r.Header.Get("User-Agent"), "athar/"+tt.current; ua != want {
					t.Errorf("User-Agent = %q, want %q", ua, want)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			useAPI(t, srv)

			got, err := Check(context.Background(), tt.current)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Check error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.want == nil && got != nil {
				t.Fatalf("Check = %+v, want nil", got)
			}
			if tt.want != nil && (got == nil || *got != *tt.want) {
				t.Fatalf("Check = %+v, want %+v", got, tt.want)
			}
			if n := hits.Load(); n != tt.wantHits {
				t.Errorf("server hit %d times, want %d", n, tt.wantHits)
			}
		})
	}
}

func TestCheckTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	useAPI(t, srv)
	old := checkTimeout
	checkTimeout = 100 * time.Millisecond
	defer func() { checkTimeout = old }()

	start := time.Now()
	got, err := Check(context.Background(), "v1.0.0")
	if err == nil || got != nil {
		t.Fatalf("Check = %+v, %v; want timeout error", got, err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Check took %v, want about %v", d, checkTimeout)
	}
}

func TestCheckDefaults(t *testing.T) {
	if apiBaseURL != "https://api.github.com" {
		t.Errorf("apiBaseURL = %q", apiBaseURL)
	}
	if checkTimeout != 5*time.Second {
		t.Errorf("checkTimeout = %v, want 5s", checkTimeout)
	}
	if Repo != "AbuDergham/Athar" {
		t.Errorf("Repo = %q", Repo)
	}
}

func TestCleanupOld(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "athar.exe")
	useExe(t, exe)
	if err := os.WriteFile(exe, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe+".old", []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}

	CleanupOld()
	if _, err := os.Stat(exe + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".old still present: %v", err)
	}
	if b, err := os.ReadFile(exe); err != nil || string(b) != "current" {
		t.Fatalf("exe changed: %q, %v", b, err)
	}

	CleanupOld() // nothing to remove: no panic, no error surfaced

	executablePath = func() (string, error) { return "", errors.New("unknown") }
	CleanupOld() // unknown exe path: ignored
}
