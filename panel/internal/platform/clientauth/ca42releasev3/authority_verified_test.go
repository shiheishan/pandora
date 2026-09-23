package ca42releasev3

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
)

func TestAuthorityVerifiedManifestBreaksExternalDependencyWithoutGrantingManifest(t *testing.T) {
	fixture := newV3Fixture(t, "amd64")
	verified, err := ParseAuthorityVerified(fixture.signed, fixture.authority, fixture.architecture, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := verified.ExecutionPlanSHA256At(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	planBytes, err := ca42executionPlanBytesForFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if digest != sha256.Sum256(planBytes) {
		t.Fatal("authority-verified plan digest mismatch")
	}
	manifest, err := verified.BindExternal(fixture.external, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.VerifiedCopyAt(fixture.now); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityVerifiedManifestRejectsWrongExternalAndExpiry(t *testing.T) {
	fixture := newV3Fixture(t, "arm64")
	verified, err := ParseAuthorityVerified(fixture.signed, fixture.authority, fixture.architecture, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	wrong := append([]byte(nil), fixture.external...)
	wrong[len(wrong)/2] ^= 1
	if _, err := verified.BindExternal(wrong, fixture.now); err == nil {
		t.Fatal("wrong external manifest completed authority-verified release")
	}
	if _, err := verified.ExecutionPlanSHA256At(time.Unix(1700003600, 0).UTC()); err == nil {
		t.Fatal("expired authority-verified capability exposed a plan digest")
	}
}

func TestAuthorityVerifiedManifestOwnsInputs(t *testing.T) {
	fixture := newV3Fixture(t, "amd64")
	verified, err := ParseAuthorityVerified(fixture.signed, fixture.authority, fixture.architecture, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	fixture.signed[0] ^= 0xff
	fixture.authority.PublicKey[0] ^= 0xff
	if _, err := verified.ExecutionPlanSHA256At(fixture.now); err != nil {
		t.Fatalf("caller mutation changed authority-verified capability: %v", err)
	}
}

func ca42executionPlanBytesForFixture(fixture v3Fixture) ([]byte, error) {
	return ca42executionv2.CanonicalBytes(fixture.planValues)
}
