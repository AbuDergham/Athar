package trae

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// Store keys, in merge priority order (an earlier kind wins ties).
const (
	keyAgent    = "memento/icube-ai-agent-storage"
	keyChat     = "memento/icube-ai-ng-chat-storage"
	keyMigrated = "chatHistoryNeedToBeMigrated-"
)

// keyRank returns the merge priority of an ItemTable key and a label for it
// without the user id, or -1 for keys that do not hold sessions.
func keyRank(key string) (int, string) {
	switch {
	case key == keyAgent:
		return 0, keyAgent
	case strings.HasPrefix(key, keyAgent+"-"):
		return 1, keyAgent + "-<user>"
	case key == keyChat:
		return 2, keyChat
	case strings.HasPrefix(key, keyChat+"-"):
		return 2, keyChat + "-<user>"
	case strings.HasPrefix(key, keyMigrated):
		return 3, keyMigrated + "<user>"
	}
	return -1, ""
}

// storeQuery preselects the candidate keys; keyRank decides exactly.
const storeQuery = `SELECT key, value FROM ItemTable
WHERE key = 'memento/icube-ai-agent-storage'
   OR key LIKE 'memento/icube-ai-agent-storage-%'
   OR key LIKE 'memento/icube-ai-ng-chat-storage%'
   OR key LIKE 'chatHistoryNeedToBeMigrated-%'`

// storeValue is one ItemTable row that holds sessions.
type storeValue struct {
	rank  int
	key   string // the raw key (only used for ordering)
	label string // the key without the user id (for warnings)
	value []byte
}

// Read copies one state.vscdb, reads the chat keys and returns the sessions.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	cp, cleanup, err := fsread.CopySQLite(src.Path, env.TempDir)
	defer cleanup()
	if err != nil {
		return res, err
	}
	db, err := fsread.OpenSQLiteCopy(cp)
	if err != nil {
		return res, err
	}
	defer db.Close() // runs before cleanup removes the copy

	values, ok, err := loadValues(ctx, db)
	if err != nil {
		return res, err
	}
	if !ok {
		res.Warnings = append(res.Warnings, "no ItemTable: not a VS Code state database")
		return res, nil
	}

	cwd := ""
	if src.Extra[extraScope] != "global" {
		if data, err := fsread.ReadFile(filepath.Join(filepath.Dir(src.Path), workspaceFile)); err == nil {
			cwd = workspaceFolder(data)
		}
	}

	m := newMerger()
	for _, v := range values {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		list, ok := sessionList(v.value)
		if !ok {
			res.Warnings = append(res.Warnings, v.label+": value is not JSON; format not recognized")
			continue
		}
		for _, raw := range list {
			rs, skipped, ok := parseSession(raw)
			res.Skipped += skipped
			if ok {
				m.add(rs)
			}
		}
	}
	for _, s := range m.sessions() {
		s.Cwd = cwd
		res.Sessions = append(res.Sessions, s)
	}
	return res, nil
}

// loadValues reads the session-holding rows in merge order. ok is false when
// the database has no ItemTable.
func loadValues(ctx context.Context, db *sql.DB) ([]storeValue, bool, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'ItemTable'`).Scan(&n); err != nil {
		return nil, false, err
	}
	if n == 0 {
		return nil, false, nil
	}
	rows, err := db.QueryContext(ctx, storeQuery)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	var out []storeValue
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, true, err
		}
		rank, label := keyRank(key)
		if rank < 0 || len(bytes.TrimSpace(value)) == 0 {
			continue
		}
		out = append(out, storeValue{rank: rank, key: key, label: label, value: value})
	}
	if err := rows.Err(); err != nil {
		return nil, true, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		return out[i].key < out[j].key
	})
	return out, true, nil
}

// sessionList returns the session elements of a store value: either
// {list:[...]} or a bare array. ok is false when the value is not JSON.
func sessionList(value []byte) ([]json.RawMessage, bool) {
	value = bytes.TrimSpace(value)
	if len(value) > 0 && value[0] == '[' {
		var list []json.RawMessage
		if err := json.Unmarshal(value, &list); err != nil {
			return nil, false
		}
		return list, true
	}
	var obj struct {
		List json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(value, &obj); err != nil {
		return nil, false
	}
	var list []json.RawMessage
	if json.Unmarshal(obj.List, &list) != nil {
		return nil, true // no list (or not an array): nothing stored
	}
	return list, true
}

// merger combines the copies of one session found under several keys.
type merger struct {
	order []string
	byID  map[string]*parsedSession
}

func newMerger() *merger { return &merger{byID: map[string]*parsedSession{}} }

func (m *merger) add(s parsedSession) {
	cur, ok := m.byID[s.id]
	if !ok {
		c := s
		m.byID[s.id] = &c
		m.order = append(m.order, s.id)
		return
	}
	if cur.title == "" {
		cur.title = s.title
	}
	if !s.created.IsZero() && (cur.created.IsZero() || s.created.Before(cur.created)) {
		cur.created = s.created
	}
	if s.updated.After(cur.updated) {
		cur.updated = s.updated
	}
	if len(s.msgs) > len(cur.msgs) {
		cur.msgs, cur.hasMore = s.msgs, s.hasMore
	}
}

// sessions returns the merged sessions in order of first appearance,
// leaving out empty untitled drafts.
func (m *merger) sessions() []model.Session {
	var out []model.Session
	for _, sid := range m.order {
		p := m.byID[sid]
		if len(p.msgs) == 0 && p.title == "" {
			continue
		}
		started, updated := p.created, p.updated
		for _, msg := range p.msgs {
			if msg.At.IsZero() {
				continue
			}
			if started.IsZero() || msg.At.Before(started) {
				started = msg.At
			}
			if msg.At.After(updated) {
				updated = msg.At
			}
		}
		if started.IsZero() {
			started = updated
		}
		if updated.IsZero() {
			updated = started
		}
		cov := model.CoverageFull
		switch {
		case len(p.msgs) == 0:
			cov = model.CoverageTitles
		case p.hasMore:
			cov = model.CoveragePartial
		}
		title, ts := sessionTitle(p.title, p.msgs)
		out = append(out, model.Session{
			Agent:       id,
			NativeID:    p.id,
			Title:       title,
			TitleSource: ts,
			StartedAt:   started,
			UpdatedAt:   updated,
			Coverage:    cov,
			Origin:      model.OriginLive,
			Messages:    p.msgs,
		})
	}
	return out
}

// sessionTitle shortens Trae's title or falls back to the first user
// message. Trae names a session after its first prompt (title = name = that
// prompt, verbatim), so both are TitlePrompt.
func sessionTitle(title string, msgs []model.Message) (string, model.TitleSource) {
	if t := collapse(title); t != "" {
		return cutRunes(t, titleRunes), model.TitlePrompt
	}
	for _, m := range msgs {
		if m.Role == model.RoleUser && m.Kind == model.KindText {
			if p := collapse(m.Text); p != "" {
				return cutRunes(p, titleRunes), model.TitlePrompt
			}
		}
	}
	return "", model.TitleNone
}

// titleRunes is the maximum title length.
const titleRunes = 100

// collapse joins the words of s with single spaces.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// cutRunes keeps the first n runes of s.
func cutRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// msTime reads an epoch timestamp given as a JSON number or numeric string:
// milliseconds, or seconds for small values. Zero when absent or invalid.
func msTime(raw json.RawMessage) time.Time {
	var n json.Number
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, `"`) {
		var str string
		if json.Unmarshal(raw, &str) != nil {
			return time.Time{}
		}
		s = strings.TrimSpace(str)
	}
	n = json.Number(s)
	v, err := n.Int64()
	if err != nil {
		f, ferr := n.Float64()
		if ferr != nil {
			return time.Time{}
		}
		v = int64(f)
	}
	switch {
	case v <= 0:
		return time.Time{}
	case v >= 100_000_000_000: // milliseconds (seconds would be year 5138+)
		return time.UnixMilli(v).UTC()
	default:
		return time.Unix(v, 0).UTC()
	}
}
