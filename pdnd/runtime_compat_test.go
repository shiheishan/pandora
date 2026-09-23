//go:build compat

package main

import (
	"log/slog"
	"testing"
)

func TestCompatibilityRuntimeIsOptIn(t *testing.T) {
	if runtimeNativeOnly {
		t.Fatal("compat build must advertise native_only=false")
	}
	runtime, err := newRuntime(slog.Default(), false)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Type() != "pandora-native+compat" {
		t.Fatalf("compat runtime type=%q, want pandora-native+compat", runtime.Type())
	}
}
