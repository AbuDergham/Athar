package commandcode

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

// record is one transcript line of any format version; fields of the other
// versions stay empty.
type record struct {
	Type      string          `json:"type"`
	Timestamp json.RawMessage `json:"timestamp"`
	Cwd       string          `json:"cwd"`     // v3 header
	Name      string          `json:"name"`    // v3 session_info
	Message   *wireMessage    `json:"message"` // v3 message
	// v1/v2 lines have no "type":
	Role     string          `json:"role"`
	Content  json.RawMessage `json:"content"`
	Metadata *wireMeta       `json:"metadata"`
}

type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Meta    *wireMeta       `json:"meta"`
}

// wireMeta is a message's meta (v3) or metadata (v1/v2).
type wireMeta struct {
	Source      string  `json:"source"`
	CreatedAt   float64 `json:"createdAt"` // epoch ms
	IsMeta      bool    `json:"isMeta"`
	IsAutomated bool    `json:"isAutomated"`
	IsSummary   bool    `json:"isSummary"`
}

// sessionMeta is the part of <id>.meta.json that Athar uses.
type sessionMeta struct {
	Title       string `json:"title"`
	UserRenamed bool   `json:"userRenamed"`
}

// ctxCheckEvery is how many lines are parsed between context checks.
const ctxCheckEvery = 256

// Read parses one transcript (and its .meta.json) into at most one session.
func (a adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	var (
		t         transcript
		cwdSeen   bool
		infoSeen  bool
		infoName  string
		lineCount int
	)
	err := fsread.Lines(src.Path, func(line []byte) error {
		lineCount++
		if lineCount%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var r record
		if !decode(line, &r) {
			res.Skipped++
			return nil
		}
		at := parseTime(r.Timestamp)
		switch {
		case r.Type == "session":
			if !cwdSeen && r.Cwd != "" {
				t.cwd, cwdSeen = r.Cwd, true
			}
		case r.Type == "message":
			if r.Message == nil {
				res.Skipped++
				return nil
			}
			at = orCreatedAt(at, r.Message.Meta)
			t.message(r.Message.Role, r.Message.Content, r.Message.Meta, at)
		case r.Type == "session_info":
			infoSeen, infoName = true, strings.TrimSpace(r.Name)
		case r.Type != "":
			// model_change, effort_change, label, custom, compaction,
			// branch_summary, custom_message and future entry types: activity only.
		case r.Role != "":
			at = orCreatedAt(at, r.Metadata)
			t.message(r.Role, r.Content, r.Metadata, at)
		default:
			res.Skipped++
			return nil
		}
		t.observe(at)
		return nil
	})
	if err != nil {
		return adapters.ReadResult{}, err
	}
	if len(t.msgs) == 0 {
		return res, nil // header only, or nothing but hidden context: not a session
	}

	meta, warn, err := readMeta(metaPath(src.Path))
	if err != nil {
		return adapters.ReadResult{}, err
	}
	if warn != "" {
		res.Warnings = append(res.Warnings, warn)
	}

	s := model.Session{
		Agent:     a.ID(),
		NativeID:  strings.TrimSuffix(filepath.Base(src.Path), ".jsonl"),
		Cwd:       t.cwd,
		StartedAt: t.first,
		UpdatedAt: t.last,
		Coverage:  model.CoverageFull,
		Origin:    model.OriginLive,
		Root:      src.Root,
		Messages:  t.msgs,
	}
	s.Title, s.TitleSource = chooseTitle(infoSeen, infoName, meta, t.firstPrompt)
	if s.UpdatedAt.IsZero() {
		if _, mtime, err := fsread.Stat(src.Path); err == nil {
			s.StartedAt, s.UpdatedAt = mtime.UTC(), mtime.UTC()
		}
	}
	res.Sessions = []model.Session{s}
	return res, nil
}

// chooseTitle applies the title preference: the user's name, the agent's
// title, then the first real prompt.
func chooseTitle(infoSeen bool, infoName string, meta sessionMeta, firstPrompt string) (string, model.TitleSource) {
	metaTitle := collapseSpace(meta.Title)
	switch {
	case infoSeen && infoName != "":
		return collapseSpace(infoName), model.TitleUser
	case metaTitle != "" && meta.UserRenamed:
		return metaTitle, model.TitleUser
	case metaTitle != "":
		return metaTitle, model.TitleAgent
	case firstPrompt != "":
		return promptTitle(firstPrompt), model.TitlePrompt
	}
	return "", model.TitleNone
}

// freshWindow: a whole-file JSON sidecar modified this recently may be
// mid-write (spec §4.3).
const freshWindow = 5 * time.Second

// readMeta reads <id>.meta.json. A missing file is normal (no title yet). A
// torn one modified within freshWindow is probably being rewritten: the
// error wraps adapters.ErrRetryLater so sync reads the source again next
// time. An older torn or unreadable one returns a warning and no title.
func readMeta(path string) (sessionMeta, string, error) {
	var m sessionMeta
	data, err := fsread.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, "", nil
	}
	if err == nil && !decode(data, &m) {
		if _, mtime, serr := fsread.Stat(path); serr == nil {
			if age := time.Since(mtime); age < freshWindow && age > -freshWindow {
				return sessionMeta{}, "", fmt.Errorf("%s changed less than %v ago: %w", filepath.Base(path), freshWindow, adapters.ErrRetryLater)
			}
		}
		err = errors.New("not valid JSON")
	}
	if err != nil {
		return sessionMeta{}, fmt.Sprintf("%s: session metadata unreadable, title skipped: %v", filepath.Base(path), err), nil
	}
	return m, "", nil
}

// decode unmarshals data into v and reports whether v is usable. A value of
// an unexpected JSON type only leaves that field unset (encoding/json keeps
// decoding the rest), so it does not make the whole record unusable.
func decode(data []byte, v any) bool {
	err := json.Unmarshal(data, v)
	var typeErr *json.UnmarshalTypeError
	return err == nil || errors.As(err, &typeErr)
}

// parseTime reads an ISO 8601 string or an epoch number (ms, or s when small).
func parseTime(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s)); err == nil {
			return v.UTC()
		}
		return time.Time{}
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return epochTime(n)
	}
	return time.Time{}
}

// epochTime converts epoch milliseconds (or seconds, for values too small to
// be milliseconds after 1973) to UTC; non-positive values give the zero time.
func epochTime(n float64) time.Time {
	switch {
	case n <= 0:
		return time.Time{}
	case n < 1e11:
		return time.Unix(int64(n), 0).UTC()
	default:
		return time.UnixMilli(int64(n)).UTC()
	}
}

// orCreatedAt falls back to meta.createdAt when a record has no timestamp.
func orCreatedAt(at time.Time, meta *wireMeta) time.Time {
	if at.IsZero() && meta != nil {
		return epochTime(meta.CreatedAt)
	}
	return at
}
