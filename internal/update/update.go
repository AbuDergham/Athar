// Package update checks GitHub Releases and installs verified updates (spec §9).
package update

import (
	"context"
	"errors"
)

var errTODO = errors.New("update: not implemented")

// Repo is the GitHub repository checked for releases.
const Repo = "AbuDergham/Athar"

// PublicKey is the minisign public key used to verify checksums.txt.minisig.
// Set at release time (see docs); empty disables installing (checking still works).
var PublicKey = ""

// Release is a newer version found on GitHub.
type Release struct {
	Version string // e.g. "v1.1.0"
	Notes   string // release body (markdown)
	URL     string // html_url
}

// Check asks GitHub for the latest release and returns it when it is newer than
// current; nil when up to date. Timeout 5 s; errors are for logging only.
func Check(ctx context.Context, current string) (*Release, error) { return nil, errTODO }

// Install downloads checksums.txt(.minisig) and the asset for this OS/arch,
// verifies signature and SHA-256, renames the running exe to <exe>.old and
// writes the new one. The caller restarts Athar.
func Install(ctx context.Context, r *Release) error { return errTODO }

// CleanupOld deletes <exe>.old left by a previous update (called at startup).
func CleanupOld() {}
