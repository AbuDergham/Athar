// Package update checks GitHub Releases and installs verified updates (spec §9).
//
// Check is a single small GET with a 5 s timeout and never runs for
// development builds. Install only replaces athar.exe after checksums.txt has a
// valid minisign signature from PublicKey and the downloaded asset matches its
// signed SHA-256.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository checked for releases.
const Repo = "AbuDergham/Athar"

// PublicKey is the minisign public key used to verify checksums.txt.minisig.
// Set at release time (see docs); empty disables installing (checking still works).
// It holds the base64 key line printed by tools/keygen (a whole minisign .pub
// file is accepted too).
var PublicKey = "RWQI2g/Fu9wN/Ky7mgr1PEzy6p12TcGutfsz27qWI/kiXUe8TJ0oDLKS"

// Errors returned by Install; the TUI can match them with errors.Is.
var (
	// ErrNoPublicKey means this build has no embedded signing key, so it
	// cannot verify (and therefore refuses to install) updates.
	ErrNoPublicKey = errors.New("update: this build has no release signing key; download the update from GitHub instead")
	// ErrBadSignature means checksums.txt is not signed by PublicKey.
	ErrBadSignature = errors.New("update: checksums.txt signature is invalid")
	// ErrChecksumMismatch means the downloaded exe does not match its signed SHA-256.
	ErrChecksumMismatch = errors.New("update: downloaded file does not match its signed checksum")
	// ErrUnsupported means self-update is not available on this OS.
	ErrUnsupported = errors.New("update: self-update is only available on Windows")
)

// Package variables replaced by tests (no other mutable state).
var (
	// apiBaseURL is the GitHub REST API root.
	apiBaseURL = "https://api.github.com"
	// checkTimeout bounds Check (spec §9: 5 s).
	checkTimeout = 5 * time.Second
	// installTimeout bounds Install as a whole (all downloads).
	installTimeout = 10 * time.Minute
	// executablePath returns the path of the running athar.exe.
	executablePath = defaultExecutablePath
	// httpClient performs every request; deadlines come from the context.
	httpClient = &http.Client{}
)

// Response size limits.
const (
	maxReleaseJSON = 8 << 20
	maxChecksums   = 1 << 20
	maxSignature   = 64 << 10
	maxAsset       = 512 << 20
)

// Release asset names published by GoReleaser (.goreleaser.yaml).
const (
	checksumsAsset = "checksums.txt"
	signatureAsset = "checksums.txt.minisig"
)

// Release is a newer version found on GitHub.
type Release struct {
	Version string // e.g. "v1.1.0"
	Notes   string // release body (markdown)
	URL     string // html_url
}

// ghRelease is the subset of the GitHub release JSON Athar reads.
type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Body       string    `json:"body"`
	HTMLURL    string    `json:"html_url"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// errNotFound wraps every HTTP 404.
var errNotFound = errors.New("update: not found")

// githubJSON is the Accept type of GitHub REST API requests.
const githubJSON = "application/vnd.github+json"

// Check asks GitHub for the latest release and returns it when it is newer than
// current; nil when up to date. Timeout 5 s; errors are for logging only.
//
// Development builds (current not of the form vMAJOR.MINOR.PATCH, e.g. "dev")
// never check and make no request. Pre-release suffixes are ignored when
// comparing, and a latest release that is a draft, flagged pre-release or
// tagged with a pre-release suffix is never offered. A repository without
// releases (HTTP 404) counts as up to date.
func Check(ctx context.Context, current string) (*Release, error) {
	cur, ok := parseVersion(current)
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	var rel ghRelease
	err := getJSON(ctx, apiBaseURL+"/repos/"+Repo+"/releases/latest", "athar/"+current, &rel)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rel.Draft || rel.Prerelease {
		return nil, nil
	}
	latest, ok := parseVersion(rel.TagName)
	if !ok {
		return nil, fmt.Errorf("update: latest release has an unrecognized tag %q", rel.TagName)
	}
	if latest.pre != "" || !latest.newerThan(cur) {
		return nil, nil
	}
	return &Release{Version: rel.TagName, Notes: rel.Body, URL: rel.HTMLURL}, nil
}

// Install downloads checksums.txt(.minisig) and the asset for this OS/arch,
// verifies signature and SHA-256, renames the running exe to <exe>.old and
// writes the new one. The caller restarts Athar.
//
// Nothing next to athar.exe changes unless every check passes: the asset is
// downloaded to <exe>.new and only moved into place once its hash matches.
// If the final move fails, the previous exe is put back.
func Install(ctx context.Context, r *Release) error {
	if r == nil {
		return errors.New("update: no release to install")
	}
	pub, err := parsePublicKey(PublicKey)
	if err != nil {
		return err
	}
	name, err := assetName(r.Version)
	if err != nil {
		return err
	}
	exe, err := executablePath()
	if err != nil {
		return fmt.Errorf("update: cannot locate athar.exe: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	ua := "athar-updater/" + r.Version

	var rel ghRelease
	if err := getJSON(ctx, apiBaseURL+"/repos/"+Repo+"/releases/tags/"+url.PathEscape(r.Version), ua, &rel); err != nil {
		if errors.Is(err, errNotFound) {
			return fmt.Errorf("update: release %s not found on GitHub", r.Version)
		}
		return err
	}
	if rel.TagName != r.Version {
		return fmt.Errorf("update: GitHub returned release %q for %q", rel.TagName, r.Version)
	}
	sumsURL, err := assetURL(rel, checksumsAsset)
	if err != nil {
		return err
	}
	sigURL, err := assetURL(rel, signatureAsset)
	if err != nil {
		return err
	}
	exeURL, err := assetURL(rel, name)
	if err != nil {
		return err
	}

	sums, err := getBytes(ctx, sumsURL, ua, maxChecksums)
	if err != nil {
		return err
	}
	sig, err := getBytes(ctx, sigURL, ua, maxSignature)
	if err != nil {
		return err
	}
	if err := verifySignature(pub, sums, sig); err != nil {
		return err
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return err
	}

	next := exe + ".new"
	if err := downloadVerified(ctx, exeURL, ua, next, want); err != nil {
		_ = os.Remove(next)
		return err
	}
	if err := replaceExe(exe, next); err != nil {
		_ = os.Remove(next)
		return err
	}
	return nil
}

// CleanupOld deletes <exe>.old left by a previous update (called at startup).
// Errors are ignored: the old exe may still be running and is removed on a
// later start.
func CleanupOld() {
	exe, err := executablePath()
	if err != nil || exe == "" {
		return
	}
	_ = os.Remove(exe + ".old")
}

// defaultExecutablePath returns the running executable, symlinks resolved.
func defaultExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// assetURL returns the download URL of the release asset called name.
func assetURL(rel ghRelease, name string) (string, error) {
	for _, a := range rel.Assets {
		if a.Name == name && a.URL != "" {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("update: release %s has no %s", rel.TagName, name)
}

// get performs a GET and returns the response when the status is 200.
// The caller closes the body.
func get(ctx context.Context, rawURL, userAgent, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	if accept == githubJSON {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("update: %w", ctxErr)
		}
		return nil, fmt.Errorf("update: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", errNotFound, urlBase(rawURL))
	}
	return nil, fmt.Errorf("update: %s returned %s", req.URL.Host, resp.Status)
}

// getJSON decodes a GitHub API response into v.
func getJSON(ctx context.Context, rawURL, userAgent string, v any) error {
	resp, err := get(ctx, rawURL, userAgent, githubJSON)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseJSON)).Decode(v); err != nil {
		return fmt.Errorf("update: bad release data: %w", err)
	}
	return nil
}

// getBytes downloads a small file, refusing more than limit bytes.
func getBytes(ctx context.Context, rawURL, userAgent string, limit int64) ([]byte, error) {
	resp, err := get(ctx, rawURL, userAgent, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("update: download: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("update: %s is larger than expected", urlBase(rawURL))
	}
	return b, nil
}

// urlBase returns the last element of a URL for messages.
func urlBase(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		return path.Base(u.Path)
	}
	return rawURL
}

// version is a parsed vMAJOR.MINOR.PATCH[-pre][+build].
type version struct {
	major, minor, patch int
	pre                 string // pre-release suffix without "-"; build metadata is dropped
}

// parseVersion accepts "v1.2.3", "1.2.3", "v1.2.3-rc.1" and "v1.2.3+build";
// anything else (such as "dev") is not a release version.
func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v version
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre = s[i+1:]
		s = s[:i]
		if v.pre == "" || strings.Trim(v.pre, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-.") != "" {
			return version{}, false
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	nums := [3]*int{&v.major, &v.minor, &v.patch}
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return version{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		*nums[i] = n
	}
	return v, true
}

// Newer reports whether release version v is newer than current (both
// vMAJOR.MINOR.PATCH; false when either is not a release version).
func Newer(v, current string) bool {
	a, ok := parseVersion(v)
	b, ok2 := parseVersion(current)
	return ok && ok2 && a.newerThan(b)
}

// newerThan compares MAJOR.MINOR.PATCH only; pre-release suffixes are ignored.
func (v version) newerThan(o version) bool {
	if v.major != o.major {
		return v.major > o.major
	}
	if v.minor != o.minor {
		return v.minor > o.minor
	}
	return v.patch > o.patch
}
