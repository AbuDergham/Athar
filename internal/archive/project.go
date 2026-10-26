package archive

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// NoProject is the project name of a session without a Cwd (spec §3.4).
// It is shown but never indexed for search.
const NoProject = "(no project)"

// projects computes ProjectPath and ProjectName (spec §3.4) and caches the
// git-root lookup per folder, so each folder is probed at most once per
// process (a folder on a disconnected drive can be slow to stat).
type projects struct {
	mu   sync.Mutex
	root map[string]string // folder -> git root ("" = none)
}

func newProjects() *projects { return &projects{root: map[string]string{}} }

// of returns the project of a Cwd: the git root (the nearest folder, walking
// up to the volume root, that contains a .git file or folder; git itself is
// never run), else the Cwd; and its name: "~" for the home folder, "D:" for a
// drive root, the base name otherwise, NoProject when cwd is empty.
// UNC paths are not probed (network shares can hang); the Cwd is used as is.
func (p *projects) of(cwd, home string) (path, name string) {
	if strings.TrimSpace(cwd) == "" {
		return "", NoProject
	}
	path = cleanPath(cwd)
	if root := p.gitRoot(path); root != "" {
		path = root
	}
	return path, projectName(path, home)
}

func (p *projects) gitRoot(dir string) string {
	if !probeable(dir) {
		return ""
	}
	p.mu.Lock()
	root, ok := p.root[dir]
	p.mu.Unlock()
	if ok {
		return root
	}
	for d := dir; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			root = d
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	p.mu.Lock()
	p.root[dir] = root
	p.mu.Unlock()
	return root
}

// probeable reports whether gitRoot may look for .git above dir: absolute
// drive-letter paths on Windows (UNC, \\?\, \??\ and device paths can stall
// on the network and are not probed), any absolute path elsewhere.
func probeable(dir string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	if filepath.Separator != '\\' {
		return true
	}
	vol := filepath.VolumeName(dir)
	return len(vol) == 2 && vol[1] == ':' && !strings.HasPrefix(dir, `\\`)
}

// cleanPath is filepath.Clean, except that a bare volume ("D:", which Clean
// turns into "D:.") becomes its root folder.
func cleanPath(p string) string {
	c := filepath.Clean(p)
	if vol := filepath.VolumeName(c); vol != "" && c[len(vol):] == "." {
		return vol + string(filepath.Separator)
	}
	return c
}

// projectName names a project folder (spec §3.4).
func projectName(path, home string) string {
	clean := cleanPath(path)
	if home != "" && sameFolder(clean, cleanPath(home)) {
		return "~"
	}
	if vol := filepath.VolumeName(clean); vol != "" {
		if rest := clean[len(vol):]; rest == "" || rest == `\` || rest == "/" {
			return vol
		}
	}
	if base := filepath.Base(clean); base != "." && base != string(filepath.Separator) {
		return base
	}
	return clean
}

// sameFolder compares folder paths the way the file system does
// (case-insensitively on Windows).
func sameFolder(a, b string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}
