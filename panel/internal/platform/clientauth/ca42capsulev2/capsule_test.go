package ca42capsulev2

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
)

func TestParseCapsuleV2SupportedArchitecturesAndOpaqueState(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			values := capsuleValues(t, architecture)
			data, err := CanonicalBytes(values)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			capsule, err := Parse(data, digest)
			if err != nil {
				t.Fatal(err)
			}
			data[0] ^= 0xff
			capsule.snapshot.values[FieldReleaseID] = "attacker"
			snapshot, err := capsule.Snapshot()
			if err != nil || snapshot.Value(FieldReleaseID) != "release-1" {
				t.Fatalf("capsule trusted state changed: %v", err)
			}
			capsule.sha256[0] ^= 0xff
			if _, err := capsule.VerifiedCopy(); err == nil {
				t.Fatal("mutated capsule identity accepted")
			}
		})
	}
}

func TestCapsuleV2RejectsVersionCanonicalAndArtifactDrift(t *testing.T) {
	base, err := CanonicalBytes(capsuleValues(t, "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func([]byte) []byte{
		"v1": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), Format, "client-auth-00042-trust-capsule-v1", 1))
		},
		"profile": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "profile_id="+ca42protocolv2.ProfileID, "profile_id=client-auth-00042-v1", 1))
		},
		"uppercase_hash": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "attestation_sha256=", "attestation_sha256=A", 1))
		},
		"duplicate_hash": func(value []byte) []byte {
			lines := strings.Split(string(value), "\n")
			var first string
			for index, line := range lines {
				if strings.HasPrefix(line, "attestation_sha256=") {
					first = strings.TrimPrefix(line, "attestation_sha256=")
				}
				if strings.HasPrefix(line, "expected_sha256=") {
					lines[index] = "expected_sha256=" + first
				}
			}
			return []byte(strings.Join(lines, "\n"))
		},
		"crlf": func(value []byte) []byte { return []byte(strings.ReplaceAll(string(value), "\n", "\r\n")) },
		"extra": func(value []byte) []byte {
			return append(value, []byte("execution_plan_sha256="+strings.Repeat("f", 64)+"\n")...)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := mutate(append([]byte(nil), base...))
			if _, err := Parse(candidate, sha256.Sum256(candidate)); err == nil {
				t.Fatal("invalid capsule v2 accepted")
			}
		})
	}
}

func capsuleValues(t *testing.T, architecture string) []string {
	t.Helper()
	values := make([]string, int(fieldCount))
	for index, name := range fieldNames {
		values[index] = "fixture"
		if strings.HasSuffix(name, "sha256") {
			values[index] = fmt.Sprintf("%x", sha256.Sum256([]byte("capsule-v2:"+name)))
		}
	}
	set := func(field Field, value string) { values[field] = value }
	profileSHA, err := ca42protocolv2.Strict().SHA256()
	if err != nil {
		t.Fatal(err)
	}
	set(FieldFormat, Format)
	set(FieldProfileID, ca42protocolv2.ProfileID)
	set(FieldProfileSHA256, fmt.Sprintf("%x", profileSHA))
	set(FieldTransition, ca42executionv2.Transition)
	set(FieldReleaseID, "release-1")
	set(FieldReleaseRunID, "run-1")
	set(FieldAttemptID, "attempt-1")
	set(FieldArchitecture, architecture)
	set(FieldAttestationCoreDevice, "10")
	set(FieldAttestationCoreMode, ca42executionv2.RequiredAttemptExecutableMode)
	set(FieldAttestationFormat, ca42protocolv2.AttestationFormat)
	set(FieldExpectedFormat, ca42protocolv2.ExpectedFormat)
	set(FieldBashDevice, "20")
	set(FieldBashMode, ca42executionv2.RequiredSystemExecutableMode)
	set(FieldDockerClientDevice, "20")
	set(FieldDockerClientMode, ca42executionv2.RequiredSystemExecutableMode)
	set(FieldGooseVersion, ca42executionv2.RequiredGooseVersion)
	set(FieldClientAuth00042SHA256, ca42manifest.FrozenMigrationSHA256)
	set(FieldArtifactStorageProfile, ca42executionv2.RequiredStorageProfile)
	set(FieldGlobalsDumpSizeBytes, "1024")
	set(FieldDatabaseDumpSizeBytes, "1073741824")
	set(FieldPostgresImageSHA256, strings.Repeat("c", 64))
	set(FieldSourceContainerID, strings.Repeat("a", 64))
	set(FieldSourceSystemIdentifier, "1111111111111111111")
	set(FieldSourceDatabaseName, "aegis")
	set(FieldSourceDatabaseOID, "16384")
	set(FieldSourceDatabaseOwnerOID, "10")
	set(FieldSourceDatabaseOwnerName, "aegis_owner")
	set(FieldSourceGooseWaterline, "41")
	set(FieldIsolatedContainerID, strings.Repeat("b", 64))
	set(FieldIsolatedSystemIdentifier, "2222222222222222222")
	set(FieldIsolatedNetworkID, strings.Repeat("d", 64))
	set(FieldIsolatedDatabaseName, "aegis")
	set(FieldIsolatedDatabaseOID, "24576")
	set(FieldIsolatedImageID, "sha256:"+strings.Repeat("c", 64))
	set(FieldIsolatedRunID, "pandoraisolatedpg18ABC123-1700000000")
	set(FieldLedgerNamespace, ca42protocolv2.LedgerNamespace)
	set(FieldLedgerDirectorySHA256, ca42executionv2.LedgerDirectorySHA256)
	return values
}
