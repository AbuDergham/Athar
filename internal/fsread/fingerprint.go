package fsread

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"time"
)

// Stat returns size and modification time without opening the file for writing.
func Stat(path string) (size int64, mtime time.Time, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, time.Time{}, err
	}
	return info.Size(), info.ModTime(), nil
}

// Fingerprint returns a stable string built from (path, size, mtime) of every
// existing file in paths, in the given order. Missing files are skipped.
//
// It is the SHA-256 hex digest of "path|size|mtimeUnixNano;" concatenated over
// the existing files. Errors other than "does not exist" are returned.
func Fingerprint(paths []string) (string, error) {
	h := sha256.New()
	var buf []byte
	for _, p := range paths {
		size, mtime, err := Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		buf = append(buf[:0], p...)
		buf = append(buf, '|')
		buf = strconv.AppendInt(buf, size, 10)
		buf = append(buf, '|')
		buf = strconv.AppendInt(buf, mtime.UnixNano(), 10)
		buf = append(buf, ';')
		h.Write(buf)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SQLiteSidecars returns db plus the -wal / -shm paths that currently exist.
func SQLiteSidecars(db string) []string {
	out := []string{db}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(db + suffix); err == nil {
			out = append(out, db+suffix)
		}
	}
	return out
}
