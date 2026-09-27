//go:build linux

// [INPUT]: 依赖同包 run.go 的 runCLI
// [OUTPUT]: 对外提供 Linux 上的 main 入口
// [POS]: pandora-client-auth-00042-root-runner 的 Linux 入口；非 Linux 由 main_other.go 直接拒绝
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"context"
	"os"
)

func main() {
	os.Exit(runCLI(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
