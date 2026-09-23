package imports

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/model"
)

// AgentsView layout (verified against the v0.40.x schema, structure only):
//
//	sessions(id, agent, file_path, cwd, machine, display_name, session_name,
//	         first_message, started_at, ended_at, parent_session_id,
//	         relationship_type, entrypoint, deleted_at, deletion_cause, ...)
//	messages(id, session_id, ordinal, role, content, timestamp, is_system,
//	         is_sidechain, ...)
//	tool_calls(message_id, session_id, tool_name, input_json, result_content, call_index, ...)
//
//   - sessions.id is "<agent>:<native id>" except for Claude (bare UUID);
//     sessions.agent is AgentsView's agent name (claude, codex, gemini,
//     vscode-copilot, antigravity-cli, ...). file_path is the agent's file
//     ("<db>#<id>" for database-backed agents).
//   - display_name is the user's rename in AgentsView (TitleUser); session_name
//     the agent's own session name (TitleAgent); first_message the first prompt.
//   - Times are RFC 3339 strings with 0-9 fraction digits.
//   - messages.content is AgentsView's flattened text: inline
//     "[Thinking]\n…\n[/Thinking]" blocks (removed here) and one rendering per
//     tool call such as "[Bash]\n$ cmd", "[Read: path]" or "[Tool: name]" (kept:
//     they show what was done). Tool results are in tool_calls.result_content.
//   - is_system rows (compact boundaries, interrupts, task notifications) and
//     sidechain rows are not conversation and are skipped, like the Claude adapter.
//   - deleted_at with a deletion_cause other than 'source_missing' is the user's
//     trash in AgentsView; those sessions are not imported.
//   - Claude rows with entrypoint sdk-cli/sdk-ts/sdk-py are headless runs; the
//     Claude adapter skips them too.

// renderedCall matches the start of AgentsView's rendering of a tool call: a
// line beginning with "[Name]", "[Name: …" or "[Two words…]".
var renderedCall = regexp.MustCompile(`(?m)^\[[A-Z][A-Za-z]*[:\] ]`)

// hostname reports this PC's name (a package var for tests).
var hostname = os.Hostname

type avSession struct {
	id, agent, filePath, cwd, machine          sql.NullString
	displayName, sessionName, firstMessage     sql.NullString
	startedAt, endedAt, parentID, relationship sql.NullString
	entrypoint, deletedAt, deletionCause       sql.NullString
}

type avToolCall struct {
	name, input, result string
}

func readAgentsView(ctx context.Context, db *sql.DB, sch schema) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	c := func(name string) string { return sch.col("sessions", "", name) }
	q := `SELECT id, agent, file_path, ` + c("cwd") + `, ` + c("machine") + `, ` +
		c("display_name") + `, ` + c("session_name") + `, ` + c("first_message") + `, ` +
		c("started_at") + `, ` + c("ended_at") + `, ` + c("parent_session_id") + `, ` +
		c("relationship_type") + `, ` + c("entrypoint") + `, ` + c("deleted_at") + `, ` +
		c("deletion_cause") + ` FROM sessions ORDER BY id`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return res, err
	}
	var list []avSession
	for rows.Next() {
		var s avSession
		if err := rows.Scan(&s.id, &s.agent, &s.filePath, &s.cwd, &s.machine,
			&s.displayName, &s.sessionName, &s.firstMessage, &s.startedAt, &s.endedAt,
			&s.parentID, &s.relationship, &s.entrypoint, &s.deletedAt, &s.deletionCause); err != nil {
			res.Skipped++
			continue
		}
		list = append(list, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	host, _ := hostname()
	for _, s := range list {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if skipAVSession(s) {
			continue
		}
		se, skipped, ok, err := avBuild(ctx, db, sch, s, host)
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

// skipAVSession reports rows that are not top-level user conversations.
func skipAVSession(s avSession) bool {
	if strings.TrimSpace(s.id.String) == "" {
		return true
	}
	if strings.TrimSpace(s.parentID.String) != "" || strings.EqualFold(strings.TrimSpace(s.relationship.String), "subagent") {
		return true
	}
	if strings.TrimSpace(s.deletedAt.String) != "" && s.deletionCause.String != "source_missing" {
		return true
	}
	switch strings.ToLower(s.entrypoint.String) {
	case "sdk-cli", "sdk-ts", "sdk-py":
		return true
	}
	return false
}

// avBuild turns one sessions row and its messages into a Session.
// ok is false when the row has neither messages nor a title.
func avBuild(ctx context.Context, db *sql.DB, sch schema, s avSession, host string) (model.Session, int, bool, error) {
	label := strings.TrimSpace(s.agent.String)
	agent, root := mapAgent(s.filePath.String, label)
	se := model.Session{
		Agent:     agent,
		NativeID:  stripPrefix(s.id.String, label, agent),
		Cwd:       strings.TrimSpace(s.cwd.String),
		StartedAt: parseTime(s.startedAt.String),
		UpdatedAt: parseTime(s.endedAt.String),
		Origin:    model.OriginAgentsView,
		Root:      root,
		Machine:   machineName(s.machine.String, host),
	}
	msgs, skipped, err := avMessages(ctx, db, sch, s.id.String)
	if err != nil {
		return se, 0, false, err
	}
	se.Messages = msgs

	switch {
	case oneLine(s.displayName.String) != "":
		se.Title, se.TitleSource = oneLine(s.displayName.String), model.TitleUser
	case oneLine(s.sessionName.String) != "":
		se.Title, se.TitleSource = oneLine(s.sessionName.String), model.TitleAgent
	case isPrompt(s.firstMessage.String):
		se.Title, se.TitleSource = promptTitle(s.firstMessage.String), model.TitlePrompt
	default:
		se.Title, se.TitleSource = firstPromptTitle(msgs)
	}
	finish(&se)
	return se, skipped, se.Coverage != "", nil
}

// avMessages reads one session's messages in order, with its tool calls.
func avMessages(ctx context.Context, db *sql.DB, sch schema, sessionID string) ([]model.Message, int, error) {
	calls, err := avToolCalls(ctx, db, sch, sessionID)
	if err != nil {
		return nil, 0, err
	}
	c := func(name string) string { return sch.col("messages", "", name) }
	rows, err := db.QueryContext(ctx, `SELECT `+c("id")+`, role, content, `+c("timestamp")+`, `+
		c("is_system")+`, `+c("is_sidechain")+` FROM messages WHERE session_id = ? ORDER BY ordinal`, sessionID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Message
	skipped := 0
	for rows.Next() {
		var (
			id                  sql.NullInt64
			role, content, ts   sql.NullString
			isSystem, sidechain sql.NullInt64
		)
		if err := rows.Scan(&id, &role, &content, &ts, &isSystem, &sidechain); err != nil {
			skipped++
			continue
		}
		if isSystem.Int64 != 0 || sidechain.Int64 != 0 {
			continue
		}
		at := parseTime(ts.String)
		text := stripThinking(content.String)
		switch strings.ToLower(role.String) {
		case "user":
			t, ok := userText(text)
			if !ok {
				continue
			}
			out = append(out, model.Message{Role: model.RoleUser, Kind: model.KindText, At: at, Text: t})
		case "assistant":
			if text != "" {
				out = append(out, model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: at, Text: text})
			}
		case "tool":
			if t := toolOutput(text); t != "" {
				out = append(out, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, At: at, Text: t})
			}
			continue
		case "system":
			continue
		default:
			skipped++
			continue
		}
		if !id.Valid {
			continue
		}
		mc := calls[id.Int64]
		// AgentsView renders each call into the content ("[Bash]\n$ …",
		// "[Edit: path]", "[Tool: name]"); add tool_call messages only when the
		// content holds fewer renderings than calls, and then only for tools
		// it does not mention.
		rendered := len(mc) > 0 && len(renderedCall.FindAllStringIndex(text, -1)) >= len(mc)
		for _, tc := range mc {
			if !rendered && tc.name != "" && !strings.Contains(text, tc.name) {
				out = append(out, model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at, Text: toolCallText(tc.name, tc.input)})
			}
			if r := toolOutput(tc.result); r != "" {
				out = append(out, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, At: at, Text: r})
			}
		}
	}
	return out, skipped, rows.Err()
}

// avToolCalls returns a session's tool calls keyed by message id, in call order.
func avToolCalls(ctx context.Context, db *sql.DB, sch schema, sessionID string) (map[int64][]avToolCall, error) {
	if !sch.has("tool_calls", "message_id", "session_id", "tool_name") || !sch.has("messages", "id") {
		return nil, nil
	}
	order := "id"
	if sch.has("tool_calls", "call_index") {
		order = "COALESCE(call_index, 0), id"
	}
	// substr bounds what is loaded; both are trimmed again in Go.
	input, result := "NULL", "NULL"
	if sch.has("tool_calls", "input_json") {
		input = "substr(input_json, 1, 4000)"
	}
	if sch.has("tool_calls", "result_content") {
		result = "substr(result_content, 1, 4096)"
	}
	rows, err := db.QueryContext(ctx, `SELECT message_id, tool_name, `+input+`, `+result+
		` FROM tool_calls WHERE session_id = ? ORDER BY message_id, `+order, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]avToolCall{}
	for rows.Next() {
		var (
			mid                 sql.NullInt64
			name, input, result sql.NullString
		)
		if err := rows.Scan(&mid, &name, &input, &result); err != nil || !mid.Valid {
			continue
		}
		out[mid.Int64] = append(out[mid.Int64], avToolCall{name: strings.TrimSpace(name.String), input: input.String, result: result.String})
	}
	return out, rows.Err()
}

// machineName returns "" for this PC (AgentsView writes "local" or the host
// name) and the recorded name for sessions synced from another PC.
func machineName(m, host string) string {
	m = strings.TrimSpace(m)
	if m == "" || strings.EqualFold(m, "local") || (host != "" && strings.EqualFold(m, host)) {
		return ""
	}
	return m
}

// isPrompt reports whether s is a real prompt (not empty, not a wrapper).
func isPrompt(s string) bool {
	_, ok := userText(s)
	return ok
}

// firstPromptTitle titles a session by its first user message.
func firstPromptTitle(msgs []model.Message) (string, model.TitleSource) {
	for _, m := range msgs {
		if m.Role == model.RoleUser && m.Kind == model.KindText {
			if t := promptTitle(m.Text); t != "" {
				return t, model.TitlePrompt
			}
		}
	}
	return "", model.TitleNone
}

// finish fills times from the messages when the row has none and sets
// Coverage: full with messages, titles with only a title, "" (skip) otherwise.
func finish(se *model.Session) {
	var first, last time.Time
	for _, m := range se.Messages {
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
	if se.StartedAt.IsZero() || (!first.IsZero() && first.Before(se.StartedAt)) {
		se.StartedAt = first
	}
	if last.After(se.UpdatedAt) {
		se.UpdatedAt = last
	}
	if se.UpdatedAt.IsZero() {
		se.UpdatedAt = se.StartedAt
	}
	if se.StartedAt.IsZero() {
		se.StartedAt = se.UpdatedAt
	}
	switch {
	case len(se.Messages) > 0:
		se.Coverage = model.CoverageFull
	case se.Title != "":
		se.Coverage = model.CoverageTitles
	default:
		se.Coverage = ""
	}
	if se.TitleSource == "" {
		se.TitleSource = model.TitleNone
	}
}
