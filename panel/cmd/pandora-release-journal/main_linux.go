//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 platform/releasejournal 的 RunCLI
// [OUTPUT]: 对外提供 Linux 上的 main 入口
// [POS]: pandora-release-journal 的 Linux amd64/arm64 入口，逻辑全在 releasejournal；其他平台由 main_unsupported.go 拒绝
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"os"

	"github.com/aegispanel/aegis/internal/platform/releasejournal"
)

func main() { os.Exit(releasejournal.RunCLI(os.Args[1:])) }
