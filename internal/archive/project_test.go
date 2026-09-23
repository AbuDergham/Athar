package archive

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProjectOf(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "Users", "someone")
	repo := filepath.Join(base, "work", "TelegramVideos")
	sub := filepath.Join(repo, "cmd", "bot")
	plain := filepath.Join(base, "scratch", "notes")
	worktree := filepath.Join(base, "work", "wt")
	for _, d := range []string{filepath.Join(repo, ".git"), sub, plain, home, worktree} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A worktree has a .git FILE, not a folder.
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, cwd, wantPath, wantName string
	}{
		{"git root itself", repo, repo, "TelegramVideos"},
		{"inside a repo", sub, repo, "TelegramVideos"},
		{"worktree .git file", worktree, worktree, "wt"},
		{"no repo", plain, plain, "notes"},
		{"folder no longer exists", filepath.Join(base, "gone", "Proj"), filepath.Join(base, "gone", "Proj"), "Proj"},
		{"home", home, home, "~"},
		{"empty cwd", "", "", NoProject},
		{"blank cwd", "   ", "", NoProject},
		{"trailing separator", plain + string(filepath.Separator), plain, "notes"},
	}
	p := newProjects()
	for _, c := range cases {
		gotPath, gotName := p.of(c.cwd, home)
		if gotPath != c.wantPath || gotName != c.wantName {
			t.Errorf("%s: projectOf(%q) = (%q, %q), want (%q, %q)", c.name, c.cwd, gotPath, gotName, c.wantPath, c.wantName)
		}
	}
}

func TestProjectNameDriveRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive letters are Windows-only")
	}
	for _, c := range []struct{ path, want string }{
		{`D:\`, "D:"}, {`D:`, "D:"}, {`d:\`, "d:"},
		{`C:\Users\Someone`, "~"}, {`c:\users\someone\`, "~"}, {`C:\Users\Someone\x`, "x"},
	} {
		if got := projectName(c.path, `C:\Users\Someone`); got != c.want {
			t.Errorf("projectName(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
