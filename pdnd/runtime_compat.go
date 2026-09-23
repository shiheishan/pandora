//go:build compat

package main

import (
	"log/slog"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/core/multi"
)

const runtimeNativeOnly = false

// newRuntime is available only for migration builds. It is never part of the
// default pandora-native artifact.
func newRuntime(log *slog.Logger, nativeOnly bool) (core.Core, error) {
	return multi.NewWithOptions(log, multi.Options{NativeOnly: nativeOnly}), nil
}
