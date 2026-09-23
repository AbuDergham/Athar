// Package windsurf lists the Cascade conversations of the Windsurf editor
// (Codeium; renamed Devin Desktop in 2026).
//
// Store: one file per Cascade conversation,
// %USERPROFILE%\.codeium\windsurf\cascade\<uuid>.pb (and the same layout under
// .codeium\windsurf-next for the "Windsurf - Next" build). The files are
// encrypted (no protobuf structure, ~8 bits/byte of entropy), so their contents
// are never read: only the file name and the file times are used.
//
//   - NativeID = file name without ".pb" (the conversation UUID).
//   - StartedAt = the file's creation time, or its modification time when that
//     is older or the creation time is unknown; UpdatedAt = modification time.
//   - Title = "Windsurf conversation <first 8 characters of the id>" (TitleNone,
//     so any real title from another source replaces it).
//   - Coverage meta, no Cwd (project "(no project)"), no messages.
//
// Other files under .codeium\windsurf (implicit\*.pb context snapshots,
// user_settings.pb, memories, …) are not conversations and are ignored.
// Windsurf has no way to open a given conversation from the command line, and
// the app is not installed on the owner's PC, so Caps is empty.
package windsurf

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/model"
)

// convExt is the extension of a Cascade conversation file (matched case-insensitively).
const convExt = ".pb"

// titleIDLen is how many characters of the id the placeholder title shows.
const titleIDLen = 8

// products are the folders under %USERPROFILE%\.codeium that hold a cascade\
// folder: the stable build first, then "Windsurf - Next".
var products = []string{"windsurf", "windsurf-next"}

type adapter struct{}

// New returns the Windsurf adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{} }

func (adapter) ID() string   { return "windsurf" }
func (adapter) Name() string { return "Windsurf" }
func (adapter) Version() int { return 1 }

// Caps is empty: a Cascade conversation cannot be resumed from a terminal and
// its store gives no project folder to open the editor on.
func (adapter) Caps() adapters.Caps { return adapters.Caps{} }

// Sources lists every <id>.pb file directly inside each cascade folder, one
// source per conversation. Missing folders are skipped silently; a cascade
// folder that exists but cannot be listed is reported as an error, together
// with whatever could be listed elsewhere.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	if env.Home == "" {
		return nil, nil
	}
	var (
		out  []adapters.Source
		errs []error
	)
	for _, product := range products {
		dir := filepath.Join(env.Home, ".codeium", product, "cascade")
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("windsurf: list %s: %w", dir, err))
			}
		}
		for _, e := range entries {
			if e.IsDir() || nativeID(e.Name()) == "" {
				continue
			}
			p := filepath.Join(dir, e.Name())
			out = append(out, adapters.Source{Adapter: "windsurf", Path: p, Files: []string{p}})
		}
	}
	return out, errors.Join(errs...)
}

// nativeID returns the conversation id of a cascade file name, or "" when the
// name is not "<id>.pb".
func nativeID(name string) string {
	if len(name) <= len(convExt) || !strings.EqualFold(filepath.Ext(name), convExt) {
		return ""
	}
	return name[:len(name)-len(convExt)]
}

// Read turns one conversation file into a metadata-only session. It only
// stats the file; the encrypted contents are never opened.
func (adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	name := filepath.Base(src.Path)
	id := nativeID(name)
	if id == "" {
		return adapters.ReadResult{}, fmt.Errorf("windsurf: %s is not a conversation file", name)
	}
	info, err := os.Stat(src.Path)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	if info.IsDir() {
		return adapters.ReadResult{}, fmt.Errorf("windsurf: %s is a folder", name)
	}
	updated := info.ModTime().UTC()
	started := updated
	if created, ok := creationTime(info); ok && !created.IsZero() && created.Before(started) {
		started = created.UTC()
	}
	s := model.Session{
		Agent:       "windsurf",
		NativeID:    id,
		Title:       "Windsurf conversation " + shortID(id),
		TitleSource: model.TitleNone,
		StartedAt:   started,
		UpdatedAt:   updated,
		Coverage:    model.CoverageMeta,
		Origin:      model.OriginLive,
	}
	return adapters.ReadResult{Sessions: []model.Session{s}}, nil
}

// shortID returns the first titleIDLen characters of id (all of it when shorter).
func shortID(id string) string {
	r := []rune(id)
	if len(r) > titleIDLen {
		r = r[:titleIDLen]
	}
	return string(r)
}
