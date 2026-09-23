package ca42executionv2

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
)

func TestParseExactPlanV2(t *testing.T) {
	data, digest := planV2Fixture(t)
	plan, err := Parse(data, digest, "amd64", time.Unix(1700000100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	planSHA, err := SHA256Hex(plan)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Architecture != "amd64" || plan.GooseVersion != "v3.24.1" || plan.DatabaseDumpSizeBytes != 1<<30 || planSHA == "" {
		t.Fatalf("unexpected plan v2 projection: %+v", plan)
	}
	if _, err := plan.VerifiedCopyAt(time.Unix(1700003600, 0).UTC()); err == nil {
		t.Fatal("expired plan v2 capability accepted")
	}
}

func TestParsePlanV2RejectsVersionDefaultsAndDrift(t *testing.T) {
	data, _ := planV2Fixture(t)
	tests := map[string]func([]byte) []byte{
		"v1_format": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), Format, "pandora-ca42-execution-plan-v1", 1))
		},
		"missing_credential": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "credential_source_descriptor_sha256="+strings.Repeat("1", 64), "credential_source_descriptor_sha256=", 1))
		},
		"bash_path_mode": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "bash_mode=0755", "bash_mode=0500", 1))
		},
		"docker_mode": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "docker_client_mode=0755", "docker_client_mode=0775", 1))
		},
		"goose_devel": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "goose_version=v3.24.1", "goose_version=(devel)", 1))
		},
		"goose_prerelease": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "goose_version=v3.24.1", "goose_version=v3.24.1-rc.1", 1))
		},
		"goose_leading_zero": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "goose_version=v3.24.1", "goose_version=v3.024.1", 1))
		},
		"goose_unfrozen": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "goose_version=v3.24.1", "goose_version=v4.0.0", 1))
		},
		"storage_unsealed": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "artifact_storage_profile=fs-verity-sha256-v2", "artifact_storage_profile=readonly-bind-v1", 1))
		},
		"storage_undefined_dm_verity": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "artifact_storage_profile=fs-verity-sha256-v2", "artifact_storage_profile=dm-verity-snapshot-v1", 1))
		},
		"database_too_large": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "database_dump_size_bytes=1073741824", "database_dump_size_bytes=1099511627777", 1))
		},
		"mixed_namespace": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "ledger_namespace="+LedgerNamespace, "ledger_namespace=client-auth-00042-v1", 1))
		},
		"invalid_profile_id": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "profile_id=client-auth-00042-v2", "profile_id=INVALID PROFILE", 1))
		},
		"invalid_profile_sha": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "profile_sha256=", "profile_sha256=0", 1))
		},
		"zero_closure": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "runtime_closure_manifest_sha256="+strings.Repeat("f", 64), "runtime_closure_manifest_sha256="+strings.Repeat("0", 64), 1))
		},
		"duplicate_security_artifacts": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "artifact_storage_descriptor_sha256="+strings.Repeat("e", 64), "artifact_storage_descriptor_sha256="+strings.Repeat("1", 64), 1))
		},
		"crlf":  func(value []byte) []byte { return []byte(strings.ReplaceAll(string(value), "\n", "\r\n")) },
		"extra": func(value []byte) []byte { return append(value, []byte("unknown=x\n")...) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := mutate(append([]byte(nil), data...))
			digest := sha256.Sum256(candidate)
			if _, err := Parse(candidate, digest, "amd64", time.Unix(1700000100, 0).UTC()); err == nil {
				t.Fatal("invalid plan v2 accepted")
			}
		})
	}
}

func planV2Fixture(t *testing.T) ([]byte, [sha256.Size]byte) {
	t.Helper()
	values := make([]string, len(fieldNames))
	for index := range values {
		values[index] = "x"
	}
	set := func(name, value string) {
		t.Helper()
		for index, candidate := range fieldNames {
			if candidate == name {
				values[index] = value
				return
			}
		}
		t.Fatalf("unknown fixture field %s", name)
	}
	set("format", Format)
	set("status", Status)
	set("transition", Transition)
	set("release_id", "release-1")
	set("release_run_id", "run-1")
	set("attempt_id", "attempt-1")
	set("architecture", "amd64")
	set("profile_id", RequiredProfileID)
	set("profile_sha256", RequiredProfileSHA256)
	hashNames := []string{
		"credential_source_descriptor_sha256", "pathtrust_binary_sha256", "pathtrust_chain_sha256", "trust_capsule_sha256",
		"attestation_core_sha256", "attestation_core_chain_sha256", "attestation_sha256", "expected_sha256",
		"attestation_public_key_sha256", "external_manifest_sha256", "bash_binary_sha256", "bash_chain_sha256",
		"docker_client_sha256", "docker_client_chain_sha256", "runtime_closure_manifest_sha256", "manifest_verifier_sha256",
		"preflight_runner_sha256", "migration_runner_sha256", "goose_binary_sha256", "goose_build_info_sha256",
		"migration_set_sha256", "artifact_storage_descriptor_sha256", "globals_dump_sha256", "database_dump_sha256",
		"postgres_image_sha256", "release_journal_head_sha256", "release_journal_snapshot_sha256", "ledger_directory_sha256",
		"source_container_id", "isolated_container_id", "isolated_network_id",
	}
	for _, name := range hashNames {
		digest := sha256.Sum256([]byte("plan-v2-fixture:" + name))
		set(name, fmt.Sprintf("%x", digest))
	}
	set("credential_source_descriptor_sha256", strings.Repeat("1", 64))
	set("runtime_closure_manifest_sha256", strings.Repeat("f", 64))
	set("artifact_storage_descriptor_sha256", strings.Repeat("e", 64))
	set("attestation_sha256", strings.Repeat("a", 64))
	set("expected_sha256", strings.Repeat("b", 64))
	set("external_manifest_sha256", strings.Repeat("c", 64))
	set("client_auth_00042_sha256", ca42manifest.FrozenMigrationSHA256)
	set("pathtrust_device", "10")
	set("pathtrust_mode", RequiredAttemptExecutableMode)
	set("attestation_core_device", "10")
	set("attestation_core_mode", RequiredAttemptExecutableMode)
	set("bash_device", "20")
	set("bash_mode", RequiredSystemExecutableMode)
	set("docker_client_device", "20")
	set("docker_client_mode", RequiredSystemExecutableMode)
	set("goose_version", "v3.24.1")
	set("artifact_storage_profile", "fs-verity-sha256-v2")
	set("globals_dump_size_bytes", "1024")
	set("database_dump_size_bytes", "1073741824")
	set("ledger_namespace", LedgerNamespace)
	set("ledger_directory_sha256", LedgerDirectorySHA256)
	set("source_system_identifier", "100")
	set("source_database", "pandora")
	set("source_database_oid", "101")
	set("source_database_owner_oid", "102")
	set("source_database_owner_name", "pandora_owner")
	set("source_goose_waterline", "41")
	set("isolated_system_identifier", "200")
	set("isolated_database", "pandora")
	set("isolated_database_oid", "201")
	set("isolated_image_id", "sha256:"+valueFor(values, "postgres_image_sha256"))
	set("isolated_run_id", "pandoraisolatedpg18abc123-1700000000")
	set("not_before_epoch", "1700000000")
	set("not_after_epoch", "1700003600")
	data, err := CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	return data, sha256.Sum256(data)
}

func valueFor(values []string, name string) string {
	for index, candidate := range fieldNames {
		if candidate == name {
			return values[index]
		}
	}
	return ""
}
