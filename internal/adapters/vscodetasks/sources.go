package vscodetasks

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
)

// Task folder file names.
const (
	apiFile  = "api_conversation_history.json"
	uiFile   = "ui_messages.json"
	itemFile = "history_item.json"
)

// Sources lists one Source per task folder in every host under %APPDATA%,
// and for Cline also the CLI task folders and SDK session folders.
func (a *adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	if env.AppData != "" {
		hosts, err := os.ReadDir(env.AppData)
		if err == nil {
			for _, h := range hosts {
				if !isDir(filepath.Join(env.AppData, h.Name())) {
					continue
				}
				tasks := filepath.Join(env.AppData, h.Name(), "User", "globalStorage", a.f.ext, "tasks")
				out = append(out, a.taskSources(tasks, h.Name())...)
			}
		}
	}
	if a.f.sdk {
		for _, dir := range clineDataDirs(env) {
			out = append(out, a.taskSources(filepath.Join(dir, "tasks"), hostCLI)...)
		}
		for _, dir := range clineSessionDirs(env) {
			out = append(out, a.sdkSources(dir)...)
		}
	}
	return out, nil
}

// taskSources lists the task folders under tasksDir that hold a chat file.
func (a *adapter) taskSources(tasksDir, host string) []adapters.Source {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil // missing locations are skipped silently
	}
	var out []adapters.Source
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		dir := filepath.Join(tasksDir, e.Name())
		var files []string
		for _, name := range []string{apiFile, uiFile} {
			if p := filepath.Join(dir, name); isFile(p) {
				files = append(files, p)
			}
		}
		if len(files) == 0 {
			continue // checkpoints only, or an empty folder
		}
		if p := filepath.Join(dir, itemFile); isFile(p) {
			files = append(files, p) // the task's own index entry (title, cwd)
		}
		out = append(out, adapters.Source{
			Adapter: a.f.id,
			Path:    dir,
			Files:   files,
			Extra:   map[string]string{extraKind: kindTask, extraHost: host},
		})
	}
	return out
}

// sdkSources lists <sessions>\<id>\ folders holding <id>.json.
func (a *adapter) sdkSources(sessionsDir string) []adapters.Source {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil
	}
	var out []adapters.Source
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(sessionsDir, e.Name())
		manifest := filepath.Join(dir, e.Name()+".json")
		if !isFile(manifest) {
			continue
		}
		files := []string{manifest}
		if p := filepath.Join(dir, e.Name()+".messages.json"); isFile(p) {
			files = append(files, p)
		}
		out = append(out, adapters.Source{
			Adapter: a.f.id,
			Path:    dir,
			Files:   files,
			Extra:   map[string]string{extraKind: kindSDK, extraHost: hostCLI},
		})
	}
	return out
}

// clineDataDirs returns Cline's data folders: %USERPROFILE%\.cline\data, and
// CLINE_DATA_DIR or CLINE_DIR\data when set to other absolute folders.
func clineDataDirs(env adapters.Env) []string {
	var out []string
	add := func(p string) {
		if p == "" || !filepath.IsAbs(p) {
			return
		}
		p = filepath.Clean(p)
		for _, q := range out {
			if strings.EqualFold(p, q) {
				return
			}
		}
		out = append(out, p)
	}
	if env.Home != "" {
		add(filepath.Join(env.Home, ".cline", "data"))
	}
	if env.Getenv != nil {
		if v := strings.TrimSpace(env.Getenv("CLINE_DIR")); v != "" {
			add(filepath.Join(v, "data"))
		}
		add(strings.TrimSpace(env.Getenv("CLINE_DATA_DIR")))
	}
	return out
}

// clineSessionDirs returns <data>\sessions for every data folder, plus
// CLINE_SESSION_DATA_DIR when set.
func clineSessionDirs(env adapters.Env) []string {
	var out []string
	for _, d := range clineDataDirs(env) {
		out = append(out, filepath.Join(d, "sessions"))
	}
	if env.Getenv != nil {
		if v := strings.TrimSpace(env.Getenv("CLINE_SESSION_DATA_DIR")); v != "" && filepath.IsAbs(v) {
			v = filepath.Clean(v)
			dup := false
			for _, q := range out {
				dup = dup || strings.EqualFold(v, q)
			}
			if !dup {
				out = append(out, v)
			}
		}
	}
	return out
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
