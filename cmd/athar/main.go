// Command athar finds, reads and resumes your past conversations with any AI agent.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	fmt.Fprintln(os.Stderr, "athar", version, "- not implemented yet")
	os.Exit(1)
}
