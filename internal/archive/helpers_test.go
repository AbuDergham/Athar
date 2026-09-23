package archive

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

// t0 is the base time of the synthetic fixtures.
var t0 = time.Date(2026, 9, 20, 18, 30, 0, 0, time.UTC)

// openTemp opens a fresh archive in a temp dir (closed at cleanup).
func openTemp(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "athar-data", "archive.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	d.home = filepath.Join(dir, "home")
	t.Cleanup(func() { d.Close() })
	return d
}

// src is a SourceRecord on machine M1 for the claude adapter.
func src(path string) SourceRecord {
	return SourceRecord{Machine: "M1", Adapter: "claude", Path: path, Fingerprint: "fp-" + path, AdapterVersion: 1}
}

// msgs builds alternating user/assistant text messages one minute apart.
func msgs(texts ...string) []model.Message {
	out := make([]model.Message, len(texts))
	for i, s := range texts {
		role := model.RoleUser
		if i%2 == 1 {
			role = model.RoleAssistant
		}
		out[i] = model.Message{Role: role, Kind: model.KindText, At: t0.Add(time.Duration(i) * time.Minute), Text: s}
	}
	return out
}

// live builds a full live session.
func live(id, title string, texts ...string) model.Session {
	m := msgs(texts...)
	s := model.Session{
		Agent: "claude", NativeID: id, Cwd: `C:\work\` + id,
		Title: title, TitleSource: model.TitleAgent,
		StartedAt: t0, UpdatedAt: t0.Add(time.Duration(len(m)) * time.Minute),
		Coverage: model.CoverageFull, Origin: model.OriginLive, Messages: m,
	}
	if title == "" {
		s.TitleSource = model.TitleNone
	}
	return s
}

func mustApply(t *testing.T, d *DB, rec SourceRecord, ss ...model.Session) ApplyReport {
	t.Helper()
	rep, err := d.ApplySource(rec, ss)
	if err != nil {
		t.Fatalf("ApplySource(%s): %v", rec.Path, err)
	}
	return rep
}

func mustSearch(t *testing.T, d *DB, f Filter) []Row {
	t.Helper()
	rows, err := d.Search(f, 1000, 0)
	if err != nil {
		t.Fatalf("Search(%+v): %v", f, err)
	}
	return rows
}

// rowByID finds the row of a native id.
func rowByID(rows []Row, id string) (Row, bool) {
	for _, r := range rows {
		if r.NativeID == id {
			return r, true
		}
	}
	return Row{}, false
}

// ids lists the native ids of rows in order.
func ids(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.NativeID
	}
	return out
}

// texts returns the message texts of a session.
func texts(t *testing.T, d *DB, sessionID int64) []string {
	t.Helper()
	ms, err := d.Messages(sessionID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text
	}
	return out
}

// session returns the timeline row of a native id, failing when absent.
func session(t *testing.T, d *DB, id string) Row {
	t.Helper()
	r, ok := rowByID(mustSearch(t, d, Filter{}), id)
	if !ok {
		t.Fatalf("session %s not in the archive", id)
	}
	return r
}
