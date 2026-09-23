package ca42artifactsv2

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestConsumptionIdentityDerivesOnlyFromLiveClaim(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			fixture := newGraphFixture(t, architecture, "identity")
			set, err := New(fixture.inputs, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := set.ConsumptionClaimAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := claim.IdentityAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			values, err := identity.ValuesAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := claim.CanonicalBytesAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			canonicalSHA := sha256.Sum256(canonical)
			claimSHA, _ := claim.SHA256HexAt(fixture.now)
			nonceID, _ := claim.NonceIDAt(fixture.now)
			if values.ClaimFormat != ConsumptionClaimFormat || values.ClaimSHA256 != claimSHA ||
				values.ClaimCanonicalSHA256 != hex.EncodeToString(canonicalSHA[:]) || values.NonceID != nonceID ||
				values.ArtifactSetFormat != Format || values.ArtifactSetBindingSHA256 == "" ||
				values.Architecture != architecture || values.EffectiveNotBefore.IsZero() || values.EffectiveNotAfter.IsZero() {
				t.Fatalf("unexpected identity projection: %+v", values)
			}
		})
	}
}

func TestConsumptionIdentityReverifiesAndFailsClosed(t *testing.T) {
	fixture := newGraphFixture(t, "amd64", "identity")
	set, err := New(fixture.inputs, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := set.ConsumptionClaimAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := claim.IdentityAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (ConsumptionIdentity{}).VerifiedCopyAt(fixture.now); err == nil {
		t.Fatal("zero identity accepted")
	}
	mutated := identity
	mutated.values.NonceID = strings.Repeat("a", 64)
	if _, err := mutated.VerifiedCopyAt(fixture.now); err == nil {
		t.Fatal("mutated identity accepted")
	}
	mutated = identity
	mutated.fingerprint[0] ^= 0xff
	if _, err := mutated.VerifiedCopyAt(fixture.now); err == nil {
		t.Fatal("mutated fingerprint accepted")
	}
	mutated = identity
	mutated.mintedAt = mutated.mintedAt.Add(time.Second)
	if _, err := mutated.VerifiedCopyAt(fixture.now.Add(time.Second)); err == nil {
		t.Fatal("mutated mint time accepted")
	}
	if _, err := identity.VerifiedCopyAt(fixture.now.Add(-time.Second)); err == nil {
		t.Fatal("trusted-clock rollback before mint accepted")
	}
	if _, err := identity.VerifiedCopyAt(time.Unix(1700003400, 0).UTC()); err == nil {
		t.Fatal("expired identity accepted")
	}
	claim.canonical[0] ^= 0xff
	identity.claim = claim
	if _, err := identity.VerifiedCopyAt(fixture.now); err == nil {
		t.Fatal("identity with mutated claim accepted")
	}
}

func TestConsumptionIdentityValuesAreNotRetainedAuthority(t *testing.T) {
	fixture := newGraphFixture(t, "amd64", "identity")
	set, err := New(fixture.inputs, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := set.ConsumptionClaimAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := claim.IdentityAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	values, err := identity.ValuesAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	values.NonceID = strings.Repeat("f", 64)
	again, err := identity.ValuesAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if again.NonceID == values.NonceID {
		t.Fatal("diagnostic value mutation changed opaque identity")
	}
}
