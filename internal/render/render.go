// Package render writes the self-contained HTML reading page (spec §2.4).
//
// The page is a single file with inline CSS and JS only: no network
// resources (a Content-Security-Policy meta tag enforces it), every message
// in an <article dir="auto"> so Arabic reads right-to-left, tool calls and
// outputs collapsed in <details>, and the message shown in the TUI preview
// marked with id="hit" and scrolled into view on load.
package render

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

// Page is everything the reading page shows.
type Page struct {
	AgentName   string
	Title       string
	ProjectPath string
	StartedAt   time.Time
	UpdatedAt   time.Time
	Coverage    model.Coverage
	Messages    []model.Message
	ScrollTo    int   // index into Messages of the message to scroll to; -1 = top
	Hits        []int // indexes of messages that matched the query
}

// DefaultDir is the folder the TUI writes reading pages to: %TEMP%\athar\read.
func DefaultDir() string {
	return filepath.Join(os.TempDir(), "athar", "read")
}

// Write renders the page into dir (created if needed) and returns the file path.
// No external resources; each message uses dir="auto"; tool calls collapsed;
// tool outputs over 20 KB truncated; the ScrollTo message has id="hit".
// The file is named athar-<unix-nanos>.html and never overwrites another file.
func Write(p Page, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("render: create %s: %w", dir, err)
	}
	now := time.Now()
	f, path, err := createUnique(dir, now)
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	w := bufio.NewWriterSize(f, 64<<10)
	err = pageTemplate.Execute(w, buildView(p, now))
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path) // our own half-written file
		return "", fmt.Errorf("render: write %s: %w", path, err)
	}
	return path, nil
}

// createUnique creates dir\athar-<unix-nanos>.html exclusively, bumping the
// number when a file with that name already exists.
func createUnique(dir string, now time.Time) (*os.File, string, error) {
	n := now.UnixNano()
	for i := int64(0); i < 1000; i++ {
		path := filepath.Join(dir, fmt.Sprintf("athar-%d.html", n+i))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, path, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("no free file name in %s", dir)
}
