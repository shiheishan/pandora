package ca42artifactsv2

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestConsumptionClaimComesOnlyFromVerifiedSetAndOwnsBytes(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			fixture := newGraphFixture(t, architecture, "a")
			set, err := New(fixture.inputs, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := set.ConsumptionClaimAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			claimSHA, err := claim.SHA256HexAt(fixture.now)
			if err != nil || len(claimSHA) != 64 || claimSHA == strings.Repeat("0", 64) {
				t.Fatalf("claim SHA invalid: %q %v", claimSHA, err)
			}
			nonceID, err := claim.NonceIDAt(fixture.now)
			if err != nil || len(nonceID) != 64 || nonceID == strings.Repeat("0", 64) {
				t.Fatalf("nonce ID invalid: %q %v", nonceID, err)
			}
			first, err := claim.CanonicalBytesAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			first[0] ^= 0xff
			second, err := claim.CanonicalBytesAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(first, second) {
				t.Fatal("claim canonical accessor returned aliased storage")
			}
			for _, field := range []string{
				"credential_source_descriptor_sha256=", "runtime_closure_manifest_sha256=",
				"goose_build_info_sha256=", "artifact_storage_descriptor_sha256=",
			} {
				if !bytes.Contains(second, []byte(field)) {
					t.Fatalf("claim omitted mandatory field %q", field)
				}
			}
		})
	}
}

func TestConsumptionClaimFailsClosedAtExpiryAndOnMutation(t *testing.T) {
	fixture := newGraphFixture(t, "amd64", "a")
	set, err := New(fixture.inputs, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := set.ConsumptionClaimAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claim.VerifiedCopyAt(time.Unix(1700003400, 0).UTC()); err == nil {
		t.Fatal("expired consumption claim accepted")
	}
	claim.canonical[0] ^= 0xff
	if _, err := claim.VerifiedCopyAt(fixture.now); err == nil {
		t.Fatal("mutated consumption claim accepted")
	}
	if _, err := (ConsumptionClaim{}).VerifiedCopyAt(fixture.now); err == nil {
		t.Fatal("zero consumption claim accepted")
	}
}

func TestConsumptionClaimAndNonceDomainsCoverInputs(t *testing.T) {
	fixtureA := newGraphFixture(t, "amd64", "a")
	fixtureB := newGraphFixture(t, "amd64", "b")
	setA, err := New(fixtureA.inputs, fixtureA.now)
	if err != nil {
		t.Fatal(err)
	}
	setB, err := New(fixtureB.inputs, fixtureB.now)
	if err != nil {
		t.Fatal(err)
	}
	claimA, err := setA.ConsumptionClaimAt(fixtureA.now)
	if err != nil {
		t.Fatal(err)
	}
	claimB, err := setB.ConsumptionClaimAt(fixtureB.now)
	if err != nil {
		t.Fatal(err)
	}
	shaA, _ := claimA.SHA256HexAt(fixtureA.now)
	shaB, _ := claimB.SHA256HexAt(fixtureB.now)
	nonceA, _ := claimA.NonceIDAt(fixtureA.now)
	nonceB, _ := claimB.NonceIDAt(fixtureB.now)
	if shaA == shaB || nonceA == nonceB {
		t.Fatal("independent claims share a domain identity")
	}
	if domainSHA256(consumptionClaimDomain, claimA.canonical) == domainSHA256(nonceIDDomain, claimA.canonical) {
		t.Fatal("claim and nonce domains are not separated")
	}
}

func TestConsumptionClaimCanonicalOrderValuesAndNewFieldMutations(t *testing.T) {
	fixture := newGraphFixture(t, "amd64", "a")
	set, err := New(fixture.inputs, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := set.SnapshotAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := consumptionClaimCanonical(consumptionClaimValues(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(canonical), "\n"), "\n")
	if len(lines) != len(consumptionClaimFieldNames) {
		t.Fatalf("canonical field count got=%d want=%d", len(lines), len(consumptionClaimFieldNames))
	}
	values := consumptionClaimValues(snapshot)
	for index, name := range consumptionClaimFieldNames {
		want := name + "=" + values[index]
		if lines[index] != want {
			t.Fatalf("canonical field %d got=%q want=%q", index, lines[index], want)
		}
	}
	baseDigest := domainSHA256(consumptionClaimDomain, canonical)
	mutations := []func(*Snapshot){
		func(s *Snapshot) { s.CredentialSourceDescriptorSHA256 = strings.Repeat("a", 64) },
		func(s *Snapshot) { s.RuntimeClosureManifestSHA256 = strings.Repeat("b", 64) },
		func(s *Snapshot) { s.GooseBuildInfoSHA256 = strings.Repeat("c", 64) },
		func(s *Snapshot) { s.ArtifactStorageDescriptorSHA256 = strings.Repeat("d", 64) },
		func(s *Snapshot) { s.EffectiveNotBefore = s.EffectiveNotBefore.Add(time.Second) },
		func(s *Snapshot) { s.EffectiveNotAfter = s.EffectiveNotAfter.Add(-time.Second) },
	}
	for index, mutate := range mutations {
		candidate := snapshot
		mutate(&candidate)
		candidateCanonical, err := consumptionClaimCanonical(consumptionClaimValues(candidate))
		if err != nil {
			t.Fatal(err)
		}
		if domainSHA256(consumptionClaimDomain, candidateCanonical) == baseDigest {
			t.Fatalf("claim field mutation %d did not change claim identity", index)
		}
	}
}

func TestNonceReplayIdentitySurvivesClaimEnvelopeUpgrade(t *testing.T) {
	if nonceIDDomain != "PANDORA\x00CA42-NONCE-ID\x00V1\x00" {
		t.Fatal("nonce replay identity domain changed across claim envelope versions")
	}
	base := Snapshot{ProfileSHA256: strings.Repeat("1", 64), AttestationNonce: strings.Repeat("2", 64),
		CredentialSourceDescriptorSHA256: strings.Repeat("3", 64)}
	const legacyV1NonceID = "cb3356bee99e442a0c681cfd18b6ff5aad281f5c663138ff723868f16846b020"
	if got := nonceIDForSnapshot(base); got != legacyV1NonceID {
		t.Fatalf("legacy v1 nonce replay identity changed: got=%s want=%s", got, legacyV1NonceID)
	}
	changedSidecar := base
	changedSidecar.CredentialSourceDescriptorSHA256 = strings.Repeat("4", 64)
	if nonceIDForSnapshot(base) != nonceIDForSnapshot(changedSidecar) {
		t.Fatal("sidecar envelope change altered the stable global replay key")
	}
	base.Format, changedSidecar.Format = Format, Format
	base.BindingSHA256, changedSidecar.BindingSHA256 = strings.Repeat("5", 64), strings.Repeat("6", 64)
	base.EffectiveNotBefore, changedSidecar.EffectiveNotBefore = time.Unix(1, 0).UTC(), time.Unix(1, 0).UTC()
	base.EffectiveNotAfter, changedSidecar.EffectiveNotAfter = time.Unix(2, 0).UTC(), time.Unix(2, 0).UTC()
	// The helper rejects empty values, so populate every remaining diagnostic
	// field with a stable token while preserving the one changed sidecar hash.
	for _, snapshot := range []*Snapshot{&base, &changedSidecar} {
		snapshot.ProfileID, snapshot.ReleaseID, snapshot.ReleaseRunID = "p", "r", "run"
		snapshot.AttemptID, snapshot.Architecture = "attempt", "amd64"
		snapshot.ReleaseManifestSHA256, snapshot.ExecutionPlanSHA256 = strings.Repeat("7", 64), strings.Repeat("8", 64)
		snapshot.TrustCapsuleSHA256, snapshot.ExpectedSHA256 = strings.Repeat("9", 64), strings.Repeat("a", 64)
		snapshot.AttestationSHA256, snapshot.ExternalManifestSHA256 = strings.Repeat("b", 64), strings.Repeat("c", 64)
		snapshot.RuntimeClosureManifestSHA256, snapshot.GooseBuildInfoSHA256 = strings.Repeat("d", 64), strings.Repeat("e", 64)
		snapshot.ArtifactStorageDescriptorSHA256 = strings.Repeat("f", 64)
	}
	baseCanonical, err := consumptionClaimCanonical(consumptionClaimValues(base))
	if err != nil {
		t.Fatal(err)
	}
	changedCanonical, err := consumptionClaimCanonical(consumptionClaimValues(changedSidecar))
	if err != nil {
		t.Fatal(err)
	}
	if domainSHA256(consumptionClaimDomain, baseCanonical) == domainSHA256(consumptionClaimDomain, changedCanonical) {
		t.Fatal("sidecar change did not alter claim identity")
	}
}
