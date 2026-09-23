package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"aead.dev/minisign"
)

const testTag = "v1.1.0"

// fakeRelease serves a GitHub release (JSON at /repos/<Repo>/releases/tags/<tag>)
// and its assets at /dl/<name>, and records every request path.
type fakeRelease struct {
	t      *testing.T
	srv    *httptest.Server
	assets map[string][]byte // name -> served bytes; order irrelevant
	omit   map[string]bool   // assets left out of the release JSON

	mu   sync.Mutex
	hits []string
}

func (f *fakeRelease) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.URL.Path)
	f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("User-Agent"), "athar") {
		f.t.Errorf("request %s without athar User-Agent", r.URL.Path)
	}
	if r.URL.Path == "/repos/"+Repo+"/releases/tags/"+testTag {
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int    `json:"size"`
		}
		rel := struct {
			Tag    string  `json:"tag_name"`
			Body   string  `json:"body"`
			Assets []asset `json:"assets"`
		}{Tag: testTag, Body: "notes"}
		for name, b := range f.assets {
			if !f.omit[name] {
				rel.Assets = append(rel.Assets, asset{name, f.srv.URL + "/dl/" + name, len(b)})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rel)
		return
	}
	if name, ok := strings.CutPrefix(r.URL.Path, "/dl/"); ok {
		if b, ok := f.assets[name]; ok {
			_, _ = w.Write(b)
			return
		}
	}
	http.NotFound(w, r)
}

func (f *fakeRelease) requested(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.hits {
		if h == path {
			return true
		}
	}
	return false
}

func (f *fakeRelease) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.hits)
}

// installFixture is a signed fake release plus a temp "installed" athar.exe.
type installFixture struct {
	rel     *fakeRelease
	priv    minisign.PrivateKey
	exe     string
	oldExe  []byte
	newExe  []byte
	asset   string
	sums    []byte
	release *Release
}

// newInstallFixture builds a correctly signed release for testTag, embeds its
// public key in PublicKey and points executablePath at a temp athar.exe.
func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	pub, priv := newKey(t)
	name, err := assetName(testTag)
	if err != nil {
		t.Fatal(err)
	}
	f := &installFixture{
		priv:    priv,
		exe:     filepath.Join(t.TempDir(), "athar.exe"),
		oldExe:  []byte("MZ old athar build"),
		newExe:  []byte("MZ new athar build " + strings.Repeat("x", 100_000)),
		asset:   name,
		release: &Release{Version: testTag, Notes: "notes", URL: "https://example.invalid/release"},
	}
	other := []byte("linux archive")
	f.sums = []byte(fmt.Sprintf("%x  %s\n%x  athar_1.1.0_linux_amd64.tar.gz\n",
		sha256.Sum256(f.newExe), name, sha256.Sum256(other)))
	f.rel = &fakeRelease{t: t, omit: map[string]bool{}, assets: map[string][]byte{
		name:                             f.newExe,
		"athar_1.1.0_linux_amd64.tar.gz": other,
		"checksums.txt":                  f.sums,
		"checksums.txt.minisig":          minisign.Sign(priv, f.sums),
	}}
	f.rel.srv = httptest.NewServer(f.rel)
	t.Cleanup(f.rel.srv.Close)
	useAPI(t, f.rel.srv)

	oldKey := PublicKey
	PublicKey = pub.String()
	t.Cleanup(func() { PublicKey = oldKey })

	if err := os.WriteFile(f.exe, f.oldExe, 0o755); err != nil {
		t.Fatal(err)
	}
	useExe(t, f.exe)
	return f
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	return string(b)
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s should not exist (stat err %v)", filepath.Base(path), err)
	}
}

// assertUntouched checks that a refused install left athar.exe as it was and
// created nothing next to it.
func (f *installFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if got := readFile(t, f.exe); got != string(f.oldExe) {
		t.Errorf("athar.exe changed to %d bytes", len(got))
	}
	assertMissing(t, f.exe+".old")
	assertMissing(t, f.exe+".new")
}

func TestAssetName(t *testing.T) {
	tests := []struct {
		version string
		want    string
		wantErr bool
	}{
		{"v1.1.0", "athar_1.1.0_windows_" + runtime.GOARCH + ".exe", false},
		{"1.2.3", "athar_1.2.3_windows_" + runtime.GOARCH + ".exe", false},
		{"v2.0.0-rc.1", "athar_2.0.0-rc.1_windows_" + runtime.GOARCH + ".exe", false},
		{"dev", "", true},
		{"", "", true},
		{"v1.1.0/../../x", "", true},
	}
	for _, tt := range tests {
		got, err := assetName(tt.version)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("assetName(%q) = %q, %v; want %q, err %v", tt.version, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestInstallReplacesExe(t *testing.T) {
	f := newInstallFixture(t)
	if err := Install(context.Background(), f.release); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.exe); got != string(f.newExe) {
		t.Errorf("athar.exe has %d bytes, want the new build (%d bytes)", len(got), len(f.newExe))
	}
	if got := readFile(t, f.exe+".old"); got != string(f.oldExe) {
		t.Errorf("athar.exe.old = %q, want the previous build", got)
	}
	assertMissing(t, f.exe+".new")
	if f.rel.requested("/dl/athar_1.1.0_linux_amd64.tar.gz") {
		t.Error("downloaded an asset for another OS")
	}

	CleanupOld()
	assertMissing(t, f.exe+".old")
}

func TestInstallReplacesPreviousOld(t *testing.T) {
	f := newInstallFixture(t)
	if err := os.WriteFile(f.exe+".old", []byte("even older build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), f.release); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.exe+".old"); got != string(f.oldExe) {
		t.Errorf("athar.exe.old = %q, want the build that was running", got)
	}
	if got := readFile(t, f.exe); got != string(f.newExe) {
		t.Error("athar.exe is not the new build")
	}
}

func TestInstallRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *installFixture)
		want  error // nil: any error
		// assetFetched reports whether the exe asset may have been downloaded.
		assetFetched bool
	}{
		{
			name: "signature by another key",
			setup: func(t *testing.T, f *installFixture) {
				_, other := newKey(t)
				f.rel.assets["checksums.txt.minisig"] = minisign.Sign(other, f.sums)
			},
			want: ErrBadSignature,
		},
		{
			name: "checksums changed after signing",
			setup: func(t *testing.T, f *installFixture) {
				forged := sha256.Sum256([]byte("evil"))
				f.rel.assets["checksums.txt"] = []byte(fmt.Sprintf("%x  %s\n", forged, f.asset))
			},
			want: ErrBadSignature,
		},
		{
			name: "garbage signature",
			setup: func(t *testing.T, f *installFixture) {
				f.rel.assets["checksums.txt.minisig"] = []byte("not a signature")
			},
			want: ErrBadSignature,
		},
		{
			name:         "asset does not match signed checksum",
			setup:        func(t *testing.T, f *installFixture) { f.rel.assets[f.asset] = []byte("MZ tampered build") },
			want:         ErrChecksumMismatch,
			assetFetched: true,
		},
		{
			name:         "truncated asset",
			setup:        func(t *testing.T, f *installFixture) { f.rel.assets[f.asset] = f.newExe[:len(f.newExe)/2] },
			want:         ErrChecksumMismatch,
			assetFetched: true,
		},
		{
			name:  "asset missing from release",
			setup: func(t *testing.T, f *installFixture) { f.rel.omit[f.asset] = true },
		},
		{
			name:  "signature missing from release",
			setup: func(t *testing.T, f *installFixture) { f.rel.omit["checksums.txt.minisig"] = true },
		},
		{
			name:  "checksums missing from release",
			setup: func(t *testing.T, f *installFixture) { f.rel.omit["checksums.txt"] = true },
		},
		{
			name: "asset not listed in signed checksums",
			setup: func(t *testing.T, f *installFixture) {
				sums := []byte("0000000000000000000000000000000000000000000000000000000000000000  other.exe\n")
				f.rel.assets["checksums.txt"] = sums
				f.rel.assets["checksums.txt.minisig"] = minisign.Sign(f.priv, sums)
			},
		},
		{
			name:  "release for another tag",
			setup: func(t *testing.T, f *installFixture) { f.release = &Release{Version: "v1.2.0"} },
		},
		{
			name:  "unparsable version",
			setup: func(t *testing.T, f *installFixture) { f.release = &Release{Version: "dev"} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstallFixture(t)
			tt.setup(t, f)
			err := Install(context.Background(), f.release)
			if err == nil {
				t.Fatal("Install succeeded, want refusal")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if !tt.assetFetched && f.rel.requested("/dl/"+f.asset) {
				t.Error("exe asset downloaded before verification succeeded")
			}
			f.assertUntouched(t)
		})
	}
}

func TestInstallRefusesWithoutPublicKey(t *testing.T) {
	for _, key := range []string{"", "  "} {
		f := newInstallFixture(t)
		PublicKey = key
		if err := Install(context.Background(), f.release); !errors.Is(err, ErrNoPublicKey) {
			t.Fatalf("PublicKey %q: err = %v, want ErrNoPublicKey", key, err)
		}
		if n := f.rel.hitCount(); n != 0 {
			t.Errorf("PublicKey %q: %d network requests, want none", key, n)
		}
		f.assertUntouched(t)
	}
}

func TestInstallNilRelease(t *testing.T) {
	f := newInstallFixture(t)
	if err := Install(context.Background(), nil); err == nil {
		t.Fatal("Install(nil) succeeded")
	}
	f.assertUntouched(t)
}

func TestInstallRollsBackWhenFinalRenameFails(t *testing.T) {
	f := newInstallFixture(t)
	old := rename
	rename = func(from, to string) error {
		if from == f.exe+".new" && to == f.exe {
			return errors.New("injected failure")
		}
		return old(from, to)
	}
	defer func() { rename = old }()

	err := Install(context.Background(), f.release)
	if err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	f.assertUntouched(t)
}

func TestInstallCanceled(t *testing.T) {
	f := newInstallFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Install(ctx, f.release); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	f.assertUntouched(t)
}

// TestHelperProcess is not a real test: TestInstallReplacesRunningExe runs a
// copy of the test binary with ATHAR_UPDATE_HELPER=1 so that a running
// athar.exe can be replaced.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("ATHAR_UPDATE_HELPER") != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

func TestInstallReplacesRunningExe(t *testing.T) {
	if testing.Short() {
		t.Skip("copies and runs the test binary")
	}
	f := newInstallFixture(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Create(f.exe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	src.Close()
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	running, err := os.ReadFile(f.exe)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(f.exe, "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "ATHAR_UPDATE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	defer stop()

	if err := Install(context.Background(), f.release); err != nil {
		t.Fatalf("Install while athar.exe is running: %v", err)
	}
	if got := readFile(t, f.exe); got != string(f.newExe) {
		t.Error("athar.exe is not the new build")
	}
	if got := readFile(t, f.exe+".old"); got != string(running) {
		t.Error("athar.exe.old is not the running build")
	}

	CleanupOld() // the old image may still be in use: must not fail loudly
	stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		CleanupOld()
		if _, err := os.Stat(f.exe + ".old"); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("athar.exe.old not removed after the old process exited")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
