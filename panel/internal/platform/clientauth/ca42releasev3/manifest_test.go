package ca42releasev3

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"strings"
	"testing"
	"time"
)

func TestParseAndVerifyV3SignedArchitectures(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			fixture := newV3Fixture(t, architecture)
			snapshot, err := fixture.manifest.SnapshotAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Value(FieldArchitecture) != architecture || snapshot.Value(FieldGooseVersion) != "v3.24.1" ||
				snapshot.Value(FieldTransition) != "goose-41-to-42" {
				t.Fatal("verified manifest projection mismatch")
			}
			if _, err := fixture.manifest.VerifiedCopyAt(time.Unix(1700003600, 0).UTC()); err == nil {
				t.Fatal("expired release manifest v3 accepted")
			}
		})
	}
}

func TestRejectsWrongSignatureDomain(t *testing.T) {
	fixture := newV3Fixture(t, "amd64")
	unsigned, err := UnsignedCanonicalBytes(fixture.values)
	if err != nil {
		t.Fatal(err)
	}
	wrongDomains := [][]byte{
		nil,
		[]byte("PANDORA\x00CA42-RELEASE-MANIFEST\x00V2\x00"),
		[]byte("pandora\x00CA42-RELEASE-MANIFEST\x00V3\x00"),
		[]byte("PANDORA\x00CA42-RELEASE-MANIFEST\x00V3"),
		append(append([]byte(nil), signatureDomain...), signatureDomain...),
	}
	for index, domain := range wrongDomains {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			message := append(append([]byte(nil), domain...), unsigned...)
			signed, err := SignedCanonicalBytes(unsigned, ed25519.Sign(fixture.private, message))
			if err != nil {
				t.Fatal(err)
			}
			authority := fixture.authority
			authority.ManifestSHA256 = sha256.Sum256(signed)
			if _, err := ParseAndVerify(signed, authority, fixture.architecture, fixture.now, fixture.external); err == nil || !strings.Contains(err.Error(), "signature denied") {
				t.Fatalf("wrong signature domain accepted or misclassified: %v", err)
			}
		})
	}
}

func TestRejectsNonCanonicalManifestEnvelope(t *testing.T) {
	fixture := newV3Fixture(t, "amd64")
	tests := map[string]func([]byte) []byte{
		"crlf":        func(value []byte) []byte { return []byte(strings.ReplaceAll(string(value), "\n", "\r\n")) },
		"bom":         func(value []byte) []byte { return append([]byte{0xef, 0xbb, 0xbf}, value...) },
		"nul":         func(value []byte) []byte { return append(value, 0) },
		"no_final_lf": func(value []byte) []byte { return value[:len(value)-1] },
		"extra_lf":    func(value []byte) []byte { return append(value, '\n') },
		"unknown":     func(value []byte) []byte { return append(value, []byte("unknown=x\n")...) },
		"reordered": func(value []byte) []byte {
			lines := bytes.Split(value, []byte{'\n'})
			lines[0], lines[1] = lines[1], lines[0]
			return bytes.Join(lines, []byte{'\n'})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := mutate(append([]byte(nil), fixture.signed...))
			authority := fixture.authority
			authority.ManifestSHA256 = sha256.Sum256(candidate)
			if _, err := ParseAndVerify(candidate, authority, fixture.architecture, fixture.now, fixture.external); err == nil {
				t.Fatal("non-canonical manifest accepted")
			}
		})
	}
}

func TestParsedManifestOwnsCanonicalState(t *testing.T) {
	fixture := newV3Fixture(t, "amd64")
	want, err := fixture.manifest.SHA256Hex(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	fixture.signed[0] ^= 0xff
	fixture.external[0] ^= 0xff
	fixture.authority.PublicKey[0] ^= 0xff
	fixture.manifest.snapshot.values[FieldReleaseID] = "attacker"
	copyBytes, err := fixture.manifest.SnapshotAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if copyBytes.Value(FieldReleaseID) != "release-1" {
		t.Fatal("mutable public projection changed trusted manifest")
	}
	got, err := fixture.manifest.SHA256Hex(fixture.now)
	if err != nil || got != want {
		t.Fatalf("trusted manifest changed through aliased input: %q %v", got, err)
	}
	fixture.manifest.sha256[0] ^= 0xff
	if _, err := fixture.manifest.VerifiedCopyAt(fixture.now); err == nil {
		t.Fatal("mutated manifest identity accepted")
	}
	if _, err := (Manifest{}).VerifiedCopyAt(fixture.now); err == nil {
		t.Fatal("zero manifest capability accepted")
	}
}
