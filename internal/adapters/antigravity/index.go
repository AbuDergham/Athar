package antigravity

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

const (
	// agyhubName is the summary index file in each Antigravity root.
	agyhubName = "agyhub_summaries_proto.pb"
	// stateKey is the ItemTable key of the summary index in state.vscdb.
	stateKey = "antigravityUnifiedStateSync.trajectorySummaries"
)

// summary is what the index knows about one conversation.
type summary struct {
	title     string
	cwd       string
	created   time.Time
	updated   time.Time // last modified (field 3)
	lastInput time.Time // last user input (field 10)
}

// merge returns the more recently modified of s and o, with its empty fields
// filled from the other one.
func (s summary) merge(o summary) summary {
	w, l := s, o
	if o.updated.After(s.updated) {
		w, l = o, s
	}
	if w.title == "" {
		w.title = l.title
	}
	if w.cwd == "" {
		w.cwd = l.cwd
	}
	if w.created.IsZero() {
		w.created = l.created
	}
	if w.lastInput.IsZero() {
		w.lastInput = l.lastInput
	}
	return w
}

// index is the merged content of every index file.
type index struct {
	summaries map[string]summary
	warnings  []string
}

// indexCache keeps the last merged index for as long as its files are
// unchanged, so the many sources of one sync copy state.vscdb only once.
// It is the package's only mutable global state.
var indexCache struct {
	sync.Mutex
	key string
	idx index
}

// stateDBs returns %APPDATA%\Antigravity*\User\globalStorage\state.vscdb
// (Antigravity, Antigravity IDE, …), "Antigravity" first.
func stateDBs(env adapters.Env) []string {
	if env.AppData == "" {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(env.AppData, "Antigravity*"))
	sort.Slice(matches, func(i, j int) bool {
		bi, bj := filepath.Base(matches[i]), filepath.Base(matches[j])
		if (bi == "Antigravity") != (bj == "Antigravity") {
			return bi == "Antigravity"
		}
		return bi < bj
	})
	var out []string
	for _, m := range matches {
		if fi, err := os.Stat(m); err != nil || !fi.IsDir() {
			continue
		}
		db := filepath.Join(m, "User", "globalStorage", "state.vscdb")
		if _, err := os.Stat(db); err == nil {
			out = append(out, db)
		}
	}
	return out
}

// agyhubFiles returns the existing agyhub_summaries_proto.pb files, main root first.
func agyhubFiles(env adapters.Env) []string {
	var out []string
	for _, r := range roots(env) {
		p := filepath.Join(r, agyhubName)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// indexFiles lists every existing index file (with SQLite sidecars) for fingerprints.
func indexFiles(env adapters.Env) []string {
	out := agyhubFiles(env)
	for _, db := range stateDBs(env) {
		out = append(out, fsread.SQLiteSidecars(db)...)
	}
	return out
}

// loadIndex returns the merged index, reusing the cached one while the index
// files are unchanged. An agyhub file modified less than settle ago yields
// ErrRetryLater; a file that cannot be read or copied fails the read (the
// source is retried next sync); content that cannot be decoded is a warning.
func loadIndex(ctx context.Context, env adapters.Env, now time.Time) (index, error) {
	hubs := agyhubFiles(env)
	for _, p := range hubs {
		if _, mtime, err := fsread.Stat(p); err == nil {
			if age := now.Sub(mtime); age > -settle && age < settle {
				return index{}, fmt.Errorf("antigravity: %s changed less than 5 s ago: %w", filepath.Base(p), adapters.ErrRetryLater)
			}
		}
	}
	dbs := stateDBs(env)
	files := indexFiles(env)
	key, err := fsread.Fingerprint(files)
	if err == nil {
		key = strings.Join(files, "|") + "#" + key
		indexCache.Lock()
		if indexCache.key == key {
			idx := indexCache.idx
			indexCache.Unlock()
			return idx, nil
		}
		indexCache.Unlock()
	}

	idx := index{summaries: map[string]summary{}}
	add := func(cid string, s summary) {
		if old, ok := idx.summaries[cid]; ok {
			s = old.merge(s)
		}
		idx.summaries[cid] = s
	}
	for _, db := range dbs {
		if err := ctx.Err(); err != nil {
			return index{}, err
		}
		entries, warn, err := readStateDB(db, env.TempDir)
		if err != nil {
			return index{}, err
		}
		if warn != "" {
			idx.warnings = append(idx.warnings, warn)
		}
		for cid, s := range entries {
			add(cid, s)
		}
	}
	for _, p := range hubs {
		data, err := fsread.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return index{}, fmt.Errorf("antigravity: read %s: %w", p, err)
		}
		entries, ok := parseAgyhub(data)
		if !ok {
			idx.warnings = append(idx.warnings, fmt.Sprintf("%s: summary index not understood", p))
		}
		for cid, s := range entries {
			add(cid, s)
		}
	}
	if key != "" {
		indexCache.Lock()
		indexCache.key, indexCache.idx = key, idx
		indexCache.Unlock()
	}
	return idx, nil
}

// readStateDB reads the summary index of one state.vscdb through a temp copy.
func readStateDB(db, tempDir string) (map[string]summary, string, error) {
	cp, cleanup, err := fsread.CopySQLite(db, tempDir)
	defer cleanup()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("antigravity: %w", err)
	}
	conn, err := fsread.OpenSQLiteCopy(cp)
	if err != nil {
		return nil, "", fmt.Errorf("antigravity: open copy of %s: %w", db, err)
	}
	defer conn.Close()
	var val []byte
	err = conn.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, stateKey).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, fmt.Sprintf("%s: %s not readable: %v", db, stateKey, err), nil
	}
	entries, ok := parseStateValue(val)
	if !ok {
		return entries, fmt.Sprintf("%s: %s not understood", db, stateKey), nil
	}
	return entries, "", nil
}

// parseAgyhub decodes agyhub_summaries_proto.pb: repeated 1 {1: id, 2: Summary}.
// ok is false when the file is not a valid message (entries parsed so far are kept).
func parseAgyhub(data []byte) (map[string]summary, bool) {
	out := map[string]summary{}
	ok := eachField(data, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num != 1 || typ != protowire.BytesType {
			return
		}
		var cid string
		var sum []byte
		eachField(v, func(n protowire.Number, t protowire.Type, b []byte, _ uint64) {
			switch {
			case n == 1 && t == protowire.BytesType:
				cid = string(b)
			case n == 2 && t == protowire.BytesType:
				sum = b
			}
		})
		if s, good := parseSummary(sum); good && isConversationID(cid) {
			out[cid] = s
		}
	})
	return out, ok
}

// parseStateValue decodes the state.vscdb value: base64 of
// repeated 1 {1: id, 2: {1: base64(Summary)}}.
func parseStateValue(val []byte) (map[string]summary, bool) {
	data, ok := decodeBase64(val)
	if !ok {
		return nil, false
	}
	out := map[string]summary{}
	ok = eachField(data, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num != 1 || typ != protowire.BytesType {
			return
		}
		var cid string
		var sum []byte
		eachField(v, func(n protowire.Number, t protowire.Type, b []byte, _ uint64) {
			switch {
			case n == 1 && t == protowire.BytesType:
				cid = string(b)
			case n == 2 && t == protowire.BytesType:
				eachField(b, func(n2 protowire.Number, t2 protowire.Type, b2 []byte, _ uint64) {
					if n2 == 1 && t2 == protowire.BytesType {
						if d, good := decodeBase64(b2); good {
							sum = d
						}
					}
				})
			}
		})
		if s, good := parseSummary(sum); good && isConversationID(cid) {
			out[cid] = s
		}
	})
	return out, ok
}

// parseSummary decodes a Summary message (see the package comment).
func parseSummary(b []byte) (summary, bool) {
	if len(b) == 0 {
		return summary{}, false
	}
	var s summary
	var fallbackURI string
	ok := eachField(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if typ != protowire.BytesType {
			return
		}
		switch num {
		case 1:
			s.title = strings.Join(strings.Fields(string(v)), " ")
		case 3:
			s.updated = parseTimestamp(v)
		case 7:
			s.created = parseTimestamp(v)
		case 9:
			if u := firstString(v, 1); u != "" {
				s.cwd = workspacePath(u)
			}
		case 10:
			s.lastInput = parseTimestamp(v)
		case 17:
			eachField(v, func(n protowire.Number, t protowire.Type, b2 []byte, _ uint64) {
				if n == 1 && t == protowire.BytesType && fallbackURI == "" {
					fallbackURI = firstString(b2, 1)
				}
			})
		}
	})
	if s.cwd == "" && fallbackURI != "" {
		s.cwd = workspacePath(fallbackURI)
	}
	return s, ok
}

// parseTimestamp decodes {1: seconds, 2: nanos}; zero when absent.
func parseTimestamp(b []byte) time.Time {
	var sec, nsec uint64
	eachField(b, func(num protowire.Number, typ protowire.Type, _ []byte, x uint64) {
		if typ != protowire.VarintType {
			return
		}
		switch num {
		case 1:
			sec = x
		case 2:
			nsec = x
		}
	})
	if sec == 0 || sec > 1<<40 || nsec >= 1e9 {
		return time.Time{}
	}
	return time.Unix(int64(sec), int64(nsec)).UTC()
}

// firstString returns the first length-delimited field num of b as a string.
func firstString(b []byte, num protowire.Number) string {
	var out string
	found := false
	eachField(b, func(n protowire.Number, t protowire.Type, v []byte, _ uint64) {
		if !found && n == num && t == protowire.BytesType {
			out, found = string(v), true
		}
	})
	return out
}

// eachField calls fn for every top-level field of the protobuf message b:
// v holds the bytes of length-delimited fields, x the value of varints.
// It returns false when b is not a well-formed message (fields before the
// damage have been delivered).
func eachField(b []byte, fn func(num protowire.Number, typ protowire.Type, v []byte, x uint64)) bool {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		switch typ {
		case protowire.VarintType:
			x, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return false
			}
			fn(num, typ, nil, x)
			n = m
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return false
			}
			fn(num, typ, v, 0)
			n = m
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return false
			}
		}
		b = b[n:]
	}
	return true
}

// decodeBase64 decodes standard base64 with or without padding (surrounding
// whitespace and quotes are ignored).
func decodeBase64(b []byte) ([]byte, bool) {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" {
		return nil, false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if d, err := enc.DecodeString(s); err == nil {
			return d, true
		}
	}
	return nil, false
}

// workspacePath turns a VS Code file URI ("file:///c%3A/work/x",
// "file://server/share/x") into a Windows path with an upper-case drive
// letter. Anything that is not a file URI is returned unchanged.
func workspacePath(u string) string {
	u = strings.TrimSpace(u)
	const scheme = "file://"
	if len(u) < len(scheme) || !strings.EqualFold(u[:len(scheme)], scheme) {
		return u
	}
	rest := u[len(scheme):]
	host, p := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host, p = rest[:i], rest[i:]
	}
	host, p = unescape(host), unescape(p)
	if strings.EqualFold(host, "localhost") {
		host = ""
	}
	if host != "" { // UNC share
		return `\\` + host + strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`)
	}
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' && (p[1]|0x20 >= 'a' && p[1]|0x20 <= 'z') {
		tail := strings.Trim(strings.ReplaceAll(p[3:], "/", `\`), `\`)
		return strings.ToUpper(p[1:2]) + `:\` + tail
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// unescape decodes %XX escapes, leaving malformed ones as they are.
func unescape(s string) string {
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

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'f'
}

func unhex(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return (c | 0x20) - 'a' + 10
}
