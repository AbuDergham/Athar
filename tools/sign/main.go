// Command sign writes a minisign signature for a release file.
//
//	MINISIGN_PRIVATE_KEY="$(cat athar.key)" go run ./tools/sign <file> <file>.minisig
//
// GoReleaser calls it for checksums.txt (see .goreleaser.yaml).
package main

import (
	"fmt"
	"os"
	"strings"

	"aead.dev/minisign"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sign <file> <signature-out>")
		os.Exit(2)
	}
	keyText := os.Getenv("MINISIGN_PRIVATE_KEY")
	if keyText == "" {
		fmt.Fprintln(os.Stderr, "MINISIGN_PRIVATE_KEY is not set")
		os.Exit(1)
	}
	// The secret may hold the whole key file or only its base64 line.
	if !strings.HasPrefix(strings.TrimSpace(keyText), "untrusted comment:") {
		keyText = "untrusted comment: minisign encrypted secret key\n" + strings.TrimSpace(keyText)
	}
	var priv minisign.PrivateKey
	if err := priv.UnmarshalText([]byte(keyText)); err != nil {
		fmt.Fprintln(os.Stderr, "bad private key:", err)
		os.Exit(1)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sig := minisign.Sign(priv, data)
	if err := os.WriteFile(os.Args[2], sig, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
