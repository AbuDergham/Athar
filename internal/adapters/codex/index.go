package codex

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// thread is what the Codex indexes know about one thread.
type thread struct {
	title string // a real thread name (never just the first prompt)
	cwd   string
}

// indexRetry is how long a failed state DB read is not retried while its
// files are unchanged (one bad copy must not slow every rollout of a sync).
const indexRetry = 30 * time.Second

// indexCache keeps the parsed title index of each Codex root. It is safe for
// concurrent use (sync may read several sources of one adapter in parallel).
type indexCache struct {
	mu    sync.Mutex
	roots map[string]*rootIndex
}

type rootIndex struct {
	fingerprint string
	threads     map[string]thread
	failedAt    time.Time // zero unless the state DB could not be read
}

func newIndexCache() *indexCache { return &indexCache{roots: map[string]*rootIndex{}} }

var stateDBName = regexp.MustCompile(`^state_(\d+)\.sqlite$`)

// newestStateDB returns the state_<N>.sqlite with the highest N in dir ("" if none).
// N grows with Codex schema generations; older files are stale.
func newestStateDB(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best, bestN := "", -1
	for _, e := range entries {
		m := stateDBName.FindStringSubmatch(strings.ToLower(e.Name()))
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > bestN {
			best, bestN = filepath.Join(dir, e.Name()), n
		}
	}
	return best
}

// get returns the thread index of a Codex root, rebuilding it when the index
// files changed. Problems are returned as warnings, never as errors: titles
// are optional, the rollouts are the source of truth.
func (c *indexCache) get(env adapters.Env, rootDir string) (map[string]thread, []string) {
	if rootDir == "" {
		return nil, nil
	}
	sessionIndex := filepath.Join(rootDir, "session_index.jsonl")
	files := []string{sessionIndex}
	db := newestStateDB(rootDir)
	if db != "" {
		files = append(files, fsread.SQLiteSidecars(db)...)
	}
	fp, err := fsread.Fingerprint(files)
	if err != nil {
		return nil, []string{fmt.Sprintf("Codex title index: %v", err)}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if ri := c.roots[rootDir]; ri != nil && ri.fingerprint == fp &&
		(ri.failedAt.IsZero() || time.Since(ri.failedAt) < indexRetry) {
		return ri.threads, nil
	}

	ri := &rootIndex{fingerprint: fp, threads: map[string]thread{}}
	var warnings []string
	if db != "" {
		if err := readStateDB(db, env.TempDir, ri.threads); err != nil {
			ri.failedAt = time.Now()
			warnings = append(warnings, fmt.Sprintf("Codex title index %s could not be read (titles fall back to first prompts): %v",
				filepath.Base(db), err))
		}
	}
	skipped, err := readSessionIndex(sessionIndex, ri.threads)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		warnings = append(warnings, fmt.Sprintf("Codex thread names (session_index.jsonl) could not be read: %v", err))
	}
	if skipped > 0 {
		warnings = append(warnings, fmt.Sprintf("session_index.jsonl: %d unreadable lines skipped", skipped))
	}
	c.roots[rootDir] = ri
	return ri.threads, warnings
}

// readSessionIndex applies session_index.jsonl ({id, thread_name, updated_at},
// append-only, the last entry of an id wins) on top of the state DB titles.
func readSessionIndex(path string, into map[string]thread) (skipped int, err error) {
	err = fsread.Lines(path, func(line []byte) error {
		var e struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(line, &e) != nil || e.ID == "" {
			skipped++
			return nil
		}
		if name := collapse(e.ThreadName); name != "" {
			t := into[e.ID]
			t.title = name
			into[e.ID] = t
		}
		return nil
	})
	return skipped, err
}

// readStateDB reads threads(id, title, first_user_message, cwd) from a private
// copy of the state DB (never the original, spec §4.3). A title that is just
// the first prompt (or a prefix of it) is not a thread name and is ignored.
func readStateDB(dbPath, tempDir string, into map[string]thread) error {
	copyPath, cleanup, err := fsread.CopySQLite(dbPath, tempDir)
	if err != nil {
		return err
	}
	defer cleanup()
	db, err := fsread.OpenSQLiteCopy(copyPath)
	if err != nil {
		return err
	}
	defer db.Close()

	cols, err := tableColumns(db, "threads")
	if err != nil {
		return err
	}
	if !cols["id"] {
		return nil // not a schema we know: no titles, no error
	}
	col := func(name string) string {
		if cols[name] {
			return "coalesce(CAST(" + name + " AS TEXT), '')"
		}
		return "''"
	}
	rows, err := db.Query("SELECT CAST(id AS TEXT), " + col("title") + ", " + col("first_user_message") + ", " + col("cwd") + " FROM threads")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, title, first, cwd string
		if err := rows.Scan(&id, &title, &first, &cwd); err != nil {
			return err
		}
		if id == "" {
			continue
		}
		t := into[id]
		if name := collapse(title); name != "" && !strings.HasPrefix(collapse(first), name) {
			t.title = name
		}
		if cwd != "" {
			t.cwd = cwd
		}
		into[id] = t
	}
	return rows.Err()
}

// tableColumns returns the column names of table (empty when it does not exist).
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[strings.ToLower(name)] = true
	}
	return cols, rows.Err()
}
