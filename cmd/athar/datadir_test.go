package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudFolder(t *testing.T) {
	cases := map[string]string{
		`C:\Users\u\OneDrive\Tools\Athar\athar-data`:     "OneDrive",
		`C:\Users\u\OneDrive - Contoso\Athar\athar-data`: "OneDrive",
		`C:\Users\u\Dropbox\athar-data`:                  "Dropbox",
		`G:\Google Drive\athar-data`:                     "Google Drive",
		`C:\Users\u\iCloudDrive\athar-data`:              "iCloudDrive",
		`c:\users\u\onedrive\x`:                          "OneDrive",
		`D:\Tools\Athar\athar-data`:                      "",
		`D:\Tools\drive\athar-data`:                      "",
	}
	for dir, want := range cases {
		if got := cloudFolder(dir); got != want {
			t.Errorf("cloudFolder(%q) = %q, want %q", dir, got, want)
		}
	}
}

// fakeTempSys returns a system whose temporary folders are somewhere the
// tests never use (the tests themselves run under the real %TEMP%).
func fakeTempSys() *system {
	return &system{
		goos:    "windows",
		getenv:  func(string) string { return "" },
		tempDir: func() string { return `Z:\athar-test-temp` },
	}
}

func TestResolveDataDirDefault(t *testing.T) {
	exeDir := filepath.Join(t.TempDir(), "Tools", "Athar")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveDataDir("", exeDir, fakeTempSys())
	if err != nil {
		t.Fatalf("resolveDataDir: %v", err)
	}
	want := filepath.Join(exeDir, dataDirName)
	if got != want {
		t.Fatalf("resolveDataDir = %q, want %q", got, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("data dir not created: %v", err)
	}
	assertNoProbe(t, exeDir)
	assertNoProbe(t, want)
}

func TestResolveDataDirRefusesTemp(t *testing.T) {
	temp := t.TempDir()
	exeDir := filepath.Join(temp, "Temp1_athar.zip")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sys := &system{goos: "windows", getenv: func(k string) string {
		if k == "TEMP" {
			return temp
		}
		return ""
	}, tempDir: func() string { return `Z:\elsewhere` }}
	_, err := resolveDataDir("", exeDir, sys)
	var ref *refusal
	if !errors.As(err, &ref) {
		t.Fatalf("resolveDataDir under TEMP: err = %v, want a refusal", err)
	}
	msg := err.Error()
	for _, want := range []string{"Athar keeps its archive next to athar.exe", exeDir, "temporary", "--data-dir <path>", `D:\Tools\Athar`} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not contain %q", msg, want)
		}
	}
	if _, err := os.Stat(filepath.Join(exeDir, dataDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("athar-data was created in a temporary folder (stat err %v)", err)
	}
}

func TestResolveDataDirRefusesNotWritable(t *testing.T) {
	// A regular file where the folder should be: nothing can be created in it.
	exeDir := filepath.Join(t.TempDir(), "readonly")
	if err := os.WriteFile(exeDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveDataDir("", exeDir, fakeTempSys())
	var ref *refusal
	if !errors.As(err, &ref) {
		t.Fatalf("resolveDataDir(not writable): err = %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "is not writable") || !strings.Contains(err.Error(), exeDir) {
		t.Errorf("refusal = %q", err)
	}
}

func TestResolveDataDirFlag(t *testing.T) {
	// --data-dir is the way out: allowed even under %TEMP%, created when missing.
	temp := t.TempDir()
	dir := filepath.Join(temp, "data")
	sys := &system{goos: "windows", getenv: func(string) string { return temp }, tempDir: func() string { return temp }}
	got, err := resolveDataDir(dir, filepath.Join(temp, "exe"), sys)
	if err != nil {
		t.Fatalf("resolveDataDir(--data-dir): %v", err)
	}
	if got != dir {
		t.Fatalf("resolveDataDir = %q, want %q", got, dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("--data-dir not created: %v", err)
	}
	assertNoProbe(t, dir)

	// A relative --data-dir is made absolute.
	rel, err := resolveDataDir(".", `Z:\exe`, fakeTempSys())
	if err != nil || !filepath.IsAbs(rel) {
		t.Fatalf("resolveDataDir(.) = %q, %v; want an absolute path", rel, err)
	}

	// A --data-dir that cannot be used is an error naming it (not the §2.1 refusal).
	file := filepath.Join(temp, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = resolveDataDir(file, `Z:\exe`, fakeTempSys())
	var ref *refusal
	if err == nil || errors.As(err, &ref) || !strings.Contains(err.Error(), file) {
		t.Fatalf("resolveDataDir(file) err = %v, want an error naming %s", err, file)
	}
}

// assertNoProbe checks that the writability probe was removed.
func assertNoProbe(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), probePrefix) {
			t.Errorf("probe file left behind: %s", filepath.Join(dir, e.Name()))
		}
	}
}

// unixSys is a Linux or macOS system whose home folder is home.
func unixSys(goos, home string, env map[string]string) *system {
	return &system{
		goos:    goos,
		getenv:  func(k string) string { return env[k] },
		homeDir: func() (string, error) { return home, nil },
		tempDir: func() string { return "/athar-test-temp" },
	}
}

func TestResolveDataDirUnix(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	exeDir := filepath.Join(base, "usr", "local", "bin")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	xdg := filepath.Join(base, "xdg-data")
	cases := []struct {
		name, goos string
		env        map[string]string
		want       string
	}{
		{"linux", "linux", nil, filepath.Join(home, ".local", "share", "athar")},
		{"linux XDG_DATA_HOME", "linux", map[string]string{"XDG_DATA_HOME": xdg}, filepath.Join(xdg, "athar")},
		{"linux relative XDG_DATA_HOME is ignored", "linux", map[string]string{"XDG_DATA_HOME": "data"}, filepath.Join(home, ".local", "share", "athar")},
		{"macOS", "darwin", map[string]string{"XDG_DATA_HOME": xdg}, filepath.Join(home, "Library", "Application Support", "Athar")},
	}
	for _, c := range cases {
		got, err := resolveDataDir("", exeDir, unixSys(c.goos, home, c.env))
		if err != nil || got != c.want {
			t.Errorf("%s: resolveDataDir = %q, %v; want %q", c.name, got, err, c.want)
			continue
		}
		if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
			t.Errorf("%s: data dir not created: %v", c.name, err)
		}
		assertNoProbe(t, got)
	}
	// The binary's folder is never written into.
	if entries, _ := os.ReadDir(exeDir); len(entries) != 0 {
		t.Errorf("files created next to the binary: %v", entries)
	}

	// A portable athar-data next to the binary wins.
	portable := filepath.Join(exeDir, dataDirName)
	if err := os.Mkdir(portable, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveDataDir("", exeDir, unixSys("linux", home, nil)); err != nil || got != portable {
		t.Errorf("portable: resolveDataDir = %q, %v; want %q", got, err, portable)
	}
	// --data-dir wins over both.
	flag := filepath.Join(base, "flag")
	if got, err := resolveDataDir(flag, exeDir, unixSys("darwin", home, nil)); err != nil || got != flag {
		t.Errorf("--data-dir: resolveDataDir = %q, %v; want %q", got, err, flag)
	}
}

func TestResolveDataDirUnixErrors(t *testing.T) {
	base := t.TempDir()
	exeDir := filepath.Join(base, "bin")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No home folder and no XDG_DATA_HOME: an error pointing at --data-dir.
	sys := unixSys("linux", "", nil)
	if _, err := resolveDataDir("", exeDir, sys); err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Errorf("no home: err = %v", err)
	}
	sys.homeDir = func() (string, error) { return "", errors.New("$HOME is not defined") }
	if _, err := resolveDataDir("", exeDir, sys); err == nil || !strings.Contains(err.Error(), "$HOME is not defined") {
		t.Errorf("home error: err = %v", err)
	}
	// The user data folder is a file.
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(home, ".local", "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local", "share", "athar"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDataDir("", exeDir, unixSys("linux", home, nil)); err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Errorf("data folder is a file: err = %v", err)
	}
}
