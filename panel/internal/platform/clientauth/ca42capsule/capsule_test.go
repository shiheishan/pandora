package ca42capsule

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
)

func TestParseAndBindPlan(t *testing.T) {
	values := validCapsuleValues()
	data, err := CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	capsule, err := Parse(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	plan := matchingPlan(capsule)
	if err := BindPlan(capsule, plan); err != nil {
		t.Fatal(err)
	}
	plan.SourceSystemIdentifier = "999"
	if err := BindPlan(capsule, plan); err == nil {
		t.Fatal("accepted capsule target tuple from another production source")
	}
}

func TestParseRejectsTamperAndNoncanonicalValues(t *testing.T) {
	base, err := CanonicalBytes(validCapsuleValues())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(base)
	changed := append([]byte(nil), base...)
	changed[len(changed)-2] ^= 1
	if _, err := Parse(changed, digest); err == nil {
		t.Fatal("accepted capsule not pinned by execution plan")
	}

	mutations := []struct {
		name  string
		index int
		value string
	}{
		{name: "zero core hash", index: 4, value: strings.Repeat("0", 64)},
		{name: "uppercase hash", index: 8, value: strings.Repeat("A", 64)},
		{name: "zero device", index: 6, value: "0"},
		{name: "OID overflow", index: 14, value: "4294967296"},
		{name: "wrong ledger", index: 15, value: "other-ledger"},
		{name: "wrong ledger directory", index: 16, value: strings.Repeat("8", 64)},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			values := validCapsuleValues()
			values[mutation.index] = mutation.value
			data, err := CanonicalBytes(values)
			if err != nil {
				t.Fatal(err)
			}
			candidate := sha256.Sum256(data)
			if _, err := Parse(data, candidate); err == nil {
				t.Fatal("accepted invalid capsule")
			}
		})
	}
	withCR := append(append([]byte(nil), base[:len(base)-1]...), '\r', '\n')
	if _, err := Parse(withCR, sha256.Sum256(withCR)); err == nil {
		t.Fatal("accepted CRLF capsule")
	}
}

func validCapsuleValues() []string {
	return []string{
		Format, Transition, "release-ca42-1", "run-ca42-1",
		strings.Repeat("2", 64), strings.Repeat("3", 64), "2050", CoreMode,
		strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("e", 64), strings.Repeat("8", 64),
		"1111111111111111111", "aegis", "16384", ca42execution.LedgerNamespace,
		ca42execution.LedgerDirectorySHA256,
	}
}

func matchingPlan(capsule Capsule) ca42execution.Plan {
	return ca42execution.Plan{
		ReleaseID: capsule.ReleaseID, ReleaseRunID: capsule.ReleaseRunID,
		AttestationCoreSHA256: capsule.CoreSHA256, AttestationCoreChainSHA256: capsule.CoreChainSHA256,
		AttestationCoreDevice: capsule.CoreDevice, AttestationSHA256: capsule.AttestationSHA256,
		ExpectedSHA256: capsule.ExpectedSHA256, AttestationPublicKeySHA256: capsule.PublicKeySHA256,
		ExternalManifestSHA256: capsule.ExternalManifestSHA256, SourceSystemIdentifier: capsule.TargetSystemIdentifier,
		SourceDatabase: capsule.TargetDatabase, SourceDatabaseOID: capsule.TargetDatabaseOID,
	}
}
