package archive

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
	"github.com/AbuDergham/Athar/internal/search"
)

// ShrinkWarning is the Sources-screen note of spec §4.4.
const ShrinkWarning = "source shrank, archived copy kept"

// ApplyReport summarizes one ApplySource call.
type ApplyReport struct {
	Inserted, Updated int
	Shrunk            int // sessions where the source had fewer messages; archived copy kept (§4.4)
}

// ApplySource stores the sessions read from one source in ONE transaction,
// following the never-delete and dedup rules of spec §4.4–§4.5:
//   - one session per (agent, native_id); live beats imports; fuller coverage fills gaps
//   - Cwd, once stored non-empty, is never overwritten by a later read of the same source
//   - messages: if the new list has the stored list as prefix (by role+text hash) it replaces it;
//     otherwise stored messages are kept and unseen new ones are appended (Shrunk++)
//   - the session is (re)attached to this source and source_missing_at is cleared
//   - ProjectPath/ProjectName are computed once, at first insert (spec §3.4)
//
// In detail, with origins ranked live > import > prompt-log:
//   - A copy from a lower origin than the archived session (a prompt log or
//     an import of a session read live) never touches its messages; it only
//     fills an empty Cwd, a better title and the time span.
//   - A copy from a higher origin takes over: its messages are appended when
//     the archived ones are its prefix, and otherwise replace them when it has
//     at least as many (a transcript replacing the prompts of a prompt log, so
//     the result does not depend on which source is read first); with fewer
//     messages the archived ones are kept and the new ones appended (not
//     counted as a shrink: no source shrank).
//   - A copy of the same origin follows §4.4 when it comes from the session's
//     own source, or from another source whose copy is at least as recent
//     (the session moved, or this is the newest of several live copies). An
//     older copy elsewhere only fills gaps, like a lower origin.
//   - Title: a better TitleSource (user > agent > prompt) always wins; an
//     equal one is replaced by the copy that takes over. Times: StartedAt is
//     the earliest, UpdatedAt the latest seen. Coverage and origin: the best.
//   - Machine, Root and the session's source follow the copy that takes over.
//     The session is no longer missing once a copy at least as good as the
//     archived one is read.
//   - Sessions without a NativeID are skipped (noted in the source warnings);
//     an empty Agent means the source's adapter.
func (d *DB) ApplySource(rec SourceRecord, sessions []model.Session) (ApplyReport, error) {
	var rep ApplyReport
	err := d.inTx(func(tx *sql.Tx) error {
		rep = ApplyReport{}
		a := &applier{d: d, tx: tx, rec: rec}
		if err := a.prepare(); err != nil {
			return err
		}
		defer a.close()
		if err := a.upsertSource(); err != nil {
			return err
		}
		contained := map[int64]bool{}
		noID := 0
		for i := range sessions {
			s := normalizeSession(sessions[i], rec.Adapter)
			if s.NativeID == "" {
				noID++
				continue
			}
			res, err := a.apply(&s)
			if err != nil {
				return fmt.Errorf("session %s/%s: %w", s.Agent, s.NativeID, err)
			}
			contained[res.id] = true
			switch {
			case res.inserted:
				rep.Inserted++
			case res.changed:
				rep.Updated++
			}
			if res.shrunk {
				rep.Shrunk++
			}
		}
		if err := a.dropStaleLinks(contained); err != nil {
			return err
		}
		warnings := append([]string(nil), rec.Warnings...)
		if noID > 0 {
			warnings = append(warnings, fmt.Sprintf("%d sessions without an id skipped", noID))
		}
		if rep.Shrunk > 0 {
			warnings = append(warnings, fmt.Sprintf("%s (%d sessions)", ShrinkWarning, rep.Shrunk))
		}
		if a.tooLong > 0 {
			warnings = append(warnings, fmt.Sprintf("%d sessions have more than %d messages; the rest were not archived", a.tooLong, maxSeq+1))
		}
		_, err := tx.Exec(`UPDATE sources SET warnings = ? WHERE id = ?`, encodeWarnings(warnings), a.srcID)
		return err
	})
	return rep, err
}

// FinishSync marks sessions missing whose sources on this machine were not seen
// in the sync that just ended (seenPaths), and are not contained in any other live source.
// It returns the number of sessions newly marked missing.
//
// Sources of this machine not in seenPaths are flagged missing too (kept,
// not listed by Sources); seen ones are unflagged. A session counts as
// present when a present source of this machine holds a copy whose origin is
// at least as good as the session's (so a prompt log does not keep a session
// whose transcript was deleted from being marked). Sessions last seen live on
// another machine are never marked.
func (d *DB) FinishSync(machine string, seenPaths map[string]bool, at time.Time) (int, error) {
	var n int
	err := d.inTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, path, missing_at IS NOT NULL FROM sources WHERE machine = ?`, machine)
		if err != nil {
			return err
		}
		type src struct {
			id      int64
			missing bool
		}
		var gone, back []int64
		for rows.Next() {
			var s src
			var path string
			if err := rows.Scan(&s.id, &path, &s.missing); err != nil {
				rows.Close()
				return err
			}
			switch seen := seenPaths[path]; {
			case seen && s.missing:
				back = append(back, s.id)
			case !seen && !s.missing:
				gone = append(gone, s.id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range back {
			if _, err := tx.Exec(`UPDATE sources SET missing_at = NULL WHERE id = ?`, id); err != nil {
				return err
			}
		}
		for _, id := range gone {
			if _, err := tx.Exec(`UPDATE sources SET missing_at = ? WHERE id = ?`, at.UnixNano(), id); err != nil {
				return err
			}
		}
		// A source that came back (Recycle Bin restore, drive plugged in again,
		// a listing that failed silently) is usually unchanged and not re-read:
		// its sessions are present again.
		if _, err := tx.Exec(`UPDATE sessions SET source_missing_at = NULL
			WHERE machine = ? AND source_missing_at IS NOT NULL AND EXISTS (
				SELECT 1 FROM session_sources l JOIN sources s ON s.id = l.source_id
				WHERE l.session_id = sessions.id AND s.machine = ? AND s.missing_at IS NULL
				  AND `+rankSQL("l.origin")+` >= `+rankSQL("sessions.origin")+`)`, machine, machine); err != nil {
			return err
		}
		res, err := tx.Exec(`UPDATE sessions SET source_missing_at = ?
			WHERE machine = ? AND source_missing_at IS NULL AND NOT EXISTS (
				SELECT 1 FROM session_sources l JOIN sources s ON s.id = l.source_id
				WHERE l.session_id = sessions.id AND s.machine = ? AND s.missing_at IS NULL
				  AND `+rankSQL("l.origin")+` >= `+rankSQL("sessions.origin")+`)`,
			at.UnixNano(), machine, machine)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		n = int(affected)
		return err
	})
	return n, err
}

// originRank orders origins: live > imports > prompt log.
func originRank(o model.Origin) int {
	switch o {
	case model.OriginLive:
		return 3
	case model.OriginPromptLog:
		return 1
	default: // import:agentsview, import:psm, future imports
		return 2
	}
}

// rankSQL is originRank as an SQL expression over column col.
func rankSQL(col string) string {
	return `(CASE ` + col + ` WHEN 'live' THEN 3 WHEN 'prompt-log' THEN 1 ELSE 2 END)`
}

// titleRank orders title sources: user > agent > prompt > none.
func titleRank(ts model.TitleSource) int {
	switch ts {
	case model.TitleUser:
		return 3
	case model.TitleAgent:
		return 2
	case model.TitlePrompt:
		return 1
	default:
		return 0
	}
}

// normalizeSession fills defaults an adapter may leave empty: the agent
// (the source's adapter), origin live, coverage meta, a title source that
// matches the title, message kind text, UTC times, and times taken from the
// messages when the session has none. The caller's messages are not modified.
func normalizeSession(s model.Session, adapter string) model.Session {
	if s.Agent == "" {
		s.Agent = adapter
	}
	if s.Origin == "" {
		s.Origin = model.OriginLive
	}
	if s.Coverage == "" {
		s.Coverage = model.CoverageMeta
	}
	switch {
	case s.Title == "":
		s.TitleSource = model.TitleNone
	case s.TitleSource == "" || s.TitleSource == model.TitleNone:
		s.TitleSource = model.TitlePrompt
	}
	for i, m := range s.Messages {
		if m.Kind == "" {
			ms := append([]model.Message(nil), s.Messages...)
			for j := i; j < len(ms); j++ {
				if ms[j].Kind == "" {
					ms[j].Kind = model.KindText
				}
			}
			s.Messages = ms
			break
		}
	}
	var first, last time.Time
	for _, m := range s.Messages {
		if m.At.IsZero() {
			continue
		}
		if first.IsZero() || m.At.Before(first) {
			first = m.At
		}
		if m.At.After(last) {
			last = m.At
		}
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = first
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = last
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = s.UpdatedAt
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = s.StartedAt
	}
	s.StartedAt, s.UpdatedAt = s.StartedAt.UTC(), s.UpdatedAt.UTC()
	return s
}

// hashLen is the number of sha256 bytes kept in messages.text_hash.
const hashLen = 16

// msgHash identifies a message's content: sha256(role|kind|text)[:16]
// (spec §4.4: messages match by role + text hash).
type msgHash [hashLen]byte

func messageHash(m model.Message) msgHash {
	h := sha256.New()
	h.Write([]byte(m.Role))
	h.Write([]byte{'|'})
	h.Write([]byte(m.Kind))
	h.Write([]byte{'|'})
	h.Write([]byte(m.Text))
	var out msgHash
	copy(out[:], h.Sum(nil))
	return out
}

// indexBody is the text of a message as indexed in fts_msg (spec §5.1):
// normalized, and for tool outputs only the first 2 KB.
func indexBody(kind model.Kind, text string) string {
	if kind == model.KindToolOutput && len(text) > model.MaxToolOutput {
		cut := model.MaxToolOutput
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return search.IndexText(text)
}

// titleBody is the text indexed in fts_title and sessions.norm_title:
// the normalized title and project name ((no project) is not indexed).
func titleBody(title, projectName string) string {
	if projectName == NoProject {
		projectName = ""
	}
	switch {
	case title == "":
		return search.IndexText(projectName)
	case projectName == "":
		return search.IndexText(title)
	}
	return search.IndexText(title + " " + projectName)
}

// applier holds the prepared statements of one ApplySource transaction.
type applier struct {
	d       *DB
	tx      *sql.Tx
	rec     SourceRecord
	srcID   int64
	tooLong int // sessions that reached maxSeq

	insMsg, insFTS, getHashes, link *sql.Stmt
}

func (a *applier) prepare() error {
	var err error
	prep := func(q string) *sql.Stmt {
		if err != nil {
			return nil
		}
		var st *sql.Stmt
		st, err = a.tx.Prepare(q)
		return st
	}
	a.insMsg = prep(`INSERT INTO messages(id, session_id, seq, role, kind, at, text, text_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	a.insFTS = prep(`INSERT INTO fts_msg(rowid, body) VALUES (?, ?)`)
	a.getHashes = prep(`SELECT text_hash FROM messages WHERE id BETWEEN ? AND ? ORDER BY id`)
	a.link = prep(`INSERT INTO session_sources(session_id, source_id, origin) VALUES (?, ?, ?)
		ON CONFLICT(session_id, source_id) DO UPDATE SET origin = excluded.origin`)
	return err
}

func (a *applier) close() {
	for _, st := range []*sql.Stmt{a.insMsg, a.insFTS, a.getHashes, a.link} {
		if st != nil {
			st.Close()
		}
	}
}

// upsertSource records a successful read of the source.
func (a *applier) upsertSource() error {
	r := a.rec
	return a.tx.QueryRow(`INSERT INTO sources(machine, adapter, path, fingerprint, adapter_version, last_ok_at, skipped)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(machine, path) DO UPDATE SET adapter = excluded.adapter, fingerprint = excluded.fingerprint,
			adapter_version = excluded.adapter_version, last_ok_at = excluded.last_ok_at, skipped = excluded.skipped,
			last_error = '', last_error_at = NULL, missing_at = NULL
		RETURNING id`,
		r.Machine, r.Adapter, r.Path, r.Fingerprint, r.AdapterVersion, time.Now().UnixNano(), r.Skipped).Scan(&a.srcID)
}

// dropStaleLinks forgets that this source contains sessions it no longer
// returned (their archived copies stay; FinishSync decides if they are missing).
func (a *applier) dropStaleLinks(contained map[int64]bool) error {
	rows, err := a.tx.Query(`SELECT session_id FROM session_sources WHERE source_id = ?`, a.srcID)
	if err != nil {
		return err
	}
	var stale []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !contained[id] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := a.tx.Exec(`DELETE FROM session_sources WHERE session_id = ? AND source_id = ?`, id, a.srcID); err != nil {
			return err
		}
	}
	return nil
}

// storedSession is the archived state of a session that ApplySource merges into.
type storedSession struct {
	cwd, projectPath, projectName string
	title                         string
	titleSource                   model.TitleSource
	started, updated              int64
	count                         int
	coverage                      model.Coverage
	origin                        model.Origin
	root, machine                 string
	sourceID                      sql.NullInt64
	missing                       sql.NullInt64
}

type applyResult struct {
	id                        int64
	inserted, changed, shrunk bool
}

// apply stores one (normalized) session.
func (a *applier) apply(s *model.Session) (applyResult, error) {
	machine := s.Machine
	if machine == "" {
		machine = a.rec.Machine
	}
	var res applyResult
	var st storedSession
	err := a.tx.QueryRow(`SELECT id, cwd, project_path, project_name, title, title_source, started_at, updated_at,
			message_count, coverage, origin, root, machine, source_id, source_missing_at
		FROM sessions WHERE agent = ? AND native_id = ?`, s.Agent, s.NativeID).Scan(
		&res.id, &st.cwd, &st.projectPath, &st.projectName, &st.title, &st.titleSource, &st.started, &st.updated,
		&st.count, &st.coverage, &st.origin, &st.root, &st.machine, &st.sourceID, &st.missing)
	if errors.Is(err, sql.ErrNoRows) {
		return a.insert(s, machine)
	}
	if err != nil {
		return res, err
	}

	ri, rs := originRank(s.Origin), originRank(st.origin)
	sameSource := st.sourceID.Valid && st.sourceID.Int64 == a.srcID
	takeOver := ri > rs || (ri == rs && (sameSource || st.missing.Valid || toNanos(s.UpdatedAt) >= st.updated))

	// Messages.
	count := st.count
	if takeOver {
		// An import is a snapshot: one older than the archived copy (indexed
		// before the last prompts of a prompt log) may not replace it.
		replace := ri > rs && (s.Origin == model.OriginLive || toNanos(s.UpdatedAt) >= st.updated)
		n, shrunk, err := a.mergeMessages(res.id, s.Messages, ri > rs, replace)
		if err != nil {
			return res, err
		}
		count, res.shrunk = n, shrunk
	} else if ri < rs && len(s.Messages) > st.count {
		// §4.5: a lower-origin copy (an import) fills a session that has
		// fewer messages when the archived ones are its prefix.
		n, err := a.extend(res.id, s.Messages)
		if err != nil {
			return res, err
		}
		count = n
	}

	// Session fields.
	nw := st
	nw.count = count
	if s.Title != "" && (titleRank(s.TitleSource) > titleRank(st.titleSource) ||
		(titleRank(s.TitleSource) == titleRank(st.titleSource) && takeOver)) {
		nw.title, nw.titleSource = s.Title, s.TitleSource
	}
	if st.cwd == "" && s.Cwd != "" {
		nw.cwd = s.Cwd
		nw.projectPath, nw.projectName = a.d.proj.of(s.Cwd, a.d.home)
	}
	if t := toNanos(s.StartedAt); t != 0 && (nw.started == 0 || t < nw.started) {
		nw.started = t
	}
	if t := toNanos(s.UpdatedAt); t > nw.updated {
		nw.updated = t
	}
	if s.Coverage.Rank() > st.coverage.Rank() {
		nw.coverage = s.Coverage
	}
	if ri > rs {
		nw.origin = s.Origin
	}
	if takeOver {
		nw.sourceID = sql.NullInt64{Int64: a.srcID, Valid: true}
		nw.root, nw.machine = s.Root, machine
	}
	if ri >= rs {
		nw.missing = sql.NullInt64{}
	}

	if nw != st {
		res.changed = true
		if _, err := a.tx.Exec(`UPDATE sessions SET cwd = ?, project_path = ?, project_name = ?, title = ?, title_source = ?,
				norm_title = ?, started_at = ?, updated_at = ?, message_count = ?, coverage = ?, origin = ?, root = ?,
				machine = ?, source_id = ?, source_missing_at = ?
			WHERE id = ?`,
			nw.cwd, nw.projectPath, nw.projectName, nw.title, nw.titleSource, titleBody(nw.title, nw.projectName),
			nw.started, nw.updated, nw.count, nw.coverage, nw.origin, nw.root, nw.machine, nw.sourceID, nw.missing,
			res.id); err != nil {
			return res, err
		}
		if nw.title != st.title || nw.projectName != st.projectName {
			if err := a.setTitleIndex(res.id, titleBody(nw.title, nw.projectName), true); err != nil {
				return res, err
			}
		}
	}
	_, err = a.link.Exec(res.id, a.srcID, s.Origin)
	return res, err
}

// insert stores a session seen for the first time.
func (a *applier) insert(s *model.Session, machine string) (applyResult, error) {
	res := applyResult{inserted: true}
	path, name := a.d.proj.of(s.Cwd, a.d.home)
	body := titleBody(s.Title, name)
	err := a.tx.QueryRow(`INSERT INTO sessions(agent, native_id, machine, cwd, project_path, project_name, title,
			title_source, norm_title, started_at, updated_at, message_count, coverage, origin, root, source_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		s.Agent, s.NativeID, machine, s.Cwd, path, name, s.Title, s.TitleSource, body,
		toNanos(s.StartedAt), toNanos(s.UpdatedAt), min(len(s.Messages), maxSeq+1), s.Coverage, s.Origin, s.Root, a.srcID).Scan(&res.id)
	if err != nil {
		return res, err
	}
	if err := a.insertMessages(res.id, 0, s.Messages); err != nil {
		return res, err
	}
	if err := a.setTitleIndex(res.id, body, false); err != nil {
		return res, err
	}
	_, err = a.link.Exec(res.id, a.srcID, s.Origin)
	return res, err
}

// setTitleIndex (re)writes the fts_title row of a session.
func (a *applier) setTitleIndex(id int64, body string, replace bool) error {
	if replace {
		if _, err := a.tx.Exec(`DELETE FROM fts_title WHERE rowid = ?`, id); err != nil {
			return err
		}
	}
	if body == "" {
		return nil
	}
	_, err := a.tx.Exec(`INSERT INTO fts_title(rowid, body) VALUES (?, ?)`, id, body)
	return err
}

// mergeMessages merges incoming messages into a session's archived ones
// (see ApplySource) and returns the new message count and whether the
// archived copy was kept because the source no longer extends it.
// upgrade reports a copy from a better origin than the archived one.
func (a *applier) mergeMessages(sessionID int64, in []model.Message, upgrade, replace bool) (int, bool, error) {
	stored, err := a.hashes(sessionID)
	if err != nil {
		return 0, false, err
	}
	hashes := make([]msgHash, len(in))
	for i, m := range in {
		hashes[i] = messageHash(m)
	}
	if isPrefix(stored, hashes) {
		return min(len(in), maxSeq+1), false, a.insertMessages(sessionID, len(stored), in[len(stored):])
	}
	if replace && len(in) >= len(stored) {
		// A transcript replacing the prompts of a prompt log, or an import:
		// the better copy holds (at least as much of) the same conversation.
		lo, hi := msgRange(sessionID)
		if _, err := a.tx.Exec(`DELETE FROM fts_msg WHERE rowid IN (SELECT id FROM messages WHERE id BETWEEN ? AND ?)`, lo, hi); err != nil {
			return 0, false, err
		}
		if _, err := a.tx.Exec(`DELETE FROM messages WHERE id BETWEEN ? AND ?`, lo, hi); err != nil {
			return 0, false, err
		}
		return min(len(in), maxSeq+1), false, a.insertMessages(sessionID, 0, in)
	}
	// Keep every archived message; append the incoming ones not archived yet
	// (as a multiset: a repeated "yes" is appended as often as it is new).
	have := make(map[msgHash]int, len(stored))
	for _, h := range stored {
		have[h]++
	}
	var add []model.Message
	for i, h := range hashes {
		if have[h] > 0 {
			have[h]--
			continue
		}
		add = append(add, in[i])
	}
	return min(len(stored)+len(add), maxSeq+1), !upgrade, a.insertMessages(sessionID, len(stored), add)
}

// extend appends the messages of in beyond the archived ones when the
// archived ones are a prefix of in; otherwise it changes nothing. It returns
// the message count.
func (a *applier) extend(sessionID int64, in []model.Message) (int, error) {
	stored, err := a.hashes(sessionID)
	if err != nil {
		return 0, err
	}
	hashes := make([]msgHash, len(in))
	for i, m := range in {
		hashes[i] = messageHash(m)
	}
	if !isPrefix(stored, hashes) {
		return len(stored), nil
	}
	return min(len(in), maxSeq+1), a.insertMessages(sessionID, len(stored), in[len(stored):])
}

// hashes returns the archived message hashes of a session in seq order.
func (a *applier) hashes(sessionID int64) ([]msgHash, error) {
	lo, hi := msgRange(sessionID)
	rows, err := a.getHashes.Query(lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []msgHash
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var h msgHash
		copy(h[:], b)
		out = append(out, h)
	}
	return out, rows.Err()
}

// isPrefix reports whether prefix is a prefix of list.
func isPrefix(prefix, list []msgHash) bool {
	if len(prefix) > len(list) {
		return false
	}
	for i := range prefix {
		if prefix[i] != list[i] {
			return false
		}
	}
	return true
}

// insertMessages appends messages with seqs from seq on, and indexes them.
// Messages beyond maxSeq are not stored (noted in the source warnings).
func (a *applier) insertMessages(sessionID int64, seq int, ms []model.Message) error {
	if over := seq + len(ms) - (maxSeq + 1); over > 0 {
		ms = ms[:max(0, len(ms)-over)]
		a.tooLong++
	}
	for i, m := range ms {
		h := messageHash(m)
		id := msgID(sessionID, seq+i)
		if _, err := a.insMsg.Exec(id, sessionID, seq+i, m.Role, m.Kind, nullNanos(m.At), m.Text, h[:]); err != nil {
			return err
		}
		if body := indexBody(m.Kind, m.Text); body != "" {
			if _, err := a.insFTS.Exec(id, body); err != nil {
				return err
			}
		}
	}
	return nil
}
