//go:build !linux || (!amd64 && !arm64)

package ca42credential

import "testing"

func TestKernelCommitmentKeyLoaderFailsClosedOnUnsupportedPlatform(t *testing.T) {
	if key, err := LoadCommitmentKeyFD("keyring-ca42"); err == nil || key != nil {
		t.Fatal("unsupported platform exposed a credential commitment key FD")
	}
}
