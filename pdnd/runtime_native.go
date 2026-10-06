//go:build !compat

// [INPUT]: 依赖 kernel 的 NewNativeCoreWithLogger，依赖 core 的 Core 契约与 main.go 传入的 *slog.Logger
// [OUTPUT]: 包内提供 newRuntime（默认构建）与 runtimeNativeOnly=true
// [POS]: pdnd 默认构建的运行时选择：只链接 NativeCore，native_only:false 直接拒绝；与 runtime_compat.go 按构建标签二选一
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"fmt"
	"log/slog"

	"github.com/aegispanel/nodeagent/core"
	nativekernel "github.com/aegispanel/nodeagent/kernel"
)

const runtimeNativeOnly = true

// newRuntime is the default production runtime. The normal binary links only
// Pandora NativeCore; compatibility cores are deliberately isolated behind
// the opt-in compat build tag.
// The process logger is handed to NativeCore so per-connection failures
// (rate-limited, redacted) land in the same stream as the rest of the node.
func newRuntime(log *slog.Logger, nativeOnly bool) (core.Core, error) {
	if !nativeOnly {
		return nil, fmt.Errorf("native_only:false requires a separately built compatibility binary (-tags compat)")
	}
	return nativekernel.NewNativeCoreWithLogger(nil, log), nil
}
