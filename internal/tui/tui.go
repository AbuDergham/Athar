// Package tui is Athar's single-screen terminal UI (spec §2).
package tui

import (
	"errors"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/archive"
	"github.com/AbuDergham/Athar/internal/config"
)

var errTODO = errors.New("tui: not implemented")

// App bundles what the UI needs.
type App struct {
	DB        *archive.DB
	Adapters  []adapters.Adapter
	Env       adapters.Env
	Machine   string
	Hostname  string
	DataDir   string // athar-data folder
	Config    config.Config
	Version   string
	FirstRun  bool
	NoUpdate  bool
}

// Run starts the UI and blocks until the user quits.
func Run(app App) error { return errTODO }
