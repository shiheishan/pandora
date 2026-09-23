package ca42runner

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsule"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
)

type attestationFixture struct {
	values   []string
	expected []string
	private  ed25519.PrivateKey
	public   []byte
	plan     ca42execution.Plan
	capsule  ca42capsule.Capsule
	manifest ca42manifest.Receipt
	now      time.Time
}

func TestVerifyRetainedAttestationV2(t *testing.T) {
	fixture := newAttestationFixture(t)
	attestation, expected := fixture.bytes(t)
	window, err := verifyRetainedAttestationV2(attestation, expected, fixture.public, fixture.plan, fixture.capsule, fixture.manifest, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if !window.notBefore.Equal(time.Unix(1700000000, 0).UTC()) || !window.notAfter.Equal(time.Unix(1700003600, 0).UTC()) {
		t.Fatalf("unexpected validity window: %+v", window)
	}
}

func TestAttestationValidityBoundaries(t *testing.T) {
	window := attestationValidity{notBefore: time.Unix(100, 0).UTC(), notAfter: time.Unix(200, 0).UTC()}
	if err := window.validateAt(time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("inclusive not-before rejected: %v", err)
	}
	for _, candidate := range []time.Time{time.Unix(99, 0).UTC(), time.Unix(200, 0).UTC(), time.Time{}} {
		if err := window.validateAt(candidate); err == nil {
			t.Fatalf("invalid boundary accepted: %v", candidate)
		}
	}
}

func TestVerifyRetainedAttestationV2RejectsDrift(t *testing.T) {
	tests := map[string]func(*attestationFixture, *[]byte, *[]byte, *[]byte){
		"algorithm_case": func(f *attestationFixture, _, _, _ *[]byte) { f.values[1] = "Ed25519" },
		"zero_nonce":     func(f *attestationFixture, _, _, _ *[]byte) { f.values[2] = strings.Repeat("0", 64) },
		"oid_overflow": func(f *attestationFixture, _, _, _ *[]byte) {
			f.values[9] = "4294967296"
			f.syncExpected()
		},
		"catalog_uses_object_hash": func(f *attestationFixture, _, _, _ *[]byte) {
			f.values[20] = f.manifest.ExactObjectManifestSHA256
			f.syncExpected()
		},
		"window_escapes_plan": func(f *attestationFixture, _, _, _ *[]byte) {
			f.plan.NotBefore = time.Unix(1700000001, 0).UTC()
		},
		"expired":         func(f *attestationFixture, _, _, _ *[]byte) { f.now = time.Unix(1700003600, 0).UTC() },
		"capsule_binding": func(f *attestationFixture, _, _, _ *[]byte) { f.capsule.ReleaseID = "release-drift" },
		"receipt_binding": func(f *attestationFixture, _, _, _ *[]byte) { f.manifest.Authorization = "AUTHORIZED" },
		"expected_binding": func(_ *attestationFixture, _, expected, _ *[]byte) {
			*expected = bytes.Replace(*expected, []byte("release_id=release-1"), []byte("release_id=release-2"), 1)
		},
		"signature": func(_ *attestationFixture, attestation, _, _ *[]byte) {
			lines := bytes.Split((*attestation)[:len(*attestation)-1], []byte{'\n'})
			signature, _ := base64.StdEncoding.DecodeString(string(bytes.TrimPrefix(lines[len(lines)-1], []byte("signature_b64="))))
			signature[0] ^= 1
			lines[len(lines)-1] = []byte("signature_b64=" + base64.StdEncoding.EncodeToString(signature))
			*attestation = append(bytes.Join(lines, []byte{'\n'}), '\n')
		},
		"crlf": func(_ *attestationFixture, attestation, _, _ *[]byte) {
			*attestation = bytes.ReplaceAll(*attestation, []byte{'\n'}, []byte{'\r', '\n'})
		},
		"pem_trailing": func(_ *attestationFixture, _, _, publicKey *[]byte) { *publicKey = append(*publicKey, '\n') },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newAttestationFixture(t)
			attestation, expected := fixture.bytes(t)
			publicKey := append([]byte(nil), fixture.public...)
			mutate(fixture, &attestation, &expected, &publicKey)
			if name != "expected_binding" && name != "signature" && name != "crlf" && name != "pem_trailing" {
				attestation, expected = fixture.bytes(t)
			}
			if _, err := verifyRetainedAttestationV2(attestation, expected, publicKey, fixture.plan, fixture.capsule, fixture.manifest, fixture.now); err == nil {
				t.Fatal("attestation drift accepted")
			}
		})
	}
}

func newAttestationFixture(t *testing.T) *attestationFixture {
	t.Helper()
	h := func(value byte) string { return strings.Repeat(fmt.Sprintf("%x", value), 64) }
	values := []string{
		attestationV2Format, attestationV2Algorithm, h(1), "1700000000", "1700003600", "release-1",
		h(2), "123456789", "pandora", "100", "101", "pandora_owner", "41",
		h(3), h(4), h(5), h(6), h(7), h(8), h(9), h(10),
	}
	seed := bytes.Repeat([]byte{42}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	der, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	publicKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	plan := ca42execution.Plan{
		ReleaseID: values[5], ReleaseRunID: "run-1", SourceContainerID: values[6], SourceSystemIdentifier: values[7],
		SourceDatabase: values[8], SourceDatabaseOID: values[9], SourceDatabaseOwnerOID: values[10], SourceDatabaseOwner: values[11],
		MigrationSetSHA256: values[13], ClientAuth00042SHA256: values[14], GooseBinarySHA256: values[15],
		PreflightRunnerSHA256: values[16], GlobalsDumpSHA256: values[17], DatabaseDumpSHA256: values[18],
		PostgresImageSHA256: values[19], ExternalManifestSHA256: values[20],
		IsolatedSystemIdentifier: "987654321", IsolatedContainerID: h(11), IsolatedNetworkID: h(12),
		IsolatedDatabase: values[8], IsolatedDatabaseOID: "200", IsolatedImageID: "sha256:" + values[19],
		IsolatedRunID: "pandoraisolatedpg18abc123-1700000000",
		NotBefore:     time.Unix(1699999999, 0).UTC(), NotAfter: time.Unix(1700003601, 0).UTC(),
	}
	capsule := ca42capsule.Capsule{
		ReleaseID: plan.ReleaseID, ReleaseRunID: plan.ReleaseRunID, TargetSystemIdentifier: plan.SourceSystemIdentifier,
		TargetDatabase: plan.SourceDatabase, TargetDatabaseOID: plan.SourceDatabaseOID,
		AttestationSHA256: plan.AttestationSHA256, ExpectedSHA256: plan.ExpectedSHA256,
		PublicKeySHA256: plan.AttestationPublicKeySHA256, ExternalManifestSHA256: plan.ExternalManifestSHA256,
	}
	manifest := ca42manifest.Receipt{
		ReceiptFormat: "client-auth-00042-external-manifest-structural-receipt-v1",
		Decision:      "STRUCTURALLY_VALID", Authorization: "NONE", ContractSHA256: ca42manifest.ContractSHA256,
		ExternalManifestSHA256: plan.ExternalManifestSHA256, ExactObjectManifestSHA256: h(13),
		IsolatedSystemIdentifier: plan.IsolatedSystemIdentifier, IsolatedContainerID: plan.IsolatedContainerID,
		IsolatedNetworkID: plan.IsolatedNetworkID, IsolatedDatabase: plan.IsolatedDatabase,
		IsolatedDatabaseOID: plan.IsolatedDatabaseOID, IsolatedImageID: plan.IsolatedImageID, IsolatedRunID: plan.IsolatedRunID,
	}
	fixture := &attestationFixture{
		values: values, private: privateKey, public: publicKey, plan: plan, capsule: capsule, manifest: manifest,
		now: time.Unix(1700000000, 0).UTC(),
	}
	fixture.syncExpected()
	return fixture
}

func (f *attestationFixture) syncExpected() {
	f.expected = append([]string(nil), f.values[5:]...)
}

func (f *attestationFixture) bytes(t *testing.T) ([]byte, []byte) {
	t.Helper()
	var payload strings.Builder
	for index, name := range attestationPayloadFieldNames {
		fmt.Fprintf(&payload, "%s=%s\n", name, f.values[index])
	}
	signature := ed25519.Sign(f.private, []byte(payload.String()))
	attestation := []byte(payload.String() + "signature_b64=" + base64.StdEncoding.EncodeToString(signature) + "\n")
	var expected strings.Builder
	for index, name := range attestationExpectedFieldNames {
		fmt.Fprintf(&expected, "%s=%s\n", name, f.expected[index])
	}
	return attestation, []byte(expected.String())
}
