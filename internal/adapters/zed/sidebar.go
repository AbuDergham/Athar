package zed

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

// renames reads sidebar_threads.title_override (the user's own thread names)
// from the channel dbs listed in src.Files, earlier dbs winning. Rows of
// external ACP agents (agent_id set) are ignored. A channel db that cannot be
// read only costs its renames and is reported as a warning.
func renames(env adapters.Env, src adapters.Source) (map[string]string, []string) {
	out := map[string]string{}
	var warns []string
	for _, db := range src.Files {
		if !strings.EqualFold(filepath.Base(db), "db.sqlite") {
			continue // the threads db and every -wal / -shm sidecar
		}
		if _, err := os.Stat(db); err != nil {
			continue // gone since Sources: nothing to read
		}
		if err := readRenames(env, db, out); err != nil {
			warns = append(warns, fmt.Sprintf("thread renames not read from %s: %v", db, err))
		}
	}
	return out, warns
}

func readRenames(env adapters.Env, path string, out map[string]string) error {
	cp, cleanup, err := fsread.CopySQLite(path, env.TempDir)
	defer cleanup()
	if err != nil {
		return err
	}
	db, err := fsread.OpenSQLiteCopy(cp)
	if err != nil {
		return err
	}
	defer db.Close()
	cols, err := columns(db, "sidebar_threads")
	if err != nil {
		return err
	}
	if !cols["session_id"] || !cols["title_override"] {
		return nil // an older Zed, or a db without the sidebar: no renames
	}
	agent := "NULL"
	if cols["agent_id"] {
		agent = "agent_id"
	}
	rows, err := db.Query(`SELECT session_id, title_override FROM sidebar_threads WHERE ` + agent + ` IS NULL AND session_id IS NOT NULL AND title_override IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		if name = collapse(name); name == "" {
			continue
		}
		if _, seen := out[id]; !seen {
			out[id] = name
		}
	}
	return rows.Err()
}
