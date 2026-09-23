package launch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPreflight(t *testing.T) {
	dir := t.TempDir()
	fakeResolver(t, []string{"claude"}, nil)

	if err := Preflight(Command{Dir: dir, Program: "claude"}); err != nil {
		t.Errorf("ok command: %v", err)
	}
	gone := filepath.Join(dir, "gone")
	err := Preflight(Command{Dir: gone, Program: "claude"})
	if err == nil || err.Error() != "Folder no longer exists: "+gone {
		t.Errorf("missing folder: %v", err)
	}
	file := filepath.Join(dir, "file.txt")
	touch(t, file)
	if err := Preflight(Command{Dir: file, Program: "claude"}); err == nil || !strings.HasPrefix(err.Error(), "Folder no longer exists") {
		t.Errorf("file as folder: %v", err)
	}
	err = Preflight(Command{Dir: dir, Program: "codex"})
	if err == nil || err.Error() != "`codex` is not installed or not on PATH" {
		t.Errorf("missing program: %v", err)
	}
	err = Preflight(Command{Dir: dir, Program: filepath.Join(dir, "opencode", "opencode-cli.exe")})
	if err == nil || err.Error() != "`opencode-cli` is not installed or not on PATH" {
		t.Errorf("missing program path: %v", err)
	}
	exe := filepath.Join(dir, "bin", "tool.exe")
	touch(t, exe)
	if err := Preflight(Command{Dir: dir, Program: exe, IsApp: true}); err != nil {
		t.Errorf("existing program path: %v", err)
	}

	if runtime.GOOS == "windows" {
		drive := ""
		for c := 'Z'; c >= 'D'; c-- {
			if _, err := os.Stat(string(c) + `:\`); err != nil {
				drive = string(c) + ":"
				break
			}
		}
		if drive == "" {
			t.Skip("no free drive letter")
		}
		err := Preflight(Command{Dir: drive + `\Projects\x`, Program: "claude"})
		if err == nil || err.Error() != "Drive "+drive+" is not connected" {
			t.Errorf("missing drive: %v", err)
		}
	}
}
