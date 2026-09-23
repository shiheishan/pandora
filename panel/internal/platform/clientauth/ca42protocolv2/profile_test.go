package ca42protocolv2

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestStrictProfileFreezesEntireVersionFamily(t *testing.T) {
	profile := Strict()
	if !profile.IsStrict() {
		t.Fatal("strict CA42 v2 profile did not validate")
	}
	canonical, err := profile.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 808 {
		t.Fatalf("strict profile canonical length changed: %d", len(canonical))
	}
	digest, err := profile.SHA256()
	if err != nil || hex.EncodeToString(digest[:]) != "3a7d7f5b88f204d57a7f0e25913064c756a91f665ce3906830d6a92bf484455c" {
		t.Fatalf("strict profile identity changed: digest=%x err=%v", digest, err)
	}
	for _, required := range []string{
		"release_manifest_format=" + ReleaseManifestFormat + "\n",
		"release_contract_core_format=" + ReleaseContractCoreFormat + "\n",
		"execution_plan_format=" + ExecutionPlanFormat + "\n",
		"trust_capsule_format=" + TrustCapsuleFormat + "\n",
		"attestation_format=" + AttestationFormat + "\n",
		"expected_format=" + ExpectedFormat + "\n",
		"artifact_storage_format=" + ArtifactStorageFormat + "\n",
		"artifact_storage_profile=" + ArtifactStorageProfile + "\n",
		"release_journal_format=" + ReleaseJournalFormat + "\n",
		"ledger_namespace=" + LedgerNamespace + "\n",
	} {
		if !bytes.Contains(canonical, []byte(required)) {
			t.Fatalf("strict profile omitted %q", required)
		}
	}
	copyBytes := append([]byte(nil), canonical...)
	copyBytes[0] ^= 0xff
	canonicalAgain, err := profile.CanonicalBytes()
	if err != nil || bytes.Equal(copyBytes, canonicalAgain) {
		t.Fatal("profile canonical bytes were exposed as mutable state")
	}
}

func TestProfileRejectsZeroAndMutatedCapabilities(t *testing.T) {
	if (Profile{}).IsStrict() {
		t.Fatal("zero CA42 v2 profile validated")
	}
	mutatedBytes := Strict()
	mutatedBytes.canonical[0] ^= 0xff
	if mutatedBytes.IsStrict() {
		t.Fatal("mutated CA42 v2 profile bytes validated")
	}
	mutatedHash := Strict()
	mutatedHash.sha256[0] ^= 0xff
	if mutatedHash.IsStrict() {
		t.Fatal("mutated CA42 v2 profile hash validated")
	}
}
