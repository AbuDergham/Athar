package hermes

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// dbName is the Hermes state database in every home folder.
const dbName = "state.db"

// home is one Hermes home folder and the Root its sessions get.
type home struct {
	dir  string
	root string // "" for the default home
}

// defaultHome is where Hermes keeps its data when HERMES_HOME is unset:
// %LOCALAPPDATA%\hermes on Windows (%USERPROFILE%\AppData\Local\hermes when
// LOCALAPPDATA is empty).
func defaultHome(env adapters.Env) string {
	base := env.LocalAppData
	if base == "" {
		if env.Home == "" {
			return ""
		}
		base = filepath.Join(env.Home, "AppData", "Local")
	}
	return filepath.Join(base, "hermes")
}

// homes lists the Hermes homes to look in: the default one, ~\.hermes
// (the non-Windows default, also used by older or ported installs),
// HERMES_HOME, and every named profile (<home>\profiles\<name>) of those.
func homes(env adapters.Env) []home {
	def := defaultHome(env)
	var bases []string
	if def != "" {
		bases = append(bases, def)
	}
	if env.Home != "" {
		bases = append(bases, filepath.Join(env.Home, ".hermes"))
	}
	if env.Getenv != nil {
		if v := strings.TrimSpace(env.Getenv("HERMES_HOME")); v != "" {
			bases = append(bases, expandHome(v, env.Home))
		}
	}
	seen := map[string]bool{}
	var out []home
	add := func(dir string) {
		dir = filepath.Clean(dir)
		key := strings.ToLower(dir)
		if seen[key] {
			return
		}
		seen[key] = true
		root := dir
		if def != "" && strings.EqualFold(dir, filepath.Clean(def)) {
			root = ""
		}
		out = append(out, home{dir: dir, root: root})
	}
	for _, b := range bases {
		add(b)
		entries, err := os.ReadDir(filepath.Join(b, "profiles"))
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			add(filepath.Join(b, "profiles", n))
		}
	}
	return out
}

// expandHome resolves a leading "~" the way Hermes does for HERMES_HOME.
func expandHome(p, userHome string) string {
	if userHome != "" && (p == "~" || strings.HasPrefix(p, `~\`) || strings.HasPrefix(p, "~/")) {
		return filepath.Join(userHome, p[1:])
	}
	return p
}

// Sources lists every Hermes state database: one source per home folder that
// holds a state.db. Missing folders are skipped silently.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	for _, h := range homes(env) {
		db := filepath.Join(h.dir, dbName)
		info, err := os.Stat(db)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, adapters.Source{
			Adapter: "hermes",
			Path:    db,
			Files:   fsread.SQLiteSidecars(db),
			Root:    h.root,
		})
	}
	return out, nil
}
