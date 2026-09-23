package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnderAny(t *testing.T) {
	roots := []string{`C:\Users\u\AppData\Local\Temp`, `D:\Scratch\`}
	cases := []struct {
		dir  string
		want bool
	}{
		{`C:\Users\u\AppData\Local\Temp\Temp1_athar.zip`, true},
		{`c:\users\U\appdata\LOCAL\temp\7zS1234`, true},
		{`C:\Users\u\AppData\Local\Temp`, true},
		{`C:\Users\u\AppData\Local\Temp2\x`, false},
		{`C:\Users\u\AppData\Local`, false},
		{`D:\Scratch\a\b`, true},
		{`D:\ScratchPad`, false},
		{`D:\Tools\Athar`, false},
	}
	for _, c := range cases {
		if got := underAny(c.dir, roots); got != c.want {
			t.Errorf("underAny(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
}

func TestTempRoots(t *testing.T) {
	env := map[string]string{
		"TEMP":         `C:\T1`,
		"TMP":          `C:\T2\`,
		"LOCALAPPDATA": `C:\Users\u\AppData\Local`,
	}
	sys := &system{getenv: func(k string) string { return env[k] }, tempDir: func() string { return `C:\T0` }}
	got := strings.Join(tempRoots(sys), "|")
	for _, want := range []string{`C:\T0`, `C:\T1`, `C:\T2`, filepath.Join(`C:\Users\u\AppData\Local`, "Temp")} {
		if !strings.Contains(got, want) {
			t.Errorf("tempRoots = %s, missing %s", got, want)
		}
	}

	// A drive root as TEMP would make every folder of the drive "temporary": ignored.
	env = map[string]string{"TEMP": `D:\`}
	sys = &system{getenv: func(k string) string { return env[k] }, tempDir: func() string { return "" }}
	if got := tempRoots(sys); len(got) != 0 {
		t.Errorf("tempRoots with TEMP=D:\\ = %q, want none", got)
	}
}

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
	sys := &system{getenv: func(k string) string {
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
	sys := &system{getenv: func(string) string { return temp }, tempDir: func() string { return temp }}
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
