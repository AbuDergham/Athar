package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/fsread"
)

// liveRecord is <root>\sessions\<pid>.json written by Claude Code for every
// running session. procStart is the process creation time as a Windows
// FILETIME in decimal (a JSON string; a number is accepted too).
type liveRecord struct {
	PID       int64           `json:"pid"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	Status    string          `json:"status"`
	Kind      string          `json:"kind"`
	ProcStart json.RawMessage `json:"procStart"`
	StartedAt int64           `json:"startedAt"` // Unix ms
}

// LiveClaudeSessions reads <root>\sessions\*.json for every Claude root and returns
// live sessions by sessionId. A record is live only if the pid exists and its creation
// time equals procStart.
//
// A root of "" means the default root %USERPROFILE%\.claude. Records with
// missing fields, unreadable files and unparsable JSON count as not live.
// Nothing is written: files are read through fsread.ReadFile.
func LiveClaudeSessions(roots []string) map[string]LiveInfo {
	out := map[string]LiveInfo{}
	for _, root := range roots {
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			root = filepath.Join(home, ".claude")
		}
		dir := filepath.Join(root, "sessions")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
				continue
			}
			data, err := fsread.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			var r liveRecord
			if json.Unmarshal(data, &r) != nil || r.SessionID == "" || r.Kind == "" || r.Cwd == "" || r.Status == "" || r.PID <= 0 || r.PID > 0xFFFFFFFF {
				continue
			}
			want, ok := parseFiletime(r.ProcStart)
			if !ok {
				continue
			}
			got, ok := processStart(uint32(r.PID))
			if !ok || got != want {
				continue
			}
			if _, seen := out[r.SessionID]; seen {
				continue
			}
			started := filetimeToTime(want)
			if r.StartedAt > 0 {
				started = time.UnixMilli(r.StartedAt).UTC()
			}
			out[r.SessionID] = LiveInfo{
				PID:         int(r.PID),
				Interactive: r.Kind == "interactive",
				StartedAt:   started,
			}
		}
	}
	return out
}

// parseFiletime accepts "134030000000000000" or 134030000000000000.
func parseFiletime(raw json.RawMessage) (uint64, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if json.Unmarshal(raw, &str) != nil {
			return 0, false
		}
		s = str
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return v, true
}

// filetimeToTime converts 100-ns intervals since 1601-01-01 UTC to time.Time.
func filetimeToTime(ft uint64) time.Time {
	const epochDiff = 116444736000000000 // 1601 → 1970 in 100 ns
	if ft < epochDiff {
		return time.Time{}
	}
	return time.Unix(0, int64(ft-epochDiff)*100).UTC()
}
