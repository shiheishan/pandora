//go:build !linux

package main

import (
	"fmt"
	"io"
	"os"
)

const unsupportedPlatformExit = 77

func main() {
	os.Exit(runUnsupported(os.Stderr))
}

func runUnsupported(stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, "client_auth_00044_root_runner=NOT_RUN reason=linux_required db=NOT_CONNECTED network=NOT_USED")
	return unsupportedPlatformExit
}
