package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
)

// sessionFile matches the stem of a top-level transcript: the session UUID.
var sessionFile = regexp.MustCompile(`^[0-9a-f-]{36}$`)

// root is one Claude data folder. id is "" for the default %USERPROFILE%\.claude,
// otherwise the folder's path (what launch puts in CLAUDE_CONFIG_DIR).
type root struct {
	dir string
	id  string
}

// Sources implements adapters.Adapter. Missing folders are skipped silently.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	for _, r := range roots(env) {
		out = append(out, rootSources(r)...)
	}
	return out, nil
}

// roots lists the Claude roots: the default one, CLAUDE_CONFIG_DIR, then every
// %USERPROFILE%\.claude* folder that contains projects\. Each folder appears once.
func roots(env adapters.Env) []root {
	var out []root
	seen := map[string]bool{}
	add := func(dir, id string) {
		if dir == "" || seen[pathKey(dir)] || !isDir(dir) {
			return
		}
		seen[pathKey(dir)] = true
		out = append(out, root{dir: dir, id: id})
	}

	def := ""
	if env.Home != "" {
		def = filepath.Join(env.Home, ".claude")
		add(def, "")
	}
	if env.Getenv != nil {
		if v := strings.TrimSpace(env.Getenv("CLAUDE_CONFIG_DIR")); v != "" {
			if abs, err := filepath.Abs(v); err == nil {
				v = abs
			}
			v = filepath.Clean(v)
			if def != "" && pathKey(v) == pathKey(def) {
				add(def, "")
			} else {
				add(v, v)
			}
		}
	}
	if env.Home != "" {
		entries, _ := os.ReadDir(env.Home)
		for _, e := range entries {
			if !strings.HasPrefix(strings.ToLower(e.Name()), ".claude") {
				continue
			}
			dir := filepath.Join(env.Home, e.Name())
			if isDir(filepath.Join(dir, "projects")) {
				add(dir, dir)
			}
		}
	}
	return out
}

// rootSources lists one root's transcripts and its prompt log.
func rootSources(r root) []adapters.Source {
	var out []adapters.Source
	projects := filepath.Join(r.dir, "projects")
	projectDirs, _ := os.ReadDir(projects)
	for _, pd := range projectDirs {
		pdir := filepath.Join(projects, pd.Name())
		if !isDir(pdir) {
			continue
		}
		entries, _ := os.ReadDir(pdir)
		for _, e := range entries {
			name := e.Name()
			stem, ok := strings.CutSuffix(name, ".jsonl")
			if !ok || !sessionFile.MatchString(stem) || !e.Type().IsRegular() {
				continue
			}
			out = append(out, source(filepath.Join(pdir, name), r.id, kindTranscript))
		}
	}
	history := filepath.Join(r.dir, "history.jsonl")
	if fi, err := os.Stat(history); err == nil && fi.Mode().IsRegular() {
		out = append(out, source(history, r.id, kindHistory))
	}
	return out
}

func source(path, rootID, kind string) adapters.Source {
	return adapters.Source{
		Adapter: "claude",
		Path:    path,
		Files:   []string{path},
		Root:    rootID,
		Extra:   map[string]string{"kind": kind},
	}
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// pathKey compares folder paths the way the file system does.
func pathKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}
