package fsread

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// lineBufSize is the bufio buffer size; lines longer than this are still
// delivered whole (ReadBytes grows as needed).
const lineBufSize = 1 << 20

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Lines calls fn for every complete line of a JSONL file (lines of ANY length;
// never bufio.Scanner's 64 KiB limit). A final line without a trailing newline
// is NOT passed to fn (it may be half-written). fn returning an error stops the read.
//
// The trailing "\n" or "\r\n" is stripped, a leading UTF-8 BOM is dropped, and
// empty or whitespace-only lines are skipped. Each line is a fresh slice that fn
// may keep. fn's error is returned unchanged.
func Lines(path string, fn func(line []byte) error) error {
	f, err := openFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fnErr, err := readLines(f, false, fn)
	if fnErr != nil {
		return fnErr
	}
	return err
}

// ZstdLines is Lines for a zstd-compressed JSONL file (streaming decoder).
// Unlike Lines, a final line without a trailing newline IS delivered, because
// compressed files are written whole.
func ZstdLines(path string, fn func(line []byte) error) error {
	f, err := openFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return fmt.Errorf("zstd %s: %w", path, err)
	}
	defer dec.Close()
	fnErr, err := readLines(dec, true, fn)
	if fnErr != nil {
		return fnErr
	}
	if err != nil {
		return fmt.Errorf("zstd %s: %w", path, err)
	}
	return nil
}

// readLines splits r into lines and calls fn for each. deliverTail says whether
// a final line without a newline is delivered. It returns fn's error (fnErr)
// separately from read errors so callers can pass fn's error through unchanged.
func readLines(r io.Reader, deliverTail bool, fn func(line []byte) error) (fnErr, err error) {
	br := bufio.NewReaderSize(r, lineBufSize)
	first := true
	for {
		line, rerr := br.ReadBytes('\n')
		if rerr != nil && rerr != io.EOF {
			return nil, rerr
		}
		atEOF := rerr == io.EOF
		if atEOF && !deliverTail {
			return nil, nil // unterminated tail: may be half-written, left for the next sync
		}
		if first {
			line = bytes.TrimPrefix(line, utf8BOM)
			first = false
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(bytes.TrimSpace(line)) > 0 {
			if ferr := fn(line); ferr != nil {
				return ferr, nil
			}
		}
		if atEOF {
			return nil, nil
		}
	}
}
