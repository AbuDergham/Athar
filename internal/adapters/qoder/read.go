package qoder

import (
	"context"
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

// defaultTitle is the placeholder title qodercli gives a session before it names it.
const defaultTitle = "New Session"

// freshWindow: a metadata file that fails to parse and was modified this
// recently is probably being written; the source is retried at the next sync.
const freshWindow = 5 * time.Second

// sessionMeta is the <id>-session.json file.
type sessionMeta struct {
	ID               string  `json:"id"`
	ParentSessionID  string  `json:"parent_session_id"`
	Title            string  `json:"title"`
	WorkingDir       string  `json:"working_dir"`
	SummaryMessageID string  `json:"summary_message_id"`
	CreatedAt        float64 `json:"created_at"`
	UpdatedAt        float64 `json:"updated_at"`
}

// record is one line of a transcript.
type record struct {
	ID         string  `json:"id"`
	Role       string  `json:"role"`
	IsMeta     bool    `json:"is_meta"`
	Parts      []part  `json:"parts"`
	CreatedAt  float64 `json:"created_at"`
	UpdatedAt  float64 `json:"updated_at"`
	FinishedAt float64 `json:"finished_at"`
}

type part struct {
	Type string   `json:"type"`
	Data partData `json:"data"`
}

// partData is the union of the part payloads Athar reads. Input and Content
// are JSON strings in qodercli 0.1.3; raw values keep other shapes readable.
type partData struct {
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// Read parses one transcript and its metadata file.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	meta, warning, err := readMeta(metaPath(src.Path))
	if err != nil {
		return res, err
	}
	if warning != "" {
		res.Warnings = append(res.Warnings, warning)
	}
	if meta.ParentSessionID != "" {
		return res, nil // sub-agent session: not archived (spec §1 non-goals)
	}

	b := builder{summaryID: meta.SummaryMessageID}
	n := 0
	err = fsread.Lines(src.Path, func(line []byte) error {
		if n++; n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			res.Skipped++
			return nil
		}
		b.add(r)
		return nil
	})
	if err != nil {
		return adapters.ReadResult{}, err
	}
	if len(b.msgs) == 0 {
		return res, nil // nothing but meta records: no conversation
	}

	s := model.Session{
		Agent:    adapterID,
		NativeID: strings.TrimSuffix(filepath.Base(src.Path), ".jsonl"),
		Cwd:      meta.WorkingDir,
		Coverage: model.CoverageFull,
		Origin:   model.OriginLive,
		Messages: b.msgs,
	}
	if t := strings.Join(strings.Fields(meta.Title), " "); t != "" && !strings.EqualFold(t, defaultTitle) {
		s.Title, s.TitleSource = t, model.TitleAgent
	} else if b.firstPrompt != "" {
		s.Title, s.TitleSource = promptTitle(b.firstPrompt), model.TitlePrompt
	} else {
		s.TitleSource = model.TitleNone
	}
	b.span.add(epoch(meta.CreatedAt))
	b.span.add(epoch(meta.UpdatedAt))
	if b.span.first.IsZero() {
		if _, mtime, err := fsread.Stat(src.Path); err == nil {
			b.span.add(mtime.UTC())
		}
	}
	s.StartedAt, s.UpdatedAt = b.span.first, b.span.last
	res.Sessions = append(res.Sessions, s)
	return res, nil
}

// readMeta reads <id>-session.json. A missing file is not an error. A file
// that cannot be parsed yields a warning, or an error when it was modified in
// the last few seconds (it is probably being written; retry next sync).
func readMeta(path string) (sessionMeta, string, error) {
	var m sessionMeta
	data, err := fsread.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, "", nil
	}
	if err != nil {
		return m, fmt.Sprintf("session metadata not read: %v", err), nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		if _, mtime, serr := fsread.Stat(path); serr == nil && time.Since(mtime) < freshWindow {
			return sessionMeta{}, "", fmt.Errorf("qoder: %s changed less than 5 s ago: %w", filepath.Base(path), adapters.ErrRetryLater)
		}
		return sessionMeta{}, fmt.Sprintf("session metadata ignored (%s): %v", filepath.Base(path), err), nil
	}
	return m, "", nil
}

// builder turns records into messages, in file order.
type builder struct {
	summaryID   string
	msgs        []model.Message
	firstPrompt string
	span        span
}

func (b *builder) add(r record) {
	at := epoch(r.CreatedAt)
	b.span.add(at)
	b.span.add(epoch(r.UpdatedAt))
	b.span.add(epoch(r.FinishedAt))
	if r.IsMeta || (b.summaryID != "" && r.ID == b.summaryID) {
		return
	}
	var role model.Role
	switch r.Role {
	case "user":
		role = model.RoleUser
	case "assistant":
		role = model.RoleAssistant
	case "tool":
		role = model.RoleTool
	default:
		return // system or unknown records carry no conversation
	}

	var texts []string
	flush := func() {
		if len(texts) > 0 {
			b.msgs = append(b.msgs, model.Message{Role: role, Kind: model.KindText, At: at, Text: strings.Join(texts, "\n\n")})
			texts = nil
		}
	}
	for _, p := range r.Parts {
		switch p.Type {
		case "text":
			t := p.Data.Text
			if role == model.RoleUser {
				t = cleanUserText(t)
				if b.firstPrompt == "" {
					b.firstPrompt = t
				}
			}
			if t = strings.TrimSpace(t); t != "" {
				texts = append(texts, t)
			}
		case "image_url", "image", "binary":
			texts = append(texts, "[image]")
		case "tool_call":
			flush()
			b.msgs = append(b.msgs, model.Message{Role: model.RoleAssistant, Kind: model.KindToolCall, At: at,
				Text: toolCallText(p.Data.Name, rawText(p.Data.Input))})
		case "tool_result":
			flush()
			b.msgs = append(b.msgs, model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, At: at,
				Text: truncateBytes(rawText(p.Data.Content), model.MaxToolOutput)})
		}
		// reasoning, finish and unknown parts are dropped.
	}
	flush()
}

// span tracks the earliest and latest non-zero times seen.
type span struct{ first, last time.Time }

func (s *span) add(t time.Time) {
	if t.IsZero() {
		return
	}
	if s.first.IsZero() || t.Before(s.first) {
		s.first = t
	}
	if t.After(s.last) {
		s.last = t
	}
}

// epoch converts a qodercli timestamp (epoch milliseconds; seconds tolerated)
// to UTC. Zero, negative or non-finite values give the zero time.
func epoch(v float64) time.Time {
	if !(v > 0) || v > 1e16 {
		return time.Time{}
	}
	if v < 1e11 { // seconds (1e11 ms is 1973)
		v *= 1000
	}
	return time.UnixMilli(int64(v)).UTC()
}
