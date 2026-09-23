//go:build !ca42e2e

package ca42runner

import "github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"

// compiledRootKeyset is the only implementation selected by ordinary source
// builds. It deliberately contains no authority material and fails closed.
func compiledRootKeyset() (ca42authority.RootKeyset, error) {
	return ca42authority.RootKeyset{}, errCompiledRootsUnprovisioned
}
