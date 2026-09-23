package ca42protocolv2_test

import (
	"slices"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42attestationv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsulev2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42expectedv2"
)

func TestV2ArtifactSchemasAreAcyclicAndDefensivelyExposed(t *testing.T) {
	forbidden := []string{
		"release_journal_head_sha256",
		"release_journal_snapshot_sha256",
		"execution_plan_sha256",
		"release_manifest_sha256",
		"release_contract_core_sha256",
	}
	tests := []struct {
		name  string
		names func() []string
	}{
		{"capsule_v2", ca42capsulev2.FieldNames},
		{"expected_v2", ca42expectedv2.FieldNames},
		{"attestation_v3", ca42attestationv3.FieldNames},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			names := test.names()
			if len(names) == 0 {
				t.Fatal("empty wire schema")
			}
			for _, field := range forbidden {
				if slices.Contains(names, field) {
					t.Fatalf("forbidden reverse pin in wire schema: %s", field)
				}
			}
			names[0] = "attacker"
			if test.names()[0] == "attacker" {
				t.Fatal("wire schema accessor exposed mutable storage")
			}
		})
	}
	for _, field := range []string{"attestation_sha256", "trust_capsule_sha256", "expected_sha256"} {
		if slices.Contains(ca42expectedv2.FieldNames(), field) {
			t.Fatalf("Expected v2 contains self/back edge: %s", field)
		}
	}
	for _, field := range []string{"attestation_sha256", "trust_capsule_sha256"} {
		if slices.Contains(ca42attestationv3.FieldNames(), field) {
			t.Fatalf("Attestation v3 contains self/back edge: %s", field)
		}
	}
	if slices.Contains(ca42capsulev2.FieldNames(), "trust_capsule_sha256") {
		t.Fatal("Capsule v2 contains a self edge")
	}
}
