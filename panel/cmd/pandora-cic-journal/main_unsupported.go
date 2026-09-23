//go:build !linux || (!amd64 && !arm64)

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "pandora-cic-journal: unsupported platform; Linux amd64/arm64 required")
	os.Exit(70)
}
