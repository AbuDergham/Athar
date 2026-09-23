package commandcode

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
)

// Sources lists one source per transcript: Files are the transcript and its
// .meta.json (the title lives there), so a rename or a generated title
// changes the fingerprint.
func (a adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var out []adapters.Source
	for _, root := range dataRoots(env) {
		projects := filepath.Join(root, "projects")
		dirs, err := os.ReadDir(projects)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			dir := filepath.Join(projects, d.Name())
			files, err := os.ReadDir(dir)
			if err != nil {
				continue // vanished or unreadable project folder: nothing to list
			}
			for _, f := range files {
				if !f.Type().IsRegular() || !isTranscriptName(f.Name()) {
					continue
				}
				p := filepath.Join(dir, f.Name())
				out = append(out, adapters.Source{
					Adapter: a.ID(),
					Path:    p,
					Files:   []string{p, metaPath(p)},
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// dataRoots returns the .commandcode folders to scan: under %HOME% (which
// Command Code prefers when set) and under %USERPROFILE%, without duplicates.
// A HOME that is not an absolute Windows path (e.g. MSYS "/c/Users/x") is ignored.
func dataRoots(env adapters.Env) []string {
	var homes []string
	if env.Getenv != nil {
		if h := env.Getenv("HOME"); h != "" && filepath.IsAbs(h) {
			homes = append(homes, h)
		}
	}
	if env.Home != "" {
		homes = append(homes, env.Home)
	}
	var roots []string
	for i, h := range homes {
		dup := false
		for _, prev := range homes[:i] {
			if sameDir(prev, h) {
				dup = true
				break
			}
		}
		if !dup {
			roots = append(roots, filepath.Join(h, ".commandcode"))
		}
	}
	return roots
}

// sameDir reports whether two folder paths name the same folder, ignoring
// trailing separators (and case on Windows).
func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// isTranscriptName mirrors the CLI's isSessionTranscriptFileName.
func isTranscriptName(name string) bool {
	return strings.HasSuffix(name, ".jsonl") && len(name) > len(".jsonl") &&
		!strings.Contains(name, ".checkpoints.") &&
		!strings.Contains(name, ".prompts.") &&
		!strings.Contains(name, ".v2.bak")
}

// metaPath returns the .meta.json sidecar of a transcript.
func metaPath(transcript string) string {
	return strings.TrimSuffix(transcript, ".jsonl") + ".meta.json"
}
