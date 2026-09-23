//go:build !linux

package main

import (
	"io"
	"os"
)

func main() {
	_, _ = io.WriteString(os.Stderr, "root_runner=DENY stage=bootstrap reason=linux_required\n")
	os.Exit(77)
}
