package agy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// Step types (the step_type column and payload field 1) that carry conversation text.
const (
	stepUserInput       = 14
	stepPlannerResponse = 15
)

// Field numbers inside the protobuf blobs (verified against agy 1.0.9 data).
const (
	// trajectory_metadata_blob.data
	metaWorkspace    = 1 // {1: workspace file:// URI}
	metaCreated      = 2 // Timestamp
	metaWorkspaceURI = 7 // workspace file:// URI (same value as 1.1)

	// steps.metadata (also embedded as step_payload field 5)
	stepCreated        = 1 // Timestamp
	payloadMetadata    = 5
	payloadUserInput   = 19 // {2: text, 3: repeated {1: text}}
	payloadPlanner     = 20 // {1: response, 3: thinking (never read), 7: repeated tool call, 8: modified response}
	userText           = 2
	userItems          = 3
	userItemText       = 1
	plannerResponse    = 1
	plannerToolCalls   = 7
	plannerModifiedRes = 8
	toolCallName       = 2
	toolCallArgs       = 3 // JSON
	toolCallOrigName   = 9
)

// readConversation reads one conversations\<id>.db through a private copy and
// adds the notifications in brain\<id>\.system_generated\messages\*.json.
// A database without steps (agy created it but nothing was sent) yields no session.
func readConversation(ctx context.Context, env adapters.Env, dbPath string) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	id := strings.TrimSuffix(filepath.Base(dbPath), filepath.Ext(dbPath))
	copyPath, cleanup, err := fsread.CopySQLite(dbPath, env.TempDir)
	if err != nil {
		return res, err
	}
	defer cleanup()
	db, err := fsread.OpenSQLiteCopy(copyPath)
	if err != nil {
		return res, err
	}
	defer db.Close()

	s := model.Session{
		Agent:       agentID,
		NativeID:    id,
		TitleSource: model.TitleNone,
		Origin:      model.OriginLive,
	}

	var blob []byte
	switch err := db.QueryRowContext(ctx, "SELECT data FROM trajectory_metadata_blob WHERE id = 'main'").Scan(&blob); {
	case err == nil:
		if !applyTrajectoryMetadata(&s, blob) {
			res.Skipped++
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		if ctx.Err() != nil {
			return adapters.ReadResult{}, ctx.Err()
		}
		res.Warnings = append(res.Warnings, "conversation metadata not readable: "+err.Error())
	}

	steps, stepCount, skipped, err := readSteps(ctx, db)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	if stepCount == 0 {
		return adapters.ReadResult{Warnings: res.Warnings}, nil
	}
	res.Skipped += skipped
	for _, st := range steps {
		widen(&s, st.at)
		s.Messages = append(s.Messages, st.msgs...)
	}

	notes, skipped, err := readBrainMessages(ctx, brainMessagesDir(filepath.Dir(filepath.Dir(dbPath)), id))
	if err != nil {
		if ctx.Err() != nil {
			return adapters.ReadResult{}, ctx.Err()
		}
		if errors.Is(err, adapters.ErrRetryLater) {
			return adapters.ReadResult{}, err
		}
		res.Warnings = append(res.Warnings, "conversation notifications not readable: "+err.Error())
	}
	res.Skipped += skipped
	for _, m := range notes {
		widen(&s, m.At)
	}
	s.Messages = mergeByTime(s.Messages, notes)

	s.Coverage = coverageOf(s.Messages)
	for _, m := range s.Messages {
		if m.Role == model.RoleUser && m.Kind == model.KindText {
			s.Title, s.TitleSource = shortTitle(m.Text), model.TitlePrompt
			break
		}
	}
	res.Sessions = []model.Session{s}
	return res, nil
}

// applyTrajectoryMetadata fills Cwd and StartedAt from the conversation's
// metadata blob. It reports false when the blob is not a protobuf message.
func applyTrajectoryMetadata(s *model.Session, blob []byte) bool {
	m, ok := parseMessage(blob)
	if !ok {
		return false
	}
	if ws, ok := m.sub(metaWorkspace); ok {
		s.Cwd = uriToPath(ws.str(1))
	}
	if s.Cwd == "" {
		s.Cwd = uriToPath(m.str(metaWorkspaceURI))
	}
	if t, ok := m.timeField(metaCreated); ok {
		widen(s, t)
	}
	return true
}

// parsedStep is what one row of the steps table contributes.
type parsedStep struct {
	at   time.Time
	msgs []model.Message
}

// readSteps reads the steps table in order. Columns are looked up by name so
// added columns do not break the reader; a missing step_type or step_payload
// column is an error (the format changed).
func readSteps(ctx context.Context, db *sql.DB) (steps []parsedStep, count, skipped int, err error) {
	rows, err := db.QueryContext(ctx, "SELECT * FROM steps ORDER BY idx")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("agy: read steps: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, 0, 0, err
	}
	typeCol, metaCol, payloadCol := -1, -1, -1
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "step_type":
			typeCol = i
		case "metadata":
			metaCol = i
		case "step_payload":
			payloadCol = i
		}
	}
	if typeCol < 0 || payloadCol < 0 {
		return nil, 0, 0, fmt.Errorf("agy: unsupported steps table (columns %s)", strings.Join(cols, ", "))
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, err
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, 0, 0, err
		}
		count++
		stepType, ok := vals[typeCol].(int64)
		payload, _ := vals[payloadCol].([]byte)
		var metadata []byte
		if metaCol >= 0 {
			metadata, _ = vals[metaCol].([]byte)
		}
		if !ok {
			skipped++
			continue
		}
		st, ok := parseStep(stepType, metadata, payload)
		if !ok {
			skipped++
			continue
		}
		steps = append(steps, st)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	return steps, count, skipped, nil
}

// parseStep decodes one step. User inputs become user messages; planner
// responses become the visible response text plus one tool-call message per
// tool call. Thinking (planner field 3) is never read. Other step types (tool
// executions, notifications, checkpoints) only contribute their time.
// ok is false when the payload is not a protobuf message.
func parseStep(stepType int64, metadata, payload []byte) (parsedStep, bool) {
	p, ok := parseMessage(payload)
	if !ok {
		return parsedStep{}, false
	}
	var st parsedStep
	md, ok := parseMessage(metadata)
	if !ok || len(metadata) == 0 {
		md, _ = p.sub(payloadMetadata)
	}
	st.at, _ = md.timeField(stepCreated)

	switch stepType {
	case stepUserInput:
		in, ok := p.sub(payloadUserInput)
		if !ok {
			return st, true
		}
		text := in.str(userText)
		if text == "" {
			var b strings.Builder
			for _, raw := range in.all(userItems) {
				if item, ok := parseMessage(raw); ok {
					b.WriteString(item.str(userItemText))
				}
			}
			text = b.String()
		}
		if strings.TrimSpace(text) != "" {
			st.msgs = append(st.msgs, model.Message{Role: model.RoleUser, Kind: model.KindText, At: st.at, Text: text})
		}
	case stepPlannerResponse:
		pr, ok := p.sub(payloadPlanner)
		if !ok {
			return st, true
		}
		text := pr.str(plannerResponse)
		if text == "" {
			text = pr.str(plannerModifiedRes)
		}
		if strings.TrimSpace(text) != "" {
			st.msgs = append(st.msgs, model.Message{Role: model.RoleAssistant, Kind: model.KindText, At: st.at, Text: text})
		}
		for _, raw := range pr.all(plannerToolCalls) {
			tc, ok := parseMessage(raw)
			if !ok {
				continue
			}
			name := tc.str(toolCallName)
			if name == "" {
				name = tc.str(toolCallOrigName)
			}
			args := tc.str(toolCallArgs)
			if name == "" && strings.TrimSpace(args) == "" {
				continue
			}
			st.msgs = append(st.msgs, model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: st.at, Text: toolCallText(name, args)})
		}
	}
	return st, true
}

// brainMessage is one brain\<id>\.system_generated\messages\<uuid>.json file:
// a notification delivered to the conversation (e.g. a background command finished).
type brainMessage struct {
	Timestamp      string `json:"timestamp"` // RFC 3339, nanoseconds
	Content        string `json:"content"`
	SourceMetadata struct {
		Tool json.RawMessage `json:"tool"`
	} `json:"sourceMetadata"`
}

// brainMessagesDir is where agy keeps the notifications of conversation id.
func brainMessagesDir(root, id string) string {
	return filepath.Join(root, "brain", id, ".system_generated", "messages")
}

// freshWindow is how recently a notification file must have changed for a
// parse failure to mean "still being written" (spec §4.3).
const freshWindow = 5 * time.Second

// readBrainMessages reads the notification files of one conversation, sorted by
// file name. Files without content (read.json is a map of read flags) are
// ignored. A file that is not JSON is counted as skipped, unless it changed less
// than freshWindow ago: then agy is probably still writing it and the error
// wraps adapters.ErrRetryLater. Notifications from a tool become tool outputs
// (first MaxToolOutput bytes), others system messages.
func readBrainMessages(ctx context.Context, dir string) ([]model.Message, int, error) {
	files, err := jsonFiles(dir)
	if err != nil {
		return nil, 0, err
	}
	var out []model.Message
	skipped := 0
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		data, err := fsread.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			skipped++
			continue
		}
		var bm brainMessage
		if err := json.Unmarshal(data, &bm); err != nil {
			if _, mtime, serr := fsread.Stat(f); serr == nil && time.Since(mtime) < freshWindow {
				return nil, 0, fmt.Errorf("agy: %s changed less than %v ago: %w", filepath.Base(f), freshWindow, adapters.ErrRetryLater)
			}
			skipped++
			continue
		}
		if strings.TrimSpace(bm.Content) == "" {
			continue
		}
		var at time.Time
		if t, err := time.Parse(time.RFC3339Nano, bm.Timestamp); err == nil {
			at = t.UTC()
		}
		m := model.Message{Role: model.RoleSystem, Kind: model.KindText, At: at, Text: bm.Content}
		if tool := strings.TrimSpace(string(bm.SourceMetadata.Tool)); tool != "" && tool != "null" {
			m.Role, m.Kind, m.Text = model.RoleTool, model.KindToolOutput, truncateUTF8(bm.Content, model.MaxToolOutput)
		}
		out = append(out, m)
	}
	return out, skipped, nil
}

// mergeByTime inserts each extra message before the first base message that
// is later than it (messages without a time go last), keeping both orders.
func mergeByTime(base, extra []model.Message) []model.Message {
	for _, e := range extra {
		idx := len(base)
		if !e.At.IsZero() {
			for i, m := range base {
				if !m.At.IsZero() && m.At.After(e.At) {
					idx = i
					break
				}
			}
		}
		base = append(base, model.Message{})
		copy(base[idx+1:], base[idx:])
		base[idx] = e
	}
	return base
}

// coverageOf is full when the conversation has user or assistant messages.
func coverageOf(msgs []model.Message) model.Coverage {
	for _, m := range msgs {
		if m.Role == model.RoleUser || m.Role == model.RoleAssistant {
			return model.CoverageFull
		}
	}
	if len(msgs) > 0 {
		return model.CoveragePartial
	}
	return model.CoverageMeta
}
