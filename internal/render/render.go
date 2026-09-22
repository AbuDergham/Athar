// Package render writes the self-contained HTML reading page (spec §2.4).
package render

import (
	"errors"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

var errTODO = errors.New("render: not implemented")

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

// Write renders the page into dir (created if needed) and returns the file path.
// No external resources; each message uses dir="auto"; tool calls collapsed;
// tool outputs over 20 KB truncated; the ScrollTo message has id="hit".
func Write(p Page, dir string) (string, error) { return "", errTODO }
