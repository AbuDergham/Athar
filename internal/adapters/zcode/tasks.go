package zcode

import (
	"context"
	"strings"

	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// readTaskTitles returns the titles the user set in the desktop app
// (tasks.title_overridden = 1, not deleted), keyed by task id = session id.
// The task index is read through a private copy like the main database.
func readTaskTitles(ctx context.Context, path, tempDir string) (map[string]string, error) {
	cp, cleanup, err := fsread.CopySQLite(path, tempDir)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	db, err := fsread.OpenSQLiteCopy(cp)
	if err != nil {
		return nil, err
	}
	defer db.Close() // runs before cleanup
	cols, err := columns(ctx, db, "tasks")
	if err != nil {
		return nil, err
	}
	if !cols["task_id"] || !cols["title"] || !cols["title_overridden"] {
		return nil, nil // an index without renames (or another layout): nothing to apply
	}
	where := "title_overridden = 1"
	if cols["deleted"] {
		where += " AND deleted = 0"
	}
	order := ""
	if cols["updated_at"] {
		order = " ORDER BY updated_at"
	}
	rows, err := db.QueryContext(ctx, `SELECT task_id, coalesce(title, '') FROM tasks WHERE `+where+order)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		if t := strings.Join(strings.Fields(title), " "); t != "" {
			out[id] = t // the most recently updated row wins
		}
	}
	return out, rows.Err()
}

// applyTaskTitles gives sessions renamed in the desktop app their user title.
func applyTaskTitles(sessions []model.Session, titles map[string]string) {
	if len(titles) == 0 {
		return
	}
	for i := range sessions {
		if t, ok := titles[sessions[i].NativeID]; ok {
			sessions[i].Title, sessions[i].TitleSource = t, model.TitleUser
		}
	}
}
