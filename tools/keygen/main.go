// Command keygen creates the minisign key pair used to sign Athar releases.
//
//	go run ./tools/keygen <outdir>
//
// It writes <outdir>/athar.key (UNENCRYPTED private key: store it only as the
// MINISIGN_PRIVATE_KEY GitHub Actions secret, then delete the file) and
// <outdir>/athar.pub, and prints the public key line to embed in
// internal/update (PublicKey).
package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"aead.dev/minisign"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: keygen <outdir>")
		os.Exit(2)
	}
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	check(err)
	privText, err := priv.MarshalText()
	check(err)
	pubText, err := pub.MarshalText()
	check(err)
	dir := os.Args[1]
	check(os.MkdirAll(dir, 0o700))
	check(os.WriteFile(filepath.Join(dir, "athar.key"), privText, 0o600))
	check(os.WriteFile(filepath.Join(dir, "athar.pub"), pubText, 0o644))
	fmt.Println(pub.String())
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
