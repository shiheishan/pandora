//go:build !compat

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
func newRuntime(_ *slog.Logger, nativeOnly bool) (core.Core, error) {
	if !nativeOnly {
		return nil, fmt.Errorf("native_only:false requires a separately built compatibility binary (-tags compat)")
	}
	return nativekernel.NewNativeCore(nil), nil
}
