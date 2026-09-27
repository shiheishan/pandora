//go:build !linux

// [INPUT]: 无
// [OUTPUT]: 对外提供非 Linux 平台的 main 桩，固定 DENY 并以 77 退出
// [POS]: pandora-client-auth-00042-root-runner 的非 Linux 桩，与 main_linux.go 互斥
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"io"
	"os"
)

func main() {
	_, _ = io.WriteString(os.Stderr, "root_runner=DENY stage=bootstrap reason=linux_required\n")
	os.Exit(77)
}
