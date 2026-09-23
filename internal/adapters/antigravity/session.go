package antigravity

import (
	"strings"
	"time"
	"unicode"

	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// titleRunes is the length of a title made from the first prompt.
const titleRunes = 100

// buildSession assembles one conversation from its index summary (hasSum
// false when no index knows it) and its brain folder. fallback is a file
// whose mtime dates the session when nothing else does.
func buildSession(cid string, sum summary, hasSum bool, b brain, fallback string) (model.Session, int) {
	s := model.Session{
		Agent:    id,
		NativeID: cid,
		Origin:   model.OriginLive,
		Cwd:      sum.cwd,
	}
	logMsgs, skipped := logMessages(b.logs)
	artMsgs, sk := artifactMessages(b.artifacts)
	skipped += sk
	s.Messages = append(logMsgs, artMsgs...)

	switch {
	case len(logMsgs) > 0:
		s.Coverage = model.CoverageFull
	case len(artMsgs) > 0:
		s.Coverage = model.CoveragePartial
	case hasSum:
		s.Coverage = model.CoverageTitles
	default:
		s.Coverage = model.CoverageMeta
	}

	s.Title, s.TitleSource = sum.title, model.TitleAgent
	if s.Title == "" {
		s.Title, s.TitleSource = "", model.TitleNone
		for _, m := range logMsgs {
			if m.Role == model.RoleUser && m.Kind == model.KindText {
				s.Title, s.TitleSource = promptTitle(m.Text), model.TitlePrompt
				break
			}
		}
	}

	var first, last time.Time
	for _, m := range s.Messages {
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
	s.StartedAt = sum.created
	if s.StartedAt.IsZero() {
		s.StartedAt = first
	}
	s.UpdatedAt = latest(sum.updated, sum.lastInput, last, s.StartedAt)
	if s.UpdatedAt.IsZero() && fallback != "" {
		if _, mtime, err := fsread.Stat(fallback); err == nil {
			s.UpdatedAt = mtime.UTC()
		}
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = s.UpdatedAt
	}
	return s, skipped
}

// latest returns the latest of ts (zero when all are zero).
func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// promptTitle collapses whitespace and keeps the first titleRunes runes.
func promptTitle(s string) string {
	out := make([]rune, 0, titleRunes)
	pendingSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			pendingSpace = len(out) > 0
			continue
		}
		if pendingSpace {
			if len(out) >= titleRunes {
				break
			}
			out = append(out, ' ')
			pendingSpace = false
		}
		if len(out) >= titleRunes {
			break
		}
		out = append(out, r)
	}
	return strings.TrimRight(string(out), " ")
}
