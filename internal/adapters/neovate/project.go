package neovate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/fsread"
)

// encodeProject is Neovate's folder-name encoding of a launch folder
// (@neovate/code 0.12.7, Paths constructor): strip leading/trailing "/",
// replace every character outside [A-Za-z0-9] with "-", collapse runs of "-",
// trim "-" and lowercase. It is lossy, so Athar only ever encodes known paths
// and compares; it never decodes a folder name.
func encodeProject(cwd string) string {
	cwd = strings.Trim(cwd, "/")
	var b strings.Builder
	b.Grow(len(cwd))
	dash := false
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			dash = false
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// matchProject returns the data.json project key whose encoding is enc, or ""
// when there is none or when several different folders encode to enc. Keys
// that name the same folder (see samePathKey) count once; the first of them
// in sort order is returned.
func matchProject(keys []string, enc string) string {
	var cands []string
	for _, k := range keys {
		if k != "" && strings.EqualFold(encodeProject(k), enc) {
			cands = append(cands, k)
		}
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Strings(cands)
	first := samePathKey(cands[0])
	for _, c := range cands[1:] {
		if samePathKey(c) != first {
			return "" // ambiguous: never guess a launch folder
		}
	}
	return cands[0]
}

// samePathKey folds the spellings of one folder onto one comparison key:
// for a Windows path (drive letter, UNC or backslash) letter case and slash
// direction do not matter; for any path a trailing separator does not.
func samePathKey(p string) string {
	if !windowsPath(p) {
		for len(p) > 1 && strings.HasSuffix(p, "/") {
			p = p[:len(p)-1]
		}
		return p
	}
	p = strings.ToLower(strings.ReplaceAll(p, "/", `\`))
	for len(p) > 1 && strings.HasSuffix(p, `\`) && !strings.HasSuffix(p, `:\`) {
		p = p[:len(p)-1]
	}
	return p
}

// windowsPath reports whether p is spelled as a Windows path.
func windowsPath(p string) bool {
	return len(p) >= 2 && p[1] == ':' || strings.Contains(p, `\`)
}

// projectsCache remembers the project keys of one data.json by size and mtime,
// so a sync does not re-parse it for every session file.
type projectsCache struct {
	path  string
	size  int64
	mtime time.Time
	keys  []string
}

// projectKeys returns the launch folders recorded in data.json (its
// "projects" keys). A missing file yields no keys and no warning; an
// unreadable or malformed one yields no keys and a warning.
func (a *adapter) projectKeys(path string) (keys []string, warning string) {
	size, mtime, err := fsread.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ""
		}
		return nil, fmt.Sprintf("data.json unreadable, project folders unknown: %v", err)
	}
	a.mu.Lock()
	c := a.cache
	a.mu.Unlock()
	if c.path == path && c.size == size && c.mtime.Equal(mtime) {
		return c.keys, ""
	}
	data, err := fsread.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ""
		}
		return nil, fmt.Sprintf("data.json unreadable, project folders unknown: %v", err)
	}
	var doc struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Sprintf("data.json unreadable, project folders unknown: %v", err)
	}
	for k := range doc.Projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	a.mu.Lock()
	a.cache = projectsCache{path: path, size: size, mtime: mtime, keys: keys}
	a.mu.Unlock()
	return keys, ""
}
