package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
	"github.com/AbuDergham/Athar/internal/model"
)

// The legacy store (OpenCode up to 1.1.x, still written by the desktop app's
// bundled CLI) keeps one JSON file per object under <data>\storage:
//
//	session\<projectID>\<ses_id>.json   {id, parentID?, directory, title, time{created, updated}}
//	message\<ses_id>\<msg_id>.json      {id, sessionID, role, time{created}, …}
//	part\<msg_id>\<prt_id>.json         {id, messageID, type, text | tool+state | …}
//
// One Source per session file. Its fingerprint files are the session file,
// its message folder, every message file and every message's part folder (a
// new part changes its folder's mtime; a part rewritten in place is followed
// by an update of its message). Directories here keep backslashes.
//
// Every file is whole-file JSON: if any file of a session was modified in the
// last 5 s, or disappeared while the session was being read, Read returns an
// error wrapping adapters.ErrRetryLater and the source is read at the next
// sync. An older file that does not parse is a skipped record.

// freshWindow: a legacy JSON file modified less than this long ago may still be
// being written, so its whole source is left for the next sync (spec §4.3).
const freshWindow = 5 * time.Second

// now is the clock used for freshWindow (a package var so tests could pin it).
var now = time.Now

// legacySources lists one Source per session file under data\storage\session.
func legacySources(data string) []adapters.Source {
	storage := filepath.Join(data, "storage")
	projects, err := os.ReadDir(filepath.Join(storage, "session"))
	if err != nil {
		return nil
	}
	var out []adapters.Source
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(storage, "session", p.Name())
		for _, f := range jsonFiles(dir) {
			path := filepath.Join(dir, f)
			id := strings.TrimSuffix(f, filepath.Ext(f))
			msgDir := filepath.Join(storage, "message", id)
			files := []string{path, msgDir}
			for _, m := range jsonFiles(msgDir) {
				files = append(files,
					filepath.Join(msgDir, m),
					filepath.Join(storage, "part", strings.TrimSuffix(m, filepath.Ext(m))))
			}
			out = append(out, adapters.Source{
				Adapter: "opencode",
				Path:    path,
				Files:   files,
				Extra:   map[string]string{"store": "legacy"},
			})
		}
	}
	return out
}

// jsonFiles returns the names of the *.json files in dir, sorted (OpenCode ids
// sort in creation order). A missing folder gives none.
func jsonFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			out = append(out, e.Name())
		}
	}
	return out
}

// legacySession is the part of a legacy session file Athar uses.
type legacySession struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentID"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Time      struct {
		Created float64 `json:"created"`
		Updated float64 `json:"updated"`
	} `json:"time"`
}

// readLegacy reads one legacy session file with its messages and parts.
func readLegacy(ctx context.Context, path string) (adapters.ReadResult, error) {
	var res adapters.ReadResult
	storage := filepath.Dir(filepath.Dir(filepath.Dir(path))) // storage\session\<project>\<id>.json

	b, err := readSettled(path)
	if err != nil {
		return res, err
	}
	var ls legacySession
	if err := json.Unmarshal(b, &ls); err != nil {
		res.Skipped++
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: unreadable session file", filepath.Base(path)))
		return res, nil
	}
	if ls.ParentID != "" {
		return res, nil // a subagent's session
	}
	// Folders are looked up by the file name (as Sources did for the fingerprint).
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if ls.ID == "" {
		ls.ID = stem
	}
	s := &session{
		id: ls.ID, directory: ls.Directory, title: ls.Title,
		created: int64(ls.Time.Created), updated: int64(ls.Time.Updated),
	}

	msgDir := filepath.Join(storage, "message", stem)
	for _, name := range jsonFiles(msgDir) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		mpath := filepath.Join(msgDir, name)
		data, err := readSettled(mpath)
		if err != nil {
			return res, err
		}
		m, err := parseMessage(data, 0)
		if err != nil {
			res.Skipped++
			continue
		}
		s.messages = append(s.messages, m)

		partDir := filepath.Join(storage, "part", strings.TrimSuffix(name, filepath.Ext(name)))
		for _, pname := range jsonFiles(partDir) {
			ppath := filepath.Join(partDir, pname)
			data, err := readSettled(ppath)
			if err != nil {
				return res, err
			}
			if err := m.addPart(data, 0); err != nil {
				res.Skipped++
			}
		}
	}
	if ms, ok := build("opencode", s); ok {
		res.Sessions = []model.Session{ms}
	}
	return res, nil
}

// readSettled reads a whole-file JSON record through fsread. A file modified
// less than freshWindow ago, or one that vanished after it was listed (OpenCode
// deletes messages on undo), gives an error wrapping adapters.ErrRetryLater.
func readSettled(path string) ([]byte, error) {
	_, mtime, err := fsread.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s disappeared while reading: %w", filepath.Base(path), adapters.ErrRetryLater)
	}
	if err != nil {
		return nil, err
	}
	if age := now().Sub(mtime); age < freshWindow && age > -freshWindow {
		return nil, fmt.Errorf("%s changed less than %v ago: %w", filepath.Base(path), freshWindow, adapters.ErrRetryLater)
	}
	b, err := fsread.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s disappeared while reading: %w", filepath.Base(path), adapters.ErrRetryLater)
	}
	return b, err
}
