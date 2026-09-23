package syncer

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLockIsExclusive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "athar-data") // created by lock
	unlock, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, lockName)); err != nil {
		t.Fatalf("lock file: %v", err)
	}
	if u, err := lock(dir); !errors.Is(err, ErrLocked) {
		if err == nil {
			u()
		}
		t.Fatalf("second lock: err = %v, want ErrLocked", err)
	}
	unlock()
	unlock() // idempotent
	unlock2, err := lock(dir)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
}

// lockHelperEnv makes the test binary hold the lock of the named folder
// (see TestLockHelperProcess).
const lockHelperEnv = "ATHAR_SYNCER_LOCK_HELPER"

// TestLockHelperProcess is not a test: TestLockAcrossProcesses runs the test
// binary with lockHelperEnv set so that another process holds the lock until
// its stdin is closed or it is killed.
func TestLockHelperProcess(t *testing.T) {
	dir := os.Getenv(lockHelperEnv)
	if dir == "" {
		t.Skip("helper process for TestLockAcrossProcesses")
	}
	unlock, err := lock(dir)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	fmt.Println("locked")
	io.Copy(io.Discard, os.Stdin)
	unlock()
	os.Exit(0)
}

// TestLockAcrossProcesses: a second Athar process gets ErrLocked, and the lock
// is free again when the holder exits or crashes (no stale lock file state).
func TestLockAcrossProcesses(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the cross-process lock is implemented for Windows")
	}
	for _, how := range []string{"exit", "kill"} {
		t.Run(how, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
			cmd.Env = append(os.Environ(), lockHelperEnv+"="+dir)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			sc := bufio.NewScanner(stdout)
			locked := false
			for !locked && sc.Scan() {
				if strings.HasPrefix(sc.Text(), "error:") {
					t.Fatalf("helper: %s", sc.Text())
				}
				locked = sc.Text() == "locked"
			}
			if !locked {
				t.Fatal("helper process did not take the lock")
			}

			if u, err := lock(dir); !errors.Is(err, ErrLocked) {
				if err == nil {
					u()
				}
				t.Fatalf("lock held by another process: err = %v, want ErrLocked", err)
			}

			if how == "exit" {
				stdin.Close()
			} else {
				cmd.Process.Kill()
			}
			go io.Copy(io.Discard, stdout)
			cmd.Wait()
			// Windows frees the locks of an ended process shortly after it ends.
			deadline := time.Now().Add(5 * time.Second)
			for {
				unlock, err := lock(dir)
				if err == nil {
					unlock()
					return
				}
				if !errors.Is(err, ErrLocked) || time.Now().After(deadline) {
					t.Fatalf("lock after the holder ended (%s): %v", how, err)
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}

func TestMachineID(t *testing.T) {
	id := MachineID()
	if id == "" {
		t.Fatal("empty machine id")
	}
	if MachineID() != id {
		t.Error("machine id is not stable")
	}
	guid := regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	if runtime.GOOS == "windows" && !guid.MatchString(id) {
		host, _ := os.Hostname()
		if id != host {
			t.Errorf("machine id %q is neither a MachineGuid nor the hostname", id)
		}
		t.Logf("MachineGuid unavailable, fell back to the hostname")
	}
}
