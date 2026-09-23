package imports

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/model"
)

// Pi Session Manager layout (verified against the v0.8.x schema, structure only):
//
//	sessions(id, path, cwd, name, created, modified, first_message,
//	         parent_session_path, ...)
//	message_entries(id, entry_id, session_path, role, source_type, content, timestamp, ...)
//	scan_state(path, provider_slug, ...)
//
//   - sessions.path is the agent's file (unique); sessions.id is
//     "<provider slug>:<native id>:<first 12 hex of sha256(path)>" for other
//     agents' files and the bare id for Pi's own sessions.
//   - scan_state.provider_slug labels the file (claude-code, codex, cline, pi);
//     it is wrong for some files (Gemini chats and .claude-local transcripts are
//     tagged "pi"), so the path rules decide first.
//   - name is the session title the tool derived (the agent's own title or the
//     first prompt, not recorded which): it counts as a prompt title when it is
//     the start of the first message, otherwise as an agent title.
//   - message_entries rows are user/assistant text; source_type "thinking" is
//     dropped (thinking is never archived) and "label" rows are tree labels, not
//     messages. Times are RFC 3339 with a ±hh:mm zone.
//   - Rows with a parent_session_path, subagent transcripts and prompt logs
//     (history.jsonl) are skipped.

// psmID splits "<slug>:<native id>:<12 hex>".
var psmID = regexp.MustCompile(`^([A-Za-z0-9_.-]+):(.+):([0-9a-f]{12})$`)

type psmSession struct {
	id, path, cwd, name, created, modified sql.NullString
	firstMessage, parentPath, slug         sql.NullString
}

func readPSM(ctx context.Context, db *sql.DB, sch schema) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	c := func(name string) string { return sch.col("sessions", "s", name) }
	slug, join := "NULL", ""
	if sch.has("scan_state", "path", "provider_slug") {
		slug, join = "st.provider_slug", " LEFT JOIN scan_state st ON st.path = s.path"
	}
	q := `SELECT s.id, s.path, ` + c("cwd") + `, ` + c("name") + `, ` + c("created") + `, ` +
		c("modified") + `, ` + c("first_message") + `, ` + c("parent_session_path") + `, ` + slug +
		` FROM sessions s` + join + ` ORDER BY s.path`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return res, err
	}
	var list []psmSession
	for rows.Next() {
		var s psmSession
		if err := rows.Scan(&s.id, &s.path, &s.cwd, &s.name, &s.created, &s.modified,
			&s.firstMessage, &s.parentPath, &s.slug); err != nil {
			res.Skipped++
			continue
		}
		list = append(list, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	for _, s := range list {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if skipPSMSession(s) {
			continue
		}
		se, skipped, ok, err := psmBuild(ctx, db, sch, s)
		if err != nil {
			return res, err
		}
		res.Skipped += skipped
		if ok {
			res.Sessions = append(res.Sessions, se)
		}
	}
	return res, nil
}

// skipPSMSession reports rows that are not top-level conversations.
func skipPSMSession(s psmSession) bool {
	p := strings.ToLower(strings.ReplaceAll(s.path.String, "/", `\`))
	switch {
	case strings.TrimSpace(s.id.String) == "", strings.TrimSpace(p) == "":
		return true
	case strings.TrimSpace(s.parentPath.String) != "":
		return true
	case strings.Contains(p, `\subagents\`):
		return true
	case strings.HasSuffix(p, `\history.jsonl`) || p == "history.jsonl":
		return true
	}
	return false
}

// psmNativeID returns the agent's own id and the slug the id carries.
func psmNativeID(id, slug, agent string) (native, idSlug string) {
	if m := psmID.FindStringSubmatch(id); m != nil {
		return m[2], m[1]
	}
	return stripPrefix(id, slug, agent), ""
}

func psmBuild(ctx context.Context, db *sql.DB, sch schema, s psmSession) (model.Session, int, bool, error) {
	label := strings.TrimSpace(s.slug.String)
	native, idSlug := psmNativeID(s.id.String, label, "")
	if idSlug != "" && (label == "" || strings.EqualFold(label, "pi")) {
		// the id's slug is the better label when scan_state says "pi"
		label = idSlug
	}
	agent, root := mapAgent(s.path.String, label)
	if idSlug == "" {
		native = stripPrefix(native, agent)
	}
	se := model.Session{
		Agent:     agent,
		NativeID:  native,
		Cwd:       strings.TrimSpace(s.cwd.String),
		StartedAt: parseTime(s.created.String),
		UpdatedAt: parseTime(s.modified.String),
		Origin:    model.OriginPSM,
		Root:      root,
	}
	if se.NativeID == "" {
		se.NativeID = strings.TrimSuffix(filepath.Base(s.path.String), filepath.Ext(s.path.String))
	}
	msgs, skipped, err := psmMessages(ctx, db, sch, s.path.String)
	if err != nil {
		return se, 0, false, err
	}
	se.Messages = msgs

	name, first := oneLine(s.name.String), oneLine(s.firstMessage.String)
	if !isPrompt(name) {
		name = ""
	}
	if !isPrompt(first) {
		first = ""
	}
	if first == "" {
		for _, m := range msgs {
			if m.Role == model.RoleUser {
				first = oneLine(m.Text)
				break
			}
		}
	}
	switch {
	case name != "" && isPromptPrefix(name, first):
		se.Title, se.TitleSource = promptTitle(name), model.TitlePrompt
	case name != "":
		se.Title, se.TitleSource = name, model.TitleAgent
	case first != "":
		se.Title, se.TitleSource = promptTitle(first), model.TitlePrompt
	default:
		se.TitleSource = model.TitleNone
	}
	finish(&se)
	return se, skipped, se.Coverage != "", nil
}

// isPromptPrefix reports whether a title is (a truncation of) the first prompt.
func isPromptPrefix(title, first string) bool {
	if first == "" {
		return false
	}
	t := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(title, "..."), "…"))
	return t != "" && strings.HasPrefix(first, t)
}

// psmMessages reads one session's entries in order (insertion order, then
// stably by time so re-indexed entries land in place).
func psmMessages(ctx context.Context, db *sql.DB, sch schema, path string) ([]model.Message, int, error) {
	st := "role"
	if sch.has("message_entries", "source_type") {
		st = "source_type"
	}
	ts := sch.col("message_entries", "", "timestamp")
	q := `SELECT role, ` + st + `, content, ` + ts + ` FROM message_entries WHERE session_path = ? ORDER BY rowid`
	rows, err := db.QueryContext(ctx, q, path)
	if err != nil {
		// WITHOUT ROWID tables (other versions): fall back to time order.
		rows, err = db.QueryContext(ctx, `SELECT role, `+st+`, content, `+ts+` FROM message_entries WHERE session_path = ? ORDER BY `+ts, path)
		if err != nil {
			return nil, 0, err
		}
	}
	defer rows.Close()
	var out []model.Message
	skipped := 0
	allTimed := true
	for rows.Next() {
		var role, source, content, at sql.NullString
		if err := rows.Scan(&role, &source, &content, &at); err != nil {
			skipped++
			continue
		}
		switch strings.ToLower(source.String) {
		case "thinking", "label":
			continue
		}
		text := strings.TrimSpace(content.String)
		if text == "" {
			continue
		}
		m := model.Message{Kind: model.KindText, At: parseTime(at.String), Text: text}
		switch strings.ToLower(role.String) {
		case "user":
			t, ok := userText(text)
			if !ok {
				continue
			}
			m.Role, m.Text = model.RoleUser, t
		case "assistant":
			m.Role = model.RoleAssistant
		default:
			skipped++
			continue
		}
		if m.At.IsZero() {
			allTimed = false
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if allTimed {
		sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	}
	return out, skipped, nil
}
