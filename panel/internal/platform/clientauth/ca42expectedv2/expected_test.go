package ca42expectedv2

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
)

func TestParseExpectedV2SupportedArchitecturesAndValidity(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			data := expectedFixtureBytes(t, architecture, "")
			digest := sha256.Sum256(data)
			expected, err := Parse(data, digest, architecture, time.Unix(1700000100, 0).UTC())
			if err != nil {
				t.Fatal(err)
			}
			data[0] ^= 0xff
			expected.snapshot.values[FieldReleaseID] = "attacker"
			snapshot, err := expected.SnapshotAt(time.Unix(1700000100, 0).UTC())
			if err != nil || snapshot.Value(FieldReleaseID) != "release-1" {
				t.Fatalf("trusted expected state changed: %v", err)
			}
			if _, err := expected.VerifiedCopyAt(time.Unix(1700003600, 0).UTC()); err == nil {
				t.Fatal("expired expected v2 accepted")
			}
			expected.sha256[0] ^= 0xff
			if _, err := expected.VerifiedCopyAt(time.Unix(1700000100, 0).UTC()); err == nil {
				t.Fatal("mutated expected identity accepted")
			}
		})
	}
}

func TestExpectedV2RejectsCanonicalVersionAndCycleInjection(t *testing.T) {
	base := expectedFixtureBytes(t, "amd64", "")
	tests := map[string]func([]byte) []byte{
		"old_format": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), Format, "client-auth-attestation-expected-v1", 1))
		},
		"old_profile": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "profile_id="+ca42protocolv2.ProfileID, "profile_id=client-auth-00042-v1", 1))
		},
		"leading_zero_device": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "bash_device=20", "bash_device=020", 1))
		},
		"duplicate_artifact": func(value []byte) []byte {
			lines := strings.Split(string(value), "\n")
			var first string
			for index, line := range lines {
				if strings.HasPrefix(line, "bash_binary_sha256=") {
					first = strings.TrimPrefix(line, "bash_binary_sha256=")
				}
				if strings.HasPrefix(line, "docker_client_sha256=") {
					lines[index] = "docker_client_sha256=" + first
				}
			}
			return []byte(strings.Join(lines, "\n"))
		},
		"crlf": func(value []byte) []byte { return []byte(strings.ReplaceAll(string(value), "\n", "\r\n")) },
		"forbidden_plan_pin": func(value []byte) []byte {
			return append(value, []byte("execution_plan_sha256="+strings.Repeat("f", 64)+"\n")...)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := mutate(append([]byte(nil), base...))
			if _, err := Parse(candidate, sha256.Sum256(candidate), "amd64", time.Unix(1700000100, 0).UTC()); err == nil {
				t.Fatal("invalid expected v2 accepted")
			}
		})
	}
}

func expectedFixtureBytes(t *testing.T, architecture, publicKeySHA string) []byte {
	t.Helper()
	values := make([]string, int(fieldCount))
	for index, name := range fieldNames {
		values[index] = "fixture"
		if strings.HasSuffix(name, "sha256") {
			values[index] = fmt.Sprintf("%x", sha256.Sum256([]byte("expected-v2:"+name)))
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
	set(FieldAttestationFormat, ca42protocolv2.AttestationFormat)
	set(FieldAttestationSignatureAlgorithm, AttestationSignatureAlgorithm)
	set(FieldReleaseID, "release-1")
	set(FieldReleaseRunID, "run-1")
	set(FieldAttemptID, "attempt-1")
	set(FieldArchitecture, architecture)
	set(FieldTransition, ca42executionv2.Transition)
	if publicKeySHA != "" {
		set(FieldAttestationPublicKeySHA256, publicKeySHA)
	}
	set(FieldPathtrustDevice, "10")
	set(FieldPathtrustMode, ca42executionv2.RequiredAttemptExecutableMode)
	set(FieldAttestationCoreDevice, "10")
	set(FieldAttestationCoreMode, ca42executionv2.RequiredAttemptExecutableMode)
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
	set(FieldNotBeforeEpoch, "1700000000")
	set(FieldNotAfterEpoch, "1700003600")
	data, err := CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
