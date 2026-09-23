package ca42runner

import (
	"errors"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
)

var errCompiledRootsUnprovisioned = errors.New("CA42 authority root keyset is not provisioned")

// CompiledRootKeyset delegates to exactly one compile-time implementation.
// Ordinary source builds select the fail-closed implementation. A release
// ceremony may replace it with generated public-key-only source; private
// authority material is never accepted through argv, environment, or the
// filesystem read by the runner.
func CompiledRootKeyset() (ca42authority.RootKeyset, error) {
	return compiledRootKeyset()
}
