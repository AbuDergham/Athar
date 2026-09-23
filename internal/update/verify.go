package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"aead.dev/minisign"
)

// parsePublicKey decodes the embedded key: the base64 line printed by
// tools/keygen or a whole minisign .pub file. Empty means ErrNoPublicKey.
func parsePublicKey(text string) (minisign.PublicKey, error) {
	var pk minisign.PublicKey
	text = strings.TrimSpace(text)
	if text == "" {
		return pk, ErrNoPublicKey
	}
	if err := pk.UnmarshalText([]byte(text)); err != nil {
		return pk, fmt.Errorf("update: embedded public key is invalid: %w", err)
	}
	return pk, nil
}

// verifySignature checks a minisign signature (EdDSA or the prehashed
// variant, trusted comment included) of data by pub.
func verifySignature(pub minisign.PublicKey, data, sig []byte) error {
	if len(sig) == 0 || !minisign.Verify(pub, data, sig) {
		return ErrBadSignature
	}
	return nil
}

// checksumFor returns the SHA-256 listed for file in a sha256sum-style
// checksums.txt ("<hex>  <name>" per line, as GoReleaser writes it). The name
// must match exactly.
func checksumFor(sums []byte, file string) ([]byte, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != file {
			continue
		}
		sum, err := hex.DecodeString(fields[0])
		if err != nil || len(sum) != sha256.Size {
			return nil, fmt.Errorf("update: malformed checksum for %s in %s", file, checksumsAsset)
		}
		return sum, nil
	}
	return nil, fmt.Errorf("update: %s is not listed in %s", file, checksumsAsset)
}

// downloadVerified streams rawURL into dst (created or truncated) while hashing
// it, and returns ErrChecksumMismatch unless its SHA-256 equals want. The
// caller removes dst on error.
func downloadVerified(ctx context.Context, rawURL, userAgent, dst string, want []byte) error {
	resp, err := get(ctx, rawURL, userAgent, "application/octet-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("update: cannot write next to athar.exe: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxAsset+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("update: download: %w", ctxErr)
		}
		return fmt.Errorf("update: download: %w", err)
	}
	if n > maxAsset {
		return fmt.Errorf("update: %s is larger than expected", urlBase(rawURL))
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return ErrChecksumMismatch
	}
	return nil
}
