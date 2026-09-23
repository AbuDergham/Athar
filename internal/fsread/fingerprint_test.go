package fsread

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStat(t *testing.T) {
	p := writeTemp(t, "a.jsonl", []byte("12345"))
	mt := time.Date(2026, 1, 2, 3, 4, 5, 600, time.UTC)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	size, mtime, err := Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if size != 5 {
		t.Errorf("size = %d, want 5", size)
	}
	if !mtime.Equal(mt) {
		t.Errorf("mtime = %v, want %v", mtime, mt)
	}
	if _, _, err := Stat(p + ".missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat(missing) = %v, want fs.ErrNotExist", err)
	}
}

func TestFingerprint(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.db")
	missing := filepath.Join(dir, "b.db-wal")
	mt := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	fp := func(paths ...string) string {
		t.Helper()
		s, err := Fingerprint(paths)
		if err != nil {
			t.Fatalf("Fingerprint: %v", err)
		}
		return s
	}

	base := fp(a, b)
	if len(base) != 64 {
		t.Fatalf("fingerprint %q is not a SHA-256 hex string", base)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|5|%d;%s|5|%d;", a, mt.UnixNano(), b, mt.UnixNano())))
	if want := hex.EncodeToString(sum[:]); base != want {
		t.Errorf("fingerprint = %s, want sha256(path|size|mtimeUnixNano;...) = %s", base, want)
	}
	if again := fp(a, b); again != base {
		t.Errorf("fingerprint not stable: %s vs %s", again, base)
	}
	if withMissing := fp(a, missing, b); withMissing != base {
		t.Errorf("missing file changed the fingerprint: %s vs %s", withMissing, base)
	}
	if fp(a) == base {
		t.Error("dropping an existing file did not change the fingerprint")
	}
	if e := fp(); len(e) != 64 || e != fp(missing) {
		t.Errorf("empty / all-missing fingerprints differ: %q vs %q", e, fp(missing))
	}

	// mtime change only
	mt2 := mt.Add(time.Second)
	if err := os.Chtimes(b, mt2, mt2); err != nil {
		t.Fatal(err)
	}
	afterMtime := fp(a, b)
	if afterMtime == base {
		t.Error("mtime change did not change the fingerprint")
	}

	// size change only (mtime restored)
	if err := os.WriteFile(b, []byte("hello, world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(b, mt2, mt2); err != nil {
		t.Fatal(err)
	}
	if afterSize := fp(a, b); afterSize == afterMtime || afterSize == base {
		t.Error("size change did not change the fingerprint")
	}
}

func TestSQLiteSidecars(t *testing.T) {
	cases := []struct {
		name  string
		exist []string // suffixes that exist besides the db
		want  []string // suffixes returned after the db
	}{
		{"db only", nil, nil},
		{"wal and shm", []string{"-wal", "-shm"}, []string{"-wal", "-shm"}},
		{"wal only", []string{"-wal"}, []string{"-wal"}},
		{"shm only", []string{"-shm"}, []string{"-shm"}},
		{"journal is not a sidecar", []string{"-journal"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.vscdb")
			if err := os.WriteFile(db, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, s := range c.exist {
				if err := os.WriteFile(db+s, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want := []string{db}
			for _, s := range c.want {
				want = append(want, db+s)
			}
			if got := SQLiteSidecars(db); !equalLines(got, want) {
				t.Errorf("SQLiteSidecars = %q, want %q", got, want)
			}
		})
	}
}
