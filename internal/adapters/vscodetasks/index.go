package vscodetasks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/fsread"
)

// historyItem is one task history entry (Cline HistoryItem, Kilo/Roo HistoryItem).
type historyItem struct {
	ID        string          `json:"id"`
	Ts        json.RawMessage `json:"ts"`   // epoch ms of the last activity
	Task      string          `json:"task"` // the first prompt
	Workspace string          `json:"workspace"`
	CwdInit   string          `json:"cwdOnTaskInitialization"`
}

// cwd is the task's working folder.
func (h historyItem) cwd() string {
	if s := strings.TrimSpace(h.CwdInit); s != "" {
		return s
	}
	return strings.TrimSpace(h.Workspace)
}

// indexCache is one parsed index file, valid while its fingerprint holds.
type indexCache struct {
	fp    string
	items map[string]historyItem
}

// lookup finds the history entry of the task in taskDir, trying in order:
// <task>\history_item.json, <ext>\state\taskHistory.json,
// <ext>\globalState.json (Cline CLI), and the host's
// globalStorage\state.vscdb ItemTable[<ext>] (Kilo legacy, old Cline).
func (a *adapter) lookup(tempDir, taskDir, id string) (historyItem, bool) {
	if data, err := fsread.ReadFile(filepath.Join(taskDir, itemFile)); err == nil {
		var h historyItem
		if decode(data, &h) && (h.ID == "" || h.ID == id) && (h.Task != "" || h.cwd() != "") {
			return h, true
		}
	}
	extDir := filepath.Dir(filepath.Dir(taskDir))
	for _, p := range []string{
		filepath.Join(extDir, "state", "taskHistory.json"),
		filepath.Join(extDir, "globalState.json"),
	} {
		if h, ok := a.cached(p, []string{p}, func() map[string]historyItem { return jsonIndex(p) })[id]; ok {
			return h, true
		}
	}
	gs := filepath.Dir(extDir)
	if strings.EqualFold(filepath.Base(gs), "globalStorage") {
		db := filepath.Join(gs, "state.vscdb")
		key := filepath.Base(extDir)
		load := func() map[string]historyItem { return vscdbIndex(db, key, tempDir) }
		if h, ok := a.cached(db, fsread.SQLiteSidecars(db), load)[id]; ok {
			return h, true
		}
	}
	return historyItem{}, false
}

// cached returns the index stored under key, reloading it when the
// fingerprint of files changed. A missing index gives nil. A failed load
// (nil) is not cached, so it is tried again on the next Read.
func (a *adapter) cached(key string, files []string, load func() map[string]historyItem) map[string]historyItem {
	if _, err := os.Stat(key); err != nil {
		return nil
	}
	fp, err := fsread.Fingerprint(files)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	c, ok := a.index[key]
	a.mu.Unlock()
	if ok && c.fp == fp {
		return c.items
	}
	items := load()
	if items == nil {
		return nil
	}
	a.mu.Lock()
	if a.index == nil {
		a.index = map[string]indexCache{}
	}
	a.index[key] = indexCache{fp: fp, items: items}
	a.mu.Unlock()
	return items
}

// jsonIndex reads a task history JSON file: a list of entries, or an object
// with a taskHistory list. A torn or missing file gives nil.
func jsonIndex(path string) map[string]historyItem {
	data, err := fsread.ReadFile(path)
	if err != nil {
		return nil
	}
	return historyItems(data)
}

// historyItems decodes [entries] or {"taskHistory": [entries]}.
func historyItems(data []byte) map[string]historyItem {
	var list []json.RawMessage
	if !decode(data, &list) || list == nil {
		var obj struct {
			TaskHistory []json.RawMessage `json:"taskHistory"`
		}
		if !decode(data, &obj) {
			return nil
		}
		list = obj.TaskHistory
	}
	out := make(map[string]historyItem, len(list))
	for _, raw := range list {
		var h historyItem
		if decode(raw, &h) && h.ID != "" {
			out[h.ID] = h
		}
	}
	return out
}

// vscdbIndex reads ItemTable[key].taskHistory from a temp copy of a host's
// state.vscdb. Any failure gives nil (the task then falls back to its own data).
func vscdbIndex(db, key, tempDir string) map[string]historyItem {
	copyPath, cleanup, err := fsread.CopySQLite(db, tempDir)
	if err != nil {
		return nil
	}
	defer cleanup()
	conn, err := fsread.OpenSQLiteCopy(copyPath)
	if err != nil {
		return nil
	}
	defer conn.Close()
	var val []byte
	err = conn.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]historyItem{} // the extension keeps no index here
	}
	if err != nil {
		return nil
	}
	if items := historyItems(val); items != nil {
		return items
	}
	return map[string]historyItem{}
}

// itemTime is the entry's ts.
func (h historyItem) itemTime() time.Time { return msTime(h.Ts) }
