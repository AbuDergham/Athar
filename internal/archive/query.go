package archive

import (
	"database/sql"
	"math/bits"
	"sort"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
	"github.com/AbuDergham/Athar/internal/search"
)

// SnippetWidth is the width, in characters, of Row.Snippet.
const SnippetWidth = 80

// maxTerms bounds the terms of one query (a bit mask per session tracks them).
const maxTerms = 64

// Filter narrows the list. Empty Query = timeline (newest first).
type Filter struct {
	Query string
	Agent string    // "" = all
	Since time.Time // zero = any time
}

// Row is one line of the result list.
type Row struct {
	SessionID    int64
	Agent        string
	NativeID     string
	Title        string
	ProjectName  string
	ProjectPath  string
	Cwd          string
	Root         string
	Machine      string
	Coverage     model.Coverage
	Origin       model.Origin
	StartedAt    time.Time
	UpdatedAt    time.Time
	MessageCount int
	Missing      bool // source deleted by the agent (archive-only)

	Snippet      string // around the best hit, from ORIGINAL text; "" for timeline
	HitSeq       int    // seq of the best-matching message, -1 if none
	MatchedTerms int    // how many query terms this session matches
	TotalTerms   int
}

// Search returns rows for the filter (spec §5.3: per-session AND, partial matches after full ones).
//
// Each term is one FTS5 trigram query (a quoted string, i.e. a substring of
// the normalized text) over the messages and one over the titles and project
// names; terms under 3 characters only match titles and project names
// (a substring test on sessions.norm_title).
// Sessions are ranked by the number of terms they match, then by the number
// matched in the title or project name, then by the sum over terms of the
// best bm25 of a matching message (see bm25MaxShare), then by recency. The
// hit message is the one that matches the most terms (ties: best bm25, then
// first); the snippet is cut around it by search.Snippet from the original
// text. The ranking of the last query is cached, so the next pages of a
// query cost only the rows of the page; any write to the archive
// invalidates it. A limit below 1 means no limit.
func (d *DB) Search(f Filter, limit, offset int) ([]Row, error) {
	terms := search.Parse(f.Query)
	if len(terms) > maxTerms {
		terms = terms[:maxTerms]
	}
	if len(terms) == 0 {
		return d.timeline(f, limit, offset)
	}
	hits, err := d.rank(f, terms)
	if err != nil {
		return nil, err
	}
	offset = max(offset, 0)
	if offset >= len(hits) {
		return nil, nil
	}
	end := len(hits)
	if limit > 0 {
		end = min(end, offset+limit)
	}
	return d.pageRows(hits[offset:end], terms)
}

// sessionCols are the sessions columns scanned by scanRow, in order.
const sessionCols = `id, agent, native_id, title, project_name, project_path, cwd, root, machine,
	coverage, origin, started_at, updated_at, message_count, source_missing_at`

func scanRow(sc interface{ Scan(...any) error }) (Row, error) {
	var r Row
	var started, updated int64
	var missing sql.NullInt64
	err := sc.Scan(&r.SessionID, &r.Agent, &r.NativeID, &r.Title, &r.ProjectName, &r.ProjectPath, &r.Cwd,
		&r.Root, &r.Machine, &r.Coverage, &r.Origin, &started, &updated, &r.MessageCount, &missing)
	r.StartedAt, r.UpdatedAt, r.Missing, r.HitSeq = fromNanos(started), fromNanos(updated), missing.Valid, -1
	return r, err
}

// filterSQL returns the WHERE conditions of the Agent and Since filters on
// table alias s (each prefixed by " AND ") and their arguments.
func filterSQL(f Filter, s string) (string, []any) {
	var cond string
	var args []any
	if f.Agent != "" {
		cond += " AND " + s + ".agent = ?"
		args = append(args, f.Agent)
	}
	if !f.Since.IsZero() {
		cond += " AND " + s + ".updated_at >= ?"
		args = append(args, f.Since.UnixNano())
	}
	return cond, args
}

// timeline lists sessions newest first (spec §2.2).
func (d *DB) timeline(f Filter, limit, offset int) ([]Row, error) {
	cond, args := filterSQL(f, "s")
	if limit < 1 {
		limit = -1
	}
	args = append(args, limit, max(offset, 0))
	rows, err := d.db.Query(`SELECT `+sessionCols+` FROM sessions s WHERE 1`+cond+
		` ORDER BY s.updated_at DESC, s.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// sessionHit is one ranked session of a query.
type sessionHit struct {
	id        int64
	updated   int64
	mask      uint64  // terms matched anywhere
	titleMask uint64  // terms matched in the title or project name
	score     float64 // sum over terms of the best message bm25 (lower is better)
	hitMsg    int64   // best message id, 0 = none
	hitSeq    int
	hitMask   uint64
	hitScore  float64
}

// searchCache is the ranking of the last query.
type searchCache struct {
	gen    uint64
	filter Filter
	hits   []sessionHit
}

func (d *DB) invalidate() {
	d.cacheMu.Lock()
	d.gen++
	d.cache, d.metas = nil, nil
	d.cacheMu.Unlock()
}

func sameFilter(a, b Filter) bool {
	return a.Query == b.Query && a.Agent == b.Agent && a.Since.Equal(b.Since)
}

// ftsString quotes a term as an FTS5 string: a phrase of its trigrams,
// which matches it as a substring.
func ftsString(text string) string {
	return `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
}

// rank runs the per-term queries and ranks the matching sessions.
//
// An fts_msg rowid is a messages.id, which encodes the session and the seq
// (msgID), and the filters are applied to an in-memory table of sessions, so
// the per-term queries touch nothing but the full-text index.
func (d *DB) rank(f Filter, terms []search.Term) ([]sessionHit, error) {
	d.cacheMu.Lock()
	gen := d.gen
	if c := d.cache; c != nil && c.gen == gen && sameFilter(c.filter, f) {
		d.cacheMu.Unlock()
		return c.hits, nil
	}
	d.cacheMu.Unlock()

	metas, err := d.sessionMetas()
	if err != nil {
		return nil, err
	}
	total := 0 // messages in the archive
	for _, m := range metas {
		total += m.messages
	}
	since := int64(0)
	if !f.Since.IsZero() {
		since = f.Since.UnixNano()
	}
	sessions := map[int64]*sessionHit{}
	// get returns the hit of a session that passes the filters, or nil.
	get := func(id int64) *sessionHit {
		if h := sessions[id]; h != nil {
			return h
		}
		m, ok := metas[id]
		if !ok || (f.Agent != "" && m.agent != f.Agent) || m.updated < since {
			return nil // filtered out (or written after metas was read)
		}
		h := &sessionHit{id: id, updated: m.updated, hitSeq: -1}
		sessions[id] = h
		return h
	}
	type msgHit struct {
		mask  uint64
		score float64
	}
	msgs := map[int64]msgHit{} // by message id

	for i, t := range terms {
		bit := uint64(1) << i
		if !t.Short {
			ids, err := d.matchIDs(t.Text)
			if err != nil {
				return nil, err
			}
			scores, err := d.bm25(t.Text, len(ids), total)
			if err != nil {
				return nil, err
			}
			best := map[*sessionHit]float64{}
			for _, id := range ids {
				sid, _ := splitMsgID(id)
				h := get(sid)
				if h == nil {
					continue
				}
				score := scores[id]
				m := msgs[id]
				m.mask |= bit
				m.score += score
				msgs[id] = m
				h.mask |= bit
				if b, ok := best[h]; !ok || score < b {
					best[h] = score
				}
			}
			for h, b := range best {
				h.score += b
			}
		}
		q, arg := `SELECT rowid FROM fts_title WHERE fts_title MATCH ?`, ftsString(t.Text)
		if t.Short {
			q, arg = `SELECT id FROM sessions WHERE instr(norm_title, ?) > 0`, t.Text
		}
		err := d.each(q, []any{arg}, func(rows *sql.Rows) error {
			var sid int64
			if err := rows.Scan(&sid); err != nil {
				return err
			}
			if h := get(sid); h != nil {
				h.mask |= bit
				h.titleMask |= bit
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// The hit message of each session: most terms, then best bm25, then first.
	for id, m := range msgs {
		sid, seq := splitMsgID(id)
		h := sessions[sid]
		n, hn := bits.OnesCount64(m.mask), bits.OnesCount64(h.hitMask)
		if h.hitMsg == 0 || n > hn || (n == hn && (m.score < h.hitScore || (m.score == h.hitScore && seq < h.hitSeq))) {
			h.hitMsg, h.hitSeq, h.hitMask, h.hitScore = id, seq, m.mask, m.score
		}
	}

	hits := make([]sessionHit, 0, len(sessions))
	for _, h := range sessions {
		hits = append(hits, *h)
	}
	sort.Slice(hits, func(i, j int) bool {
		a, b := &hits[i], &hits[j]
		if x, y := bits.OnesCount64(a.mask), bits.OnesCount64(b.mask); x != y {
			return x > y
		}
		if x, y := bits.OnesCount64(a.titleMask), bits.OnesCount64(b.titleMask); x != y {
			return x > y
		}
		if a.score != b.score {
			return a.score < b.score
		}
		if a.updated != b.updated {
			return a.updated > b.updated
		}
		return a.id > b.id
	})

	d.cacheMu.Lock()
	if d.gen == gen {
		d.cache = &searchCache{gen: gen, filter: f, hits: hits}
	}
	d.cacheMu.Unlock()
	return hits, nil
}

// matchIDs returns the ids of the messages that contain a term.
func (d *DB) matchIDs(term string) ([]int64, error) {
	var ids []int64
	err := d.each(`SELECT rowid FROM fts_msg WHERE fts_msg MATCH ?`, []any{ftsString(term)}, func(rows *sql.Rows) error {
		var id int64
		err := rows.Scan(&id)
		ids = append(ids, id)
		return err
	})
	return ids, err
}

// A term found in more than bm25MaxShare of all messages, and in more than
// bm25MinMatches of them, gets no bm25 score. BM25 barely weighs such a term
// (its IDF is below 0.8 at 30% against 4.6 at 1%), while computing bm25 for
// each of its matches is most of the cost of a query in a large archive
// (about 120 ms per 75,000 matches); its sessions are then ordered by the
// other terms and by recency.
const (
	bm25MaxShare   = 0.25
	bm25MinMatches = 5000
)

// bm25 returns the bm25 score of every message that contains a term, or
// nil for a very common term (see bm25MaxShare).
func (d *DB) bm25(term string, matches, total int) (map[int64]float64, error) {
	if matches == 0 || (matches > bm25MinMatches && float64(matches) > bm25MaxShare*float64(total)) {
		return nil, nil
	}
	scores := make(map[int64]float64, matches)
	err := d.each(`SELECT rowid, bm25(fts_msg) FROM fts_msg WHERE fts_msg MATCH ?`, []any{ftsString(term)},
		func(rows *sql.Rows) error {
			var id int64
			var score float64
			err := rows.Scan(&id, &score)
			scores[id] = score
			return err
		})
	return scores, err
}

// sessionMeta is what ranking needs to know about a session.
type sessionMeta struct {
	agent    string
	updated  int64
	messages int
}

// sessionMetas returns the agent and updated_at of every session, cached
// until the next write.
func (d *DB) sessionMetas() (map[int64]sessionMeta, error) {
	d.cacheMu.Lock()
	gen, m := d.gen, d.metas
	d.cacheMu.Unlock()
	if m != nil {
		return m, nil
	}
	m = map[int64]sessionMeta{}
	err := d.each(`SELECT id, agent, updated_at, message_count FROM sessions`, nil, func(rows *sql.Rows) error {
		var id int64
		var sm sessionMeta
		err := rows.Scan(&id, &sm.agent, &sm.updated, &sm.messages)
		m[id] = sm
		return err
	})
	if err != nil {
		return nil, err
	}
	d.cacheMu.Lock()
	if d.gen == gen {
		d.metas = m
	}
	d.cacheMu.Unlock()
	return m, nil
}

// each runs a query and calls fn for every row.
func (d *DB) each(q string, args []any, fn func(*sql.Rows) error) error {
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// inChunk bounds the ids bound in one "IN (...)" list.
const inChunk = 500

// byIDs runs "q IN (ids…)" in chunks and calls fn for every row.
func (d *DB) byIDs(q string, ids []int64, fn func(*sql.Rows) error) error {
	for len(ids) > 0 {
		n := min(len(ids), inChunk)
		args := make([]any, n)
		for i, id := range ids[:n] {
			args[i] = id
		}
		if err := d.each(q+` IN (?`+strings.Repeat(`, ?`, n-1)+`)`, args, fn); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

// pageRows loads the rows of ranked hits and cuts their snippets.
func (d *DB) pageRows(page []sessionHit, terms []search.Term) ([]Row, error) {
	ids := make([]int64, len(page))
	var msgIDs []int64
	for i, h := range page {
		ids[i] = h.id
		if h.hitMsg != 0 {
			msgIDs = append(msgIDs, h.hitMsg)
		}
	}
	byID := make(map[int64]Row, len(page))
	err := d.byIDs(`SELECT `+sessionCols+` FROM sessions WHERE id`, ids, func(rows *sql.Rows) error {
		r, err := scanRow(rows)
		byID[r.SessionID] = r
		return err
	})
	if err != nil {
		return nil, err
	}
	text := make(map[int64]string, len(msgIDs))
	err = d.byIDs(`SELECT id, text FROM messages WHERE id`, msgIDs, func(rows *sql.Rows) error {
		var id int64
		var s string
		err := rows.Scan(&id, &s)
		text[id] = s
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(page))
	for _, h := range page {
		r, ok := byID[h.id]
		if !ok {
			continue // cannot happen: sessions are never deleted
		}
		r.MatchedTerms, r.TotalTerms = bits.OnesCount64(h.mask), len(terms)
		if h.hitMsg != 0 {
			r.HitSeq = h.hitSeq
			r.Snippet, _ = search.Snippet(text[h.hitMsg], terms, SnippetWidth)
		}
		out = append(out, r)
	}
	return out, nil
}

// HitSeqs returns the seqs of messages in the session that match any query term (for the reading page).
// Like the index, it matches terms of 3 or more characters in the normalized
// text (tool outputs: their first 2 KB); shorter terms only match titles.
func (d *DB) HitSeqs(sessionID int64, query string) ([]int, error) {
	var terms []string
	for _, t := range search.Parse(query) {
		if !t.Short {
			terms = append(terms, t.Text)
		}
	}
	if len(terms) == 0 {
		return nil, nil
	}
	var out []int
	lo, hi := msgRange(sessionID)
	err := d.each(`SELECT seq, kind, text FROM messages WHERE id BETWEEN ? AND ? ORDER BY id`, []any{lo, hi},
		func(rows *sql.Rows) error {
			var seq int
			var kind model.Kind
			var text string
			if err := rows.Scan(&seq, &kind, &text); err != nil {
				return err
			}
			body := indexBody(kind, text)
			for _, t := range terms {
				if strings.Contains(body, t) {
					out = append(out, seq)
					break
				}
			}
			return nil
		})
	return out, err
}
