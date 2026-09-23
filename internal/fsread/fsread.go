// Package fsread reads agent files without disturbing the agents:
// files are opened with FILE_SHARE_READ|WRITE|DELETE and closed right away,
// and SQLite databases are only ever read from a private temp copy.
//
// Nothing in this package writes, creates, locks or deletes anything next to
// the files it reads.
package fsread

import (
	"bytes"
	"io"
)

// Open opens a file for reading with full sharing (read, write, delete) so the
// owning agent can keep appending to, renaming or deleting it. Callers must Close quickly.
// The returned value is an *os.File; errors are *fs.PathError (errors.Is(err, fs.ErrNotExist) works).
func Open(path string) (io.ReadCloser, error) {
	f, err := openFile(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// ReadFile reads a whole file through Open.
func ReadFile(path string) ([]byte, error) {
	f, err := openFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var buf bytes.Buffer
	if info, err := f.Stat(); err == nil && info.Size() > 0 && int64(int(info.Size())) == info.Size() {
		// The agent may still append; the size is only a hint.
		buf.Grow(int(info.Size()) + bytes.MinRead)
	}
	if _, err := buf.ReadFrom(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
