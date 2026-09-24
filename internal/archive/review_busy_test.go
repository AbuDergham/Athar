package archive

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AbuDergham/Athar/internal/model"
)

// How long does one big source (an AgentsView import, a Cursor/Trae DB)
// hold the write lock?
func TestReviewBigSourceTxDuration(t *testing.T) {
	d := openTemp(t)
	var ss []model.Session
	para := strings.Repeat("lorem ipsum dolor sit amet consectetur ", 12)
	for i := 0; i < 1000; i++ {
		s := live(fmt.Sprintf("s%04d", i), "title", "x")
		s.Messages = nil
		for j := 0; j < 60; j++ {
			s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Kind: model.KindText, At: t0, Text: fmt.Sprintf("%d %d %s", i, j, para)})
		}
		ss = append(ss, s)
	}
	start := time.Now()
	mustApply(t, d, src("big.db"), ss...)
	t.Logf("ApplySource of 60,000 messages (~%d MB): %v", 60000*len(para)/1e6, time.Since(start))
}

// A second process opening the archive while the first one holds a write
// transaction longer than the busy timeout, when the weekly full check is due.
func TestReviewOpenDuringLongWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "athar-data", "archive.db")
	d1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d1.Close()
	mustApply(t, d1, src("a"), live("s1", "t", "hello"))
	if err := d1.SetMeta("last_check_at", time.Now().Add(-8*24*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error)
	go func() {
		done <- d1.inTx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('x', 'y')`); err != nil {
				return err
			}
			close(started)
			time.Sleep(7 * time.Second) // one big source being applied
			return nil
		})
	}()
	<-started
	t0 := time.Now()
	d2, err := Open(path)
	t.Logf("second Open after %v: err=%v", time.Since(t0).Round(time.Millisecond), err)
	if err != nil {
		t.Errorf("BUG: the archive cannot be opened while another process applies a big source: %v", err)
	} else {
		d2.Close()
	}
	<-done
}
