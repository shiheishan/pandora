package ca42releasev3

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
)

func TestBindExecutionPlanV2UsesTrustedCopiesAndProfile(t *testing.T) {
	fixture := newV3Fixture(t, "amd64")
	if err := BindExecutionPlan(fixture.manifest, fixture.plan, fixture.now); err != nil {
		t.Fatal(err)
	}
	mutatedProjection := fixture.plan
	mutatedProjection.ReleaseID = "attacker"
	mutatedProjection.ProfileID = "client-auth-00042-v1"
	if err := BindExecutionPlan(fixture.manifest, mutatedProjection, fixture.now); err != nil {
		t.Fatalf("binder trusted mutable plan projection: %v", err)
	}

	planValues := append([]string(nil), fixture.planValues...)
	setPlanValue(t, planValues, "release_id", "release-2")
	planBytes, err := ca42executionv2.CanonicalBytes(planValues)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(planBytes)
	changedPlan, err := ca42executionv2.Parse(planBytes, digest, fixture.architecture, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := BindExecutionPlan(fixture.manifest, changedPlan, fixture.now); err == nil {
		t.Fatal("different canonical release identity bound")
	}

	planValues = append([]string(nil), fixture.planValues...)
	setPlanValue(t, planValues, "profile_id", "client-auth-00042-v1")
	planBytes, err = ca42executionv2.CanonicalBytes(planValues)
	if err != nil {
		t.Fatal(err)
	}
	digest = sha256.Sum256(planBytes)
	if _, err = ca42executionv2.Parse(planBytes, digest, fixture.architecture, fixture.now); err == nil {
		t.Fatal("mixed admission profile parsed as a Plan v2 capability")
	}
}

func TestBindExecutionPlanV2AllMutableDuplicatedFields(t *testing.T) {
	fixture := newV3Fixture(t, "amd64")
	h := func(label string) string { return fmt.Sprintf("%x", hashFor("plan-mutation:"+label)) }
	tests := []struct {
		name         string
		architecture string
		changes      map[string]string
	}{
		{"release_id", "amd64", map[string]string{"release_id": "release-2"}},
		{"release_run_id", "amd64", map[string]string{"release_run_id": "release-run-2"}},
		{"attempt_id", "amd64", map[string]string{"attempt_id": "attempt-2"}},
		{"architecture", "arm64", map[string]string{"architecture": "arm64"}},
		{"credential_descriptor", "amd64", map[string]string{"credential_source_descriptor_sha256": h("credential")}},
		{"pathtrust_binary", "amd64", map[string]string{"pathtrust_binary_sha256": h("pathtrust-binary")}},
		{"pathtrust_chain", "amd64", map[string]string{"pathtrust_chain_sha256": h("pathtrust-chain")}},
		{"pathtrust_device", "amd64", map[string]string{"pathtrust_device": "11"}},
		{"trust_capsule", "amd64", map[string]string{"trust_capsule_sha256": h("capsule")}},
		{"attestation_core", "amd64", map[string]string{"attestation_core_sha256": h("attestation-core")}},
		{"attestation_core_chain", "amd64", map[string]string{"attestation_core_chain_sha256": h("attestation-core-chain")}},
		{"attestation_core_device", "amd64", map[string]string{"attestation_core_device": "11"}},
		{"attestation", "amd64", map[string]string{"attestation_sha256": h("attestation")}},
		{"expected", "amd64", map[string]string{"expected_sha256": h("expected")}},
		{"attestation_key", "amd64", map[string]string{"attestation_public_key_sha256": h("attestation-key")}},
		{"external_manifest", "amd64", map[string]string{"external_manifest_sha256": h("external")}},
		{"bash_binary", "amd64", map[string]string{"bash_binary_sha256": h("bash")}},
		{"bash_chain", "amd64", map[string]string{"bash_chain_sha256": h("bash-chain")}},
		{"bash_device", "amd64", map[string]string{"bash_device": "21"}},
		{"docker_binary", "amd64", map[string]string{"docker_client_sha256": h("docker")}},
		{"docker_chain", "amd64", map[string]string{"docker_client_chain_sha256": h("docker-chain")}},
		{"docker_device", "amd64", map[string]string{"docker_client_device": "21"}},
		{"runtime_closure", "amd64", map[string]string{"runtime_closure_manifest_sha256": h("closure")}},
		{"manifest_verifier", "amd64", map[string]string{"manifest_verifier_sha256": h("verifier")}},
		{"preflight_runner", "amd64", map[string]string{"preflight_runner_sha256": h("preflight")}},
		{"migration_runner", "amd64", map[string]string{"migration_runner_sha256": h("migration-runner")}},
		{"goose_binary", "amd64", map[string]string{"goose_binary_sha256": h("goose")}},
		{"goose_build_info", "amd64", map[string]string{"goose_build_info_sha256": h("goose-info")}},
		{"migration_set", "amd64", map[string]string{"migration_set_sha256": h("migration-set")}},
		{"storage_descriptor", "amd64", map[string]string{"artifact_storage_descriptor_sha256": h("storage")}},
		{"globals_dump", "amd64", map[string]string{"globals_dump_sha256": h("globals")}},
		{"globals_size", "amd64", map[string]string{"globals_dump_size_bytes": "2048"}},
		{"database_dump", "amd64", map[string]string{"database_dump_sha256": h("database")}},
		{"database_size", "amd64", map[string]string{"database_dump_size_bytes": "1073741825"}},
		{"postgres_image_pair", "amd64", map[string]string{"postgres_image_sha256": strings.Repeat("e", 64), "isolated_image_id": "sha256:" + strings.Repeat("e", 64)}},
		{"journal_head", "amd64", map[string]string{"release_journal_head_sha256": h("journal-head")}},
		{"journal_snapshot", "amd64", map[string]string{"release_journal_snapshot_sha256": h("journal-snapshot")}},
		{"source_container", "amd64", map[string]string{"source_container_id": strings.Repeat("e", 64)}},
		{"source_system", "amd64", map[string]string{"source_system_identifier": "3333333333333333333"}},
		{"source_database_pair", "amd64", map[string]string{"source_database": "pandora", "isolated_database": "pandora"}},
		{"source_database_oid", "amd64", map[string]string{"source_database_oid": "16385"}},
		{"source_owner_oid", "amd64", map[string]string{"source_database_owner_oid": "11"}},
		{"source_owner", "amd64", map[string]string{"source_database_owner_name": "other_owner"}},
		{"isolated_container", "amd64", map[string]string{"isolated_container_id": strings.Repeat("e", 64)}},
		{"isolated_system", "amd64", map[string]string{"isolated_system_identifier": "3333333333333333333"}},
		{"isolated_network", "amd64", map[string]string{"isolated_network_id": strings.Repeat("e", 64)}},
		{"isolated_database_oid", "amd64", map[string]string{"isolated_database_oid": "24577"}},
		{"isolated_run", "amd64", map[string]string{"isolated_run_id": "pandoraisolatedpg18XYZ789-1700000001"}},
		{"not_before", "amd64", map[string]string{"not_before_epoch": "1700000001"}},
		{"not_after", "amd64", map[string]string{"not_after_epoch": "1700003599"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			planValues := append([]string(nil), fixture.planValues...)
			for name, value := range test.changes {
				setPlanValue(t, planValues, name, value)
			}
			planBytes, err := ca42executionv2.CanonicalBytes(planValues)
			if err != nil {
				t.Fatal(err)
			}
			planDigest := sha256.Sum256(planBytes)
			plan, err := ca42executionv2.Parse(planBytes, planDigest, test.architecture, fixture.now)
			if err != nil {
				t.Fatalf("mutation must remain a valid Plan v2: %v", err)
			}
			manifestValues := append([]string(nil), fixture.values...)
			setManifestValue(t, manifestValues, FieldExecutionPlanSHA256, fmt.Sprintf("%x", planDigest))
			manifest := parseSignedValues(t, fixture, manifestValues)
			if err := BindExecutionPlan(manifest, plan, fixture.now); err == nil {
				t.Fatal("unbound Plan v2 field accepted")
			}
		})
	}
}

func setPlanValue(t *testing.T, values []string, name, value string) {
	t.Helper()
	for index, candidate := range ca42executionv2.CanonicalFieldNames() {
		if candidate == name {
			values[index] = value
			return
		}
	}
	t.Fatalf("unknown plan field: %s", name)
}
