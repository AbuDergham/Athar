//go:build windows

package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func ownCreationFiletime(t *testing.T) uint64 {
	t.Helper()
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &c, &e, &k, &u); err != nil {
		t.Fatal(err)
	}
	return uint64(c.HighDateTime)<<32 | uint64(c.LowDateTime)
}

func TestLiveClaudeSessions(t *testing.T) {
	pid := os.Getpid()
	ft := ownCreationFiletime(t)
	startedMs := time.Now().Add(-time.Minute).UnixMilli()

	rootA := t.TempDir()
	rootB := t.TempDir()
	write := func(root, name, body string) {
		t.Helper()
		p := filepath.Join(root, "sessions", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec := func(session, kind, procStart string) string {
		return fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"C:\\w","startedAt":%d,"procStart":%s,"kind":%q,"status":"busy"}`,
			pid, session, startedMs, procStart, kind)
	}
	q := strconv.Quote
	write(rootA, "1.json", rec("s-live", "interactive", q(strconv.FormatUint(ft, 10))))
	write(rootA, "2.json", rec("s-wrongstart", "interactive", q(strconv.FormatUint(ft+1, 10))))
	write(rootA, "3.json", rec("s-background", "bg", q(strconv.FormatUint(ft, 10))))
	write(rootA, "4.json", `{"pid":`+strconv.Itoa(pid)+`,"sessionId":"s-nostart","kind":"interactive"}`)
	write(rootA, "5.json", `{not json`)
	write(rootA, "6.json", `{"pid":4000000000,"sessionId":"s-nopid","kind":"interactive","procStart":"1"}`)
	write(rootA, "notes.txt", rec("s-txt", "interactive", q(strconv.FormatUint(ft, 10))))
	write(rootB, "7.json", rec("s-numeric", "interactive", strconv.FormatUint(ft, 10)))

	got := LiveClaudeSessions([]string{rootA, rootB, filepath.Join(rootA, "missing")})

	if len(got) != 3 {
		t.Errorf("got %d live sessions, want 3: %v", len(got), got)
	}
	live, ok := got["s-live"]
	if !ok {
		t.Fatalf("s-live not live: %v", got)
	}
	if live.PID != pid || !live.Interactive || live.StartedAt.UnixMilli() != startedMs {
		t.Errorf("s-live = %+v", live)
	}
	if bg, ok := got["s-background"]; !ok || bg.Interactive {
		t.Errorf("s-background = %+v, %v; want live, not interactive", bg, ok)
	}
	if _, ok := got["s-numeric"]; !ok {
		t.Errorf("numeric procStart not accepted")
	}
	for _, id := range []string{"s-wrongstart", "s-nostart", "s-nopid", "s-txt"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s reported live", id)
		}
	}
}
