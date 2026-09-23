package ca42attestationv3

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsulev2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42expectedv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
)

type attestationFixture struct {
	now       time.Time
	private   ed25519.PrivateKey
	publicPEM []byte
	expected  ca42expectedv2.Expected
	values    []string
	data      []byte
	digest    [sha256.Size]byte
}

func TestBindCapsuleComparesTheEntireCommonProjection(t *testing.T) {
	fixture := newAttestationFixture(t, "amd64")
	attestation, err := ParseAndVerify(fixture.data, fixture.digest, fixture.expected, fixture.publicPEM, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := attestation.SnapshotAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	expectedSHA, err := fixture.expected.SHA256Hex(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]string, len(ca42capsulev2.FieldNames()))
	for index, name := range ca42capsulev2.FieldNames() {
		switch name {
		case "format":
			values[index] = ca42capsulev2.Format
		case "attestation_format":
			values[index] = snapshot.Value("format")
		case "attestation_sha256":
			values[index] = fmt.Sprintf("%x", fixture.digest)
		case "expected_format":
			values[index] = snapshot.Value("expected_format")
		case "expected_sha256":
			values[index] = expectedSHA
		default:
			values[index] = snapshot.Value(name)
		}
		if values[index] == "" {
			t.Fatalf("missing capsule projection: %s", name)
		}
	}
	parseCapsule := func(candidate []string) ca42capsulev2.Capsule {
		data, err := ca42capsulev2.CanonicalBytes(candidate)
		if err != nil {
			t.Fatal(err)
		}
		capsule, err := ca42capsulev2.Parse(data, sha256.Sum256(data))
		if err != nil {
			t.Fatal(err)
		}
		return capsule
	}
	if err := BindCapsule(attestation, parseCapsule(values), fixture.now); err != nil {
		t.Fatal(err)
	}
	changed := append([]string(nil), values...)
	for index, name := range ca42capsulev2.FieldNames() {
		if name == "preflight_runner_sha256" {
			changed[index] = fmt.Sprintf("%x", sha256.Sum256([]byte("changed-preflight")))
		}
	}
	if err := BindCapsule(attestation, parseCapsule(changed), fixture.now); err == nil {
		t.Fatal("capsule semantic split accepted")
	}
}

func TestBindExternalRequiresRawVerifiedManifest(t *testing.T) {
	fixture := newAttestationFixture(t, "amd64")
	attestation, err := ParseAndVerify(fixture.data, fixture.digest, fixture.expected, fixture.publicPEM, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := BindExternal(attestation, []byte("{}\n"), fixture.now); err == nil {
		t.Fatal("unverified external manifest bytes accepted")
	}
}

func TestParseAndVerifyAttestationV3DomainAndOpaqueState(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			fixture := newAttestationFixture(t, architecture)
			attestation, err := ParseAndVerify(fixture.data, fixture.digest, fixture.expected, fixture.publicPEM, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			fixture.data[0] ^= 0xff
			fixture.publicPEM[0] ^= 0xff
			attestation.snapshot.values[9] = "attacker"
			snapshot, err := attestation.SnapshotAt(fixture.now)
			if err != nil || snapshot.Value("release_id") != "release-1" {
				t.Fatalf("trusted attestation state changed: %v", err)
			}
			if _, err := attestation.VerifiedCopyAt(time.Unix(1700003600, 0).UTC()); err == nil {
				t.Fatal("expired attestation v3 accepted")
			}
			attestation.sha256[0] ^= 0xff
			if _, err := attestation.VerifiedCopyAt(fixture.now); err == nil {
				t.Fatal("mutated attestation identity accepted")
			}
		})
	}
}

func TestAttestationV3RejectsWrongSignatureDomainsAndCycleInjection(t *testing.T) {
	fixture := newAttestationFixture(t, "amd64")
	unsigned, err := UnsignedCanonicalBytes(fixture.values)
	if err != nil {
		t.Fatal(err)
	}
	wrongDomains := [][]byte{nil, []byte("PANDORA\x00CA42-ATTESTATION\x00V2\x00"), []byte("pandora\x00CA42-ATTESTATION\x00V3\x00"), []byte("PANDORA\x00CA42-ATTESTATION\x00V3"), append(append([]byte(nil), signatureDomain...), signatureDomain...)}
	for index, domain := range wrongDomains {
		t.Run(fmt.Sprintf("domain_%d", index), func(t *testing.T) {
			message := append(append([]byte(nil), domain...), unsigned...)
			data, err := SignedCanonicalBytes(unsigned, ed25519.Sign(fixture.private, message))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseAndVerify(data, sha256.Sum256(data), fixture.expected, fixture.publicPEM, fixture.now); err == nil {
				t.Fatal("wrong signature domain accepted")
			}
		})
	}
	for _, forbidden := range []string{"release_journal_head_sha256", "release_journal_snapshot_sha256", "execution_plan_sha256", "release_manifest_sha256", "release_contract_core_sha256"} {
		t.Run(forbidden, func(t *testing.T) {
			injected := append([]byte(nil), unsigned...)
			injected = append(injected, []byte(forbidden+"="+strings.Repeat("f", 64)+"\n")...)
			message := append(append([]byte(nil), signatureDomain...), injected...)
			data, err := SignedCanonicalBytes(injected, ed25519.Sign(fixture.private, message))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseAndVerify(data, sha256.Sum256(data), fixture.expected, fixture.publicPEM, fixture.now); err == nil {
				t.Fatal("forbidden reverse pin accepted")
			}
		})
	}
}

func newAttestationFixture(t *testing.T, architecture string) attestationFixture {
	t.Helper()
	now := time.Unix(1700000100, 0).UTC()
	seed := sha256.Sum256([]byte("attestation-v3-fixture"))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	publicSHA := sha256.Sum256(publicPEM)
	expectedValues := expectedValues(t, architecture, hex.EncodeToString(publicSHA[:]))
	expectedBytes, err := ca42expectedv2.CanonicalBytes(expectedValues)
	if err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256(expectedBytes)
	expected, err := ca42expectedv2.Parse(expectedBytes, expectedDigest, architecture, now)
	if err != nil {
		t.Fatal(err)
	}
	expectedSnapshot, err := expected.SnapshotAt(now)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]string, len(fieldNames)-1)
	for index, name := range fieldNames[:len(fieldNames)-1] {
		switch name {
		case "format":
			values[index] = Format
		case "signature_algorithm":
			values[index] = SignatureAlgorithm
		case "profile_id":
			values[index] = ca42protocolv2.ProfileID
		case "profile_sha256":
			profileSHA, e := ca42protocolv2.Strict().SHA256()
			if e != nil {
				t.Fatal(e)
			}
			values[index] = fmt.Sprintf("%x", profileSHA)
		case "expected_format":
			values[index] = ca42expectedv2.Format
		case "expected_sha256":
			values[index] = hex.EncodeToString(expectedDigest[:])
		case "nonce":
			values[index] = fmt.Sprintf("%x", sha256.Sum256([]byte("attestation-v3-nonce")))
		case "issued_at":
			values[index] = "1700000000"
		case "expires_at":
			values[index] = "1700003600"
		default:
			values[index] = expectedSnapshot.ValueName(name)
		}
		if values[index] == "" {
			t.Fatalf("missing attestation fixture field: %s", name)
		}
	}
	unsigned, err := UnsignedCanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	message, err := SignatureMessage(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	data, err := SignedCanonicalBytes(unsigned, ed25519.Sign(private, message))
	if err != nil {
		t.Fatal(err)
	}
	return attestationFixture{now: now, private: private, publicPEM: publicPEM, expected: expected, values: values, data: data, digest: sha256.Sum256(data)}
}

func expectedValues(t *testing.T, architecture, publicKeySHA string) []string {
	t.Helper()
	names := ca42expectedv2.FieldNames()
	values := make([]string, len(names))
	set := func(name, value string) {
		for index, candidate := range names {
			if candidate == name {
				values[index] = value
				return
			}
		}
		t.Fatalf("unknown expected field: %s", name)
	}
	for index, name := range names {
		values[index] = "fixture"
		if strings.HasSuffix(name, "sha256") {
			values[index] = fmt.Sprintf("%x", sha256.Sum256([]byte("attestation-v3-expected:"+name)))
		}
	}
	profileSHA, err := ca42protocolv2.Strict().SHA256()
	if err != nil {
		t.Fatal(err)
	}
	set("format", ca42expectedv2.Format)
	set("profile_id", ca42protocolv2.ProfileID)
	set("profile_sha256", fmt.Sprintf("%x", profileSHA))
	set("attestation_format", Format)
	set("attestation_signature_algorithm", SignatureAlgorithm)
	set("release_id", "release-1")
	set("release_run_id", "run-1")
	set("attempt_id", "attempt-1")
	set("architecture", architecture)
	set("transition", ca42executionv2.Transition)
	set("attestation_public_key_sha256", publicKeySHA)
	set("pathtrust_device", "10")
	set("pathtrust_mode", ca42executionv2.RequiredAttemptExecutableMode)
	set("attestation_core_device", "10")
	set("attestation_core_mode", ca42executionv2.RequiredAttemptExecutableMode)
	set("bash_device", "20")
	set("bash_mode", ca42executionv2.RequiredSystemExecutableMode)
	set("docker_client_device", "20")
	set("docker_client_mode", ca42executionv2.RequiredSystemExecutableMode)
	set("goose_version", ca42executionv2.RequiredGooseVersion)
	set("client_auth_00042_sha256", ca42manifest.FrozenMigrationSHA256)
	set("artifact_storage_profile", ca42executionv2.RequiredStorageProfile)
	set("globals_dump_size_bytes", "1024")
	set("database_dump_size_bytes", "1073741824")
	set("postgres_image_sha256", strings.Repeat("c", 64))
	set("source_container_id", strings.Repeat("a", 64))
	set("source_system_identifier", "1111111111111111111")
	set("source_database_name", "aegis")
	set("source_database_oid", "16384")
	set("source_database_owner_oid", "10")
	set("source_database_owner_name", "aegis_owner")
	set("source_goose_waterline", "41")
	set("isolated_container_id", strings.Repeat("b", 64))
	set("isolated_system_identifier", "2222222222222222222")
	set("isolated_network_id", strings.Repeat("d", 64))
	set("isolated_database_name", "aegis")
	set("isolated_database_oid", "24576")
	set("isolated_image_id", "sha256:"+strings.Repeat("c", 64))
	set("isolated_run_id", "pandoraisolatedpg18ABC123-1700000000")
	set("ledger_namespace", ca42protocolv2.LedgerNamespace)
	set("ledger_directory_sha256", ca42executionv2.LedgerDirectorySHA256)
	set("not_before_epoch", "1700000000")
	set("not_after_epoch", "1700003600")
	return values
}
