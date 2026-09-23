package cursor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// workspaceDir is one workspaceStorage\<hash> folder that has a workspace.json.
type workspaceDir struct {
	jsonPath string
	dbPath   string
}

// listWorkspaces returns the workspaceStorage folders that have a
// workspace.json (folders without one are empty windows).
func listWorkspaces(dir string) []workspaceDir {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []workspaceDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(dir, e.Name())
		w := workspaceDir{jsonPath: filepath.Join(d, "workspace.json"), dbPath: filepath.Join(d, "state.vscdb")}
		if isFile(w.jsonPath) {
			out = append(out, w)
		}
	}
	return out
}

// workspaceIndex maps chats to project folders through the workspace
// databases. Folders come from workspace.json right away; the databases are
// only copied and read the first time a chat needs them.
type workspaceIndex struct {
	env      adapters.Env
	dirs     []workspaceDir
	folder   map[string]string // workspace.json path -> folder
	loaded   bool
	owner    map[string]string // composerId -> folder
	warnings []string
}

func newWorkspaceIndex(env adapters.Env, dir string) *workspaceIndex {
	w := &workspaceIndex{env: env, folder: map[string]string{}}
	for _, d := range listWorkspaces(dir) {
		data, err := fsread.ReadFile(d.jsonPath)
		if err != nil {
			continue
		}
		var wj struct {
			Folder string `json:"folder"`
		}
		if json.Unmarshal(data, &wj) != nil {
			continue
		}
		if f := fileURIPath(wj.Folder); f != "" {
			w.dirs = append(w.dirs, d)
			w.folder[d.jsonPath] = f
		}
	}
	return w
}

// folders lists the project folders of all folder workspaces.
func (w *workspaceIndex) folders() []string {
	out := make([]string, 0, len(w.dirs))
	for _, d := range w.dirs {
		out = append(out, w.folder[d.jsonPath])
	}
	return out
}

// folderOf returns the folder of the workspace that lists composerID, or "".
func (w *workspaceIndex) folderOf(composerID string) string {
	if !w.loaded {
		w.load()
	}
	return w.owner[composerID]
}

func (w *workspaceIndex) load() {
	w.loaded = true
	w.owner = map[string]string{}
	for _, d := range w.dirs {
		if !isFile(d.dbPath) {
			continue
		}
		ids, err := workspaceComposers(d.dbPath, w.env.TempDir)
		if err != nil {
			w.warnings = append(w.warnings, fmt.Sprintf("workspace %s: %v", filepath.Base(filepath.Dir(d.dbPath)), err))
			continue
		}
		for _, cid := range ids {
			if _, dup := w.owner[cid]; !dup {
				w.owner[cid] = w.folder[d.jsonPath]
			}
		}
	}
}

// workspaceComposers reads ItemTable composer.composerData.allComposers[].composerId
// from a private copy of a workspace database.
func workspaceComposers(dbPath, tempDir string) ([]string, error) {
	copyPath, cleanup, err := fsread.CopySQLite(dbPath, tempDir)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	db, err := fsread.OpenSQLiteCopy(copyPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if !hasTable(db, "ItemTable") {
		return nil, nil
	}
	var value []byte
	switch err := db.QueryRow(`SELECT value FROM ItemTable WHERE key = 'composer.composerData'`).Scan(&value); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, err
	}
	var data struct {
		AllComposers []struct {
			ComposerID string `json:"composerId"`
		} `json:"allComposers"`
	}
	if !decodeJSON(value, &data) {
		return nil, fmt.Errorf("composer.composerData is not valid JSON")
	}
	var ids []string
	for _, c := range data.AllComposers {
		if c.ComposerID != "" {
			ids = append(ids, c.ComposerID)
		}
	}
	return ids, nil
}

// fileURIPath turns a VS Code file:// URI into a Windows path
// ("file:///d%3A/work/x" -> "D:\work\x", "file://server/share" -> UNC).
// Anything that is not a file URI is returned unchanged.
func fileURIPath(uri string) string {
	uri = strings.TrimSpace(uri)
	const scheme = "file://"
	if len(uri) < len(scheme) || !strings.EqualFold(uri[:len(scheme)], scheme) {
		return uri
	}
	rest := uri[len(scheme):]
	host, p := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host, p = rest[:i], rest[i:]
	}
	host, p = percentDecode(host), percentDecode(p)
	if strings.EqualFold(host, "localhost") {
		host = ""
	}
	if host != "" {
		return `\\` + host + strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`)
	}
	if len(p) >= 3 && p[0] == '/' && isASCIILetter(p[1]) && p[2] == ':' {
		tail := strings.Trim(strings.ReplaceAll(p[3:], "/", `\`), `\`)
		return strings.ToUpper(p[1:2]) + `:\` + tail
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// upperDrive upper-cases the drive letter of a Windows path (VS Code's
// fsPath lower-cases it).
func upperDrive(p string) string {
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		return strings.ToUpper(p[:1]) + p[1:]
	}
	return p
}

// percentDecode decodes %XX escapes and leaves malformed ones as they are.
func percentDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isASCIILetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'f'
}

func unhex(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return (c | 0x20) - 'a' + 10
}

// slugKey reduces a path or a ~\.cursor\projects folder name to lower-case
// letters and digits separated by single dashes, so that
// "D:\Projects\My App" and "d-Projects-My-App" compare equal.
func slugKey(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		dash = true
	}
	return b.String()
}

// matchSlug returns the one known folder whose slug is slug, or "" when none
// or several match. The slug itself is never turned into a path.
func matchSlug(slug string, known []string) string {
	key := slugKey(slug)
	if key == "" {
		return ""
	}
	found := ""
	for _, k := range known {
		if slugKey(k) != key {
			continue
		}
		switch {
		case found == "":
			found = k
		case !strings.EqualFold(found, k):
			return "" // ambiguous
		}
	}
	return found
}
