//go:build !linux

// [INPUT]: 无
// [OUTPUT]: 对外提供非 Linux 平台的 main 桩，打印 NOT_RUN 并以 77 退出
// [POS]: pandora-client-auth-00044-root-runner 的非 Linux 桩，与 main_linux.go 互斥
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
