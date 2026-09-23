package qwen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// Qwen files its prompt log (and 0.0.14-0.3 sessions) under tmp/<hash>,
// where hash = sha256 of the launch folder: the exact path in old versions,
// the lowercased path on Windows in newer ones. The hash cannot be reversed,
// so resolveHash tries the folders Athar can think of:
//
//  1. every cwd recorded by a 0.4+ transcript of the same root (exact);
//  2. folders under the home folder, at most walkDepth levels deep, skipping
//     hidden folders, AppData and node_modules, listing at most walkBudget
//     folders (the owner's legacy folders are all within 3 levels, ~50 ms).
//
// An unresolved hash leaves Cwd empty ("(no project)").
const (
	walkDepth  = 3
	walkBudget = 20000
	maxCwdScan = 10 // records read per transcript looking for a cwd
)

var (
	hashRE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	skipDirs = map[string]bool{
		"appdata": true, "node_modules": true, "application data": true,
		"local settings": true, "system volume information": true,
	}
	errStop = errors.New("stop")
)

// Lookup caches, guarded by cacheMu (sources may be read concurrently). Both
// hold only successful lookups, which never change: a project folder's name is
// derived from its cwd, and a hash names exactly one path. Without projCwds a
// first sync would re-read every project's transcript once per prompt log.
var (
	cacheMu  sync.Mutex
	projCwds = map[string]string{} // projects\<enc> folder → recorded cwd
	hashDirs = map[string]string{} // tmp\<hash> name → resolved folder
)

func cached(m map[string]string, key string) (string, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	v, ok := m[key]
	return v, ok
}

func remember(m map[string]string, key, value string) {
	if value == "" {
		return
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	m[key] = value
}

// hashVariants returns the sha256 hex of p as Qwen may have hashed it:
// verbatim, lowercased, and with only the drive letter lowercased.
func hashVariants(p string) []string {
	variants := []string{p, strings.ToLower(p)}
	if len(p) >= 2 && p[1] == ':' {
		variants = append(variants, strings.ToLower(p[:1])+p[1:])
	}
	var out []string
	seen := map[string]bool{}
	for _, v := range variants {
		if seen[v] {
			continue
		}
		seen[v] = true
		sum := sha256.Sum256([]byte(v))
		out = append(out, hex.EncodeToString(sum[:]))
	}
	return out
}

func matchesHash(p, hash string) bool {
	for _, h := range hashVariants(p) {
		if h == hash {
			return true
		}
	}
	return false
}

// resolveHash returns the folder whose hash names tmp/<hash>, or "".
func resolveHash(env adapters.Env, base, hash string) string {
	hash = strings.ToLower(hash)
	if !hashRE.MatchString(hash) {
		return ""
	}
	if dir, ok := cached(hashDirs, hash); ok {
		return dir
	}
	dir := ""
	for _, cwd := range knownCwds(base) {
		if matchesHash(cwd, hash) {
			dir = cwd
			break
		}
	}
	if dir == "" {
		dir = walkFind(env.Home, hash)
	}
	remember(hashDirs, hash, dir)
	return dir
}

// knownCwds returns one recorded cwd per project folder of a Qwen root.
func knownCwds(base string) []string {
	var out []string
	projects := filepath.Join(base, "projects")
	for _, pd := range subdirs(projects) {
		if cwd := projectCwd(filepath.Join(projects, pd)); cwd != "" {
			out = append(out, cwd)
		}
	}
	return out
}

// projectCwd returns the cwd recorded by the first readable transcript of a
// project folder.
func projectCwd(project string) string {
	if cwd, ok := cached(projCwds, project); ok {
		return cwd
	}
	chats := filepath.Join(project, "chats")
	for _, dir := range []string{chats, filepath.Join(chats, "archive")} {
		for _, f := range matchingFiles(dir, chatFileRE) {
			if cwd := firstCwd(f); cwd != "" {
				remember(projCwds, project, cwd)
				return cwd
			}
		}
	}
	return ""
}

func firstCwd(path string) string {
	cwd, n := "", 0
	_ = fsread.Lines(path, func(line []byte) error {
		n++
		var r struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(line, &r) == nil && r.Cwd != "" {
			cwd = r.Cwd
			return errStop
		}
		if n >= maxCwdScan {
			return errStop
		}
		return nil
	})
	return cwd
}

// walkFind searches root breadth-first for a folder matching hash.
func walkFind(root, hash string) string {
	if root == "" {
		return ""
	}
	type item struct {
		dir   string
		depth int
	}
	queue := []item{{filepath.Clean(root), 0}}
	listed := 0
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if matchesHash(it.dir, hash) {
			return it.dir
		}
		if it.depth >= walkDepth || listed >= walkBudget {
			continue
		}
		listed++
		entries, err := os.ReadDir(it.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || e.Type()&os.ModeSymlink != 0 ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") || skipDirs[strings.ToLower(name)] {
				continue
			}
			queue = append(queue, item{filepath.Join(it.dir, name), it.depth + 1})
		}
	}
	return ""
}
