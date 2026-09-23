package gemini

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AbuDergham/Athar/internal/fsread"
)

// hashDir matches the old sha256(project path) folder names under tmp\.
var hashDir = regexp.MustCompile(`^[0-9a-f]{64}$`)

// projectRoot returns the project path of a tmp\<slug> folder, or "" when
// it cannot be known. See the package comment for the lookup order.
func projectRoot(gemDir, slugDir string) string {
	slug := filepath.Base(slugDir)
	p := readMarker(filepath.Join(slugDir, ".project_root"))
	if p == "" {
		p = readMarker(filepath.Join(gemDir, "history", slug, ".project_root"))
	}
	if p == "" {
		for path, s := range registry(gemDir) {
			if s == slug {
				p = path
				break
			}
		}
	}
	if p == "" && hashDir.MatchString(slug) {
		return pathForHash(gemDir, slug)
	}
	if p == "" {
		return ""
	}
	return trueCase(p)
}

// readMarker returns the trimmed content of a .project_root file, or "".
func readMarker(path string) string {
	b, err := fsread.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(string(b), "\xef\xbb\xbf"))
}

// registry reads projects.json: lowercased project path -> slug.
func registry(gemDir string) map[string]string {
	b, err := fsread.ReadFile(filepath.Join(gemDir, "projects.json"))
	if err != nil {
		return nil
	}
	var reg struct {
		Projects map[string]string `json:"projects"`
	}
	if json.Unmarshal(b, &reg) != nil {
		return nil
	}
	return reg.Projects
}

// pathForHash finds the known project whose sha256 names an old hash folder.
// The CLI hashed the project path as the process saw it (real casing), so
// both the stored (lowercased) path and its real casing are tried.
func pathForHash(gemDir, hash string) string {
	seen := map[string]bool{}
	var cands []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			cands = append(cands, p)
		}
	}
	for p := range registry(gemDir) {
		add(p)
	}
	for _, base := range []string{"tmp", "history"} {
		entries, _ := os.ReadDir(filepath.Join(gemDir, base))
		for _, e := range entries {
			if e.IsDir() && !hashDir.MatchString(e.Name()) {
				add(readMarker(filepath.Join(gemDir, base, e.Name(), ".project_root")))
			}
		}
	}
	for _, c := range cands {
		real := trueCase(c)
		for _, v := range []string{c, real} {
			sum := sha256.Sum256([]byte(v))
			if hex.EncodeToString(sum[:]) == hash {
				return real
			}
		}
	}
	return ""
}

// trueCase returns p with the casing the file system actually uses, walking
// the path one folder at a time (read-only directory listings). Gemini stores
// project paths lowercased on Windows. When p does not exist it is returned as is.
func trueCase(p string) string {
	clean := filepath.Clean(p)
	vol := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean[len(vol):], string(filepath.Separator))
	if vol == "" || !filepath.IsAbs(clean) {
		return p
	}
	if len(vol) == 2 && vol[1] == ':' {
		vol = strings.ToUpper(vol)
	}
	cur := vol + string(filepath.Separator)
	if _, err := os.Stat(cur); err != nil {
		return p
	}
	if rest == "" {
		return cur
	}
	for _, comp := range strings.Split(rest, string(filepath.Separator)) {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return p
		}
		match := ""
		for _, e := range entries {
			if e.Name() == comp {
				match = comp
				break
			}
			if match == "" && strings.EqualFold(e.Name(), comp) {
				match = e.Name()
			}
		}
		if match == "" {
			return p
		}
		cur = filepath.Join(cur, match)
	}
	return cur
}
