//go:build ca42e2e && linux && (amd64 || arm64)

package ca42runner

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
)

func TestCA42E2ECompiledRootKeysetIsExplicitAndPublicOnly(t *testing.T) {
	if roots, err := CompiledRootKeyset(); err == nil || !errors.Is(err, errCompiledRootsUnprovisioned) || len(roots.Keys) != 0 {
		t.Fatalf("tagged roots opened outside attested isolation: roots=%+v err=%v", roots, err)
	}
	roots := ca42E2EPublicRootKeyset()
	if roots.ID != "pandora-ca42-e2e-roots-v1" || roots.Quorum != ca42authority.RequiredQuorum || len(roots.Keys) != 3 {
		t.Fatalf("unexpected E2E root keyset: %+v", roots)
	}
	expected := []string{
		"d54207da194977dcf46adbfec2bc2e75b52d5a8a42184fedfdc00024f0e3e8da",
		"511c34a1a2cb521df16bb246b8de8e7997ce235c7e76b22a3d7503a24819dd8a",
		"31debe55d37c722768b137131caa6087080b2e0b60b94bd785d14575cfa498bc",
	}
	for index, key := range roots.Keys {
		if key.ID != "root-"+string(rune('a'+index)) || hex.EncodeToString(key.PublicKey) != expected[index] {
			t.Fatalf("unexpected E2E public key %d: %+v", index, key)
		}
	}
}
