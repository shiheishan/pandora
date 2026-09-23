//go:build !linux || (!amd64 && !arm64)

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "pandora-pathtrust: unsupported platform; Linux amd64/arm64 is required")
	os.Exit(70)
}
