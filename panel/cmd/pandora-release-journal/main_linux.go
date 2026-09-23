//go:build linux && (amd64 || arm64)

package main

import (
	"os"

	"github.com/aegispanel/aegis/internal/platform/releasejournal"
)

func main() { os.Exit(releasejournal.RunCLI(os.Args[1:])) }
