//go:build !linux || (!amd64 && !arm64)

// [INPUT]: 无
// [OUTPUT]: 对外提供不支持平台的 main 桩，打印原因并以 70 退出
// [POS]: pandora-release-journal 的非 Linux amd64/arm64 桩，与 main_linux.go 互斥
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "pandora-release-journal: unsupported platform; Linux amd64/arm64 required")
	os.Exit(70)
}
