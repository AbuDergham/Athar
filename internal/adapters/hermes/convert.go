package hermes

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/model"
)

// headlessSources are session sources that are not conversations with the
// user: scheduled jobs, sub-agent runs, tool-created sessions, kanban workers.
var headlessSources = map[string]bool{"cron": true, "subagent": true, "tool": true, "kanban": true}

// resetEndReasons end a gateway conversation at a deliberate boundary; a
// child with the same session_key is a new conversation (Hermes'
// _RESET_END_REASONS).
var resetEndReasons = map[string]bool{
	"session_reset": true, "session_switch": true, "idle": true,
	"daily": true, "suspended": true, "resume_pending_expired": true,
}

// markers are the lineage markers Hermes writes into sessions.model_config.
type markers struct {
	branched, delegate, reset bool
}

func parseMarkers(raw string) markers {
	var m map[string]json.RawMessage
	if raw == "" || json.Unmarshal([]byte(raw), &m) != nil {
		return markers{}
	}
	set := func(k string) bool {
		v, ok := m[k]
		return ok && string(v) != "null"
	}
	return markers{branched: set("_branched_from"), delegate: set("_delegate_from"), reset: set("_reset_from")}
}

// edge says how a session relates to the conversation list.
type edge int

const (
	edgeSkip         edge = iota // not a conversation (delegate, sub-agent, headless)
	edgeRoot                     // a conversation of its own
	edgeContinuation             // merged into its parent (compression)
)

// classify mirrors Hermes' picker: roots, branch and reset children are
// listed; compression continuations belong to their parent; everything else
// with a parent is a sub-agent run.
func classify(s *sessionRow, byID map[string]*sessionRow) edge {
	if headlessSources[s.source] {
		return edgeSkip
	}
	mk := parseMarkers(s.modelConfig)
	if mk.delegate {
		return edgeSkip
	}
	if s.parent == "" {
		return edgeRoot
	}
	p, ok := byID[s.parent]
	if !ok || p == s {
		return edgeRoot // the parent row is gone: keep what is left
	}
	if mk.branched || mk.reset {
		return edgeRoot
	}
	if p.endReason == "branched" && p.endedAt > 0 && s.startedAt >= p.endedAt {
		return edgeRoot
	}
	if resetEndReasons[p.endReason] && s.sessionKey != "" && s.sessionKey == p.sessionKey {
		return edgeRoot
	}
	if p.endReason == "compression" {
		return edgeContinuation
	}
	return edgeSkip
}

// chainRoot follows compression edges up to the conversation's first session.
// ok=false when the chain leads to a skipped session.
func chainRoot(s *sessionRow, byID map[string]*sessionRow, kind map[string]edge) (*sessionRow, bool) {
	seen := map[string]bool{}
	for {
		switch kind[s.id] {
		case edgeRoot:
			return s, true
		case edgeSkip:
			return nil, false
		}
		if seen[s.id] {
			return nil, false // a cycle: no root
		}
		seen[s.id] = true
		s = byID[s.parent]
	}
}

// build turns the rows of one database into sessions.
func build(rows []sessionRow, msgs []messageRow, root string) adapters.ReadResult {
	var res adapters.ReadResult
	byID := make(map[string]*sessionRow, len(rows))
	for i := range rows {
		if rows[i].id != "" {
			byID[rows[i].id] = &rows[i]
		}
	}
	kind := make(map[string]edge, len(byID))
	for id, s := range byID {
		kind[id] = classify(s, byID)
	}
	// chains: root id → sessions of the conversation.
	chains := map[string][]*sessionRow{}
	for _, s := range byID {
		r, ok := chainRoot(s, byID, kind)
		if ok {
			chains[r.id] = append(chains[r.id], s)
		}
	}
	bySession := map[string][]messageRow{}
	for _, m := range msgs {
		bySession[m.sessionID] = append(bySession[m.sessionID], m)
	}

	ids := make([]string, 0, len(chains))
	for id := range chains {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		chain := chains[id]
		// The root first, then continuations in the order they started.
		sort.SliceStable(chain, func(i, j int) bool {
			if (chain[i].id == id) != (chain[j].id == id) {
				return chain[i].id == id
			}
			if chain[i].startedAt != chain[j].startedAt {
				return chain[i].startedAt < chain[j].startedAt
			}
			return chain[i].id < chain[j].id
		})
		se, skipped, ok := conversation(chain, bySession, root)
		res.Skipped += skipped
		if ok {
			res.Sessions = append(res.Sessions, se)
		}
	}
	return res
}

// conversation builds one session from a compression chain (root first).
// ok=false when nothing readable is left.
func conversation(chain []*sessionRow, bySession map[string][]messageRow, root string) (model.Session, int, bool) {
	first := chain[0]
	se := model.Session{
		Agent:    "hermes",
		NativeID: first.id,
		Coverage: model.CoverageFull,
		Origin:   model.OriginLive,
		Root:     root,
	}
	var rows []messageRow
	var last float64
	for _, s := range chain {
		if se.Cwd == "" {
			se.Cwd = s.cwd
		}
		rows = append(rows, bySession[s.id]...)
		last = math.Max(last, s.lastActivityAt)
	}
	skipped := 0
	for _, r := range displayRows(rows) {
		msgs, bad := convert(r)
		if bad {
			skipped++
		}
		if len(msgs) > 0 {
			se.Messages = append(se.Messages, msgs...)
			last = math.Max(last, r.timestamp)
		}
	}
	if len(se.Messages) == 0 {
		return model.Session{}, skipped, false
	}
	se.StartedAt = unix(first.startedAt)
	if se.StartedAt.IsZero() {
		se.StartedAt = earliest(se.Messages)
	}
	se.UpdatedAt = unix(last)
	if se.UpdatedAt.IsZero() || se.UpdatedAt.Before(se.StartedAt) {
		se.UpdatedAt = se.StartedAt
	}
	se.Title, se.TitleSource = title(chain, se.Messages)
	return se, skipped, true
}

// displayRows applies Hermes' display projection: live rows and rows archived
// by compaction (never rows taken back by undo/rewind), each logical message
// once — the live copy, else the newest — at the place of its first copy.
func displayRows(rows []messageRow) []messageRow {
	type key struct {
		role, content, toolCallID, toolCalls, toolName string
		ts                                             float64
	}
	chosen := map[key]int{} // key → index in out
	var out []messageRow
	for _, r := range rows {
		if !r.active && !r.compacted {
			continue
		}
		k := key{r.role, r.content, r.toolCallID, r.toolCalls, r.toolName, r.timestamp}
		if i, ok := chosen[k]; ok {
			cur := out[i]
			if r.active && !cur.active || r.active == cur.active && r.id > cur.id {
				out[i] = r
			}
			continue
		}
		chosen[k] = len(out)
		out = append(out, r)
	}
	return out
}

// unix converts Unix seconds to UTC; zero stays the zero time.
func unix(sec float64) time.Time {
	if sec <= 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return time.Time{}
	}
	whole, frac := math.Modf(sec)
	return time.Unix(int64(whole), int64(math.Round(frac*1e6))*1e3).UTC()
}

// earliest returns the first non-zero message time.
func earliest(msgs []model.Message) time.Time {
	var t time.Time
	for _, m := range msgs {
		if !m.At.IsZero() && (t.IsZero() || m.At.Before(t)) {
			t = m.At
		}
	}
	return t
}
