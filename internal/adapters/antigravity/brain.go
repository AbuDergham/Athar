package antigravity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// logNames are the step-log files of brain\<id>\.system_generated\logs in
// preference order; other *.jsonl files there come after them.
var logNames = []string{"transcript_full.jsonl", "transcript.jsonl", "overview.txt"}

// bom is the UTF-8 byte order mark some editors put at the start of a file.
const bom = "\ufeff"

// artifact is one Markdown artifact of a conversation.
type artifact struct {
	name string // "task.md"
	path string // the version that is read
	meta string // <name>.metadata.json ("" when absent)
}

// brain is what a brain\<id> folder holds that Athar reads.
type brain struct {
	dir       string
	artifacts []artifact
	logs      []string
}

// hasText reports whether the folder holds anything readable.
func (b brain) hasText() bool { return len(b.artifacts) > 0 || len(b.logs) > 0 }

// files lists the files that are read (for the fingerprint).
func (b brain) files() []string {
	var out []string
	for _, a := range b.artifacts {
		out = append(out, a.path)
		if a.meta != "" {
			out = append(out, a.meta)
		}
	}
	return append(out, b.logs...)
}

// recentFile returns a whole-file input (artifact or metadata) modified less
// than settle before now, or "".
func (b brain) recentFile(now time.Time) string {
	for _, a := range b.artifacts {
		for _, p := range []string{a.path, a.meta} {
			if p == "" {
				continue
			}
			if _, mtime, err := fsread.Stat(p); err == nil {
				if age := now.Sub(mtime); age > -settle && age < settle {
					return p
				}
			}
		}
	}
	return ""
}

// pickBrain scans the brain folders of one id (root order) and returns the
// first that holds readable text, else the first one (zero brain when none).
func pickBrain(dirs []string) brain {
	var first brain
	for i, d := range dirs {
		b := scanBrain(d)
		if b.hasText() {
			return b
		}
		if i == 0 {
			first = b
		}
	}
	return first
}

// scanBrain lists the artifacts and step logs of a brain\<id> folder.
// For every <name>.md the current file is read; when it is missing, the
// <name>.md.resolved file, else the highest <name>.md.resolved.N.
func scanBrain(dir string) brain {
	b := brain{dir: dir}
	entries, err := os.ReadDir(dir)
	if err == nil {
		type cand struct {
			cur, resolved string
			n             int
			numbered      string
			meta          string
		}
		cands := map[string]*cand{}
		get := func(name string) *cand {
			c := cands[name]
			if c == nil {
				c = &cand{n: -1}
				cands[name] = c
			}
			return c
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			low := strings.ToLower(n)
			i := strings.Index(low, ".md")
			if i <= 0 {
				continue
			}
			base, rest := n[:i+3], low[i+3:]
			switch {
			case rest == "":
				get(base).cur = n
			case rest == ".metadata.json":
				get(base).meta = n
			case rest == ".resolved":
				get(base).resolved = n
			case strings.HasPrefix(rest, ".resolved."):
				if k, err := strconv.Atoi(rest[len(".resolved."):]); err == nil && k >= 0 {
					c := get(base)
					if k > c.n {
						c.n, c.numbered = k, n
					}
				}
			}
		}
		for name, c := range cands {
			pick := c.cur
			if pick == "" {
				pick = c.resolved
			}
			if pick == "" {
				pick = c.numbered
			}
			if pick == "" {
				continue
			}
			a := artifact{name: name, path: filepath.Join(dir, pick)}
			if c.meta != "" {
				a.meta = filepath.Join(dir, c.meta)
			}
			b.artifacts = append(b.artifacts, a)
		}
		sort.Slice(b.artifacts, func(i, j int) bool {
			ri, rj := artifactRank(b.artifacts[i].name), artifactRank(b.artifacts[j].name)
			if ri != rj {
				return ri < rj
			}
			return b.artifacts[i].name < b.artifacts[j].name
		})
	}

	logDir := filepath.Join(dir, ".system_generated", "logs")
	if les, err := os.ReadDir(logDir); err == nil {
		have := map[string]string{} // lower-case name → real name
		var extra []string
		for _, e := range les {
			if e.IsDir() {
				continue
			}
			have[strings.ToLower(e.Name())] = e.Name()
			if strings.HasSuffix(strings.ToLower(e.Name()), ".jsonl") && !isLogName(e.Name()) {
				extra = append(extra, e.Name())
			}
		}
		for _, n := range logNames {
			if real, ok := have[n]; ok {
				b.logs = append(b.logs, filepath.Join(logDir, real))
			}
		}
		sort.Strings(extra)
		for _, n := range extra {
			b.logs = append(b.logs, filepath.Join(logDir, n))
		}
	}
	return b
}

func isLogName(n string) bool {
	for _, l := range logNames {
		if strings.EqualFold(n, l) {
			return true
		}
	}
	return false
}

// artifactRank orders the well-known artifacts first.
func artifactRank(name string) int {
	switch strings.ToLower(name) {
	case "task.md":
		return 0
	case "implementation_plan.md":
		return 1
	case "walkthrough.md":
		return 2
	}
	return 3
}

// artifactLabel turns "implementation_plan.md" into "Implementation plan".
func artifactLabel(name string) string {
	s := strings.TrimSuffix(name, filepath.Ext(name))
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || unicode.IsSpace(r) }), " ")
	if s == "" {
		return name
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// artifactMeta is <name>.md.metadata.json.
type artifactMeta struct {
	UpdatedAt string `json:"updatedAt"`
}

// artifactMessages returns one assistant message per artifact with text.
// Unreadable files are counted as skipped.
func artifactMessages(arts []artifact) ([]model.Message, int) {
	var out []model.Message
	skipped := 0
	for _, a := range arts {
		data, err := fsread.ReadFile(a.path)
		if err != nil {
			skipped++
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(string(data), bom))
		if text == "" {
			continue
		}
		var at time.Time
		if a.meta != "" {
			if mb, err := fsread.ReadFile(a.meta); err == nil {
				var m artifactMeta
				if json.Unmarshal(mb, &m) == nil {
					at = parseTime(m.UpdatedAt)
				}
			}
		}
		if at.IsZero() {
			if _, mtime, err := fsread.Stat(a.path); err == nil {
				at = mtime.UTC()
			}
		}
		out = append(out, model.Message{
			Role: model.RoleAssistant,
			Kind: model.KindText,
			At:   at,
			Text: artifactLabel(a.name) + " (" + a.name + ")\n\n" + text,
		})
	}
	return out, skipped
}

// parseTime parses an RFC 3339 timestamp to UTC; zero when absent or invalid.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
