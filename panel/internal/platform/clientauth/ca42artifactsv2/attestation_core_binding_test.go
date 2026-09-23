package ca42artifactsv2

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifiedCopyBoundToAttestationCoreAt(t *testing.T) {
	fixture := newGraphFixture(t, "amd64", "core-binding")
	set, err := New(fixture.inputs, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	digest := mustFixtureSHA256(t, fixture.inputs.Plan.AttestationCoreSHA256)
	chain := mustFixtureSHA256(t, fixture.inputs.Plan.AttestationCoreChainSHA256)
	if _, err := set.VerifiedCopyBoundToAttestationCoreAt(fixture.now, fixture.inputs.Plan.AttemptID, digest, chain,
		fixture.inputs.Plan.AttestationCoreDevice, 0o500); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		attempt string
		digest  [sha256.Size]byte
		chain   [sha256.Size]byte
		device  uint64
		mode    uint32
	}{
		{"attempt", "wrong", digest, chain, fixture.inputs.Plan.AttestationCoreDevice, 0o500},
		{"digest", fixture.inputs.Plan.AttemptID, flipFixtureDigest(digest), chain, fixture.inputs.Plan.AttestationCoreDevice, 0o500},
		{"chain", fixture.inputs.Plan.AttemptID, digest, flipFixtureDigest(chain), fixture.inputs.Plan.AttestationCoreDevice, 0o500},
		{"device", fixture.inputs.Plan.AttemptID, digest, chain, fixture.inputs.Plan.AttestationCoreDevice + 1, 0o500},
		{"mode", fixture.inputs.Plan.AttemptID, digest, chain, fixture.inputs.Plan.AttestationCoreDevice, 0o400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := set.VerifiedCopyBoundToAttestationCoreAt(fixture.now, test.attempt, test.digest, test.chain, test.device, test.mode); err == nil {
				t.Fatal("accepted mismatched attestation-core evidence")
			}
		})
	}
}

func mustFixtureSHA256(t *testing.T, value string) [sha256.Size]byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		t.Fatal("fixture SHA256 invalid")
	}
	var result [sha256.Size]byte
	copy(result[:], decoded)
	return result
}

func flipFixtureDigest(value [sha256.Size]byte) [sha256.Size]byte {
	value[0] ^= 1
	return value
}
