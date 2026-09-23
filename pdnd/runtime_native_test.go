//go:build !compat

package main

import (
	"log/slog"
	"testing"
)

func TestDefaultRuntimeIsNativeCoreOnly(t *testing.T) {
	if !runtimeNativeOnly {
		t.Fatal("default build must advertise native_only=true")
	}
	runtime, err := newRuntime(slog.Default(), true)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Type() != "pandora-native" {
		t.Fatalf("default runtime type=%q, want pandora-native", runtime.Type())
	}
	if _, err := newRuntime(slog.Default(), false); err == nil {
		t.Fatal("default binary accepted native_only:false without compat build tag")
	}
}
