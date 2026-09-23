package ca42artifactsv2

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42credential"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42gooseinfo"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42runtimeclosure"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

func TestNewAcceptsCompleteGraphOnSupportedArchitecturesAndOwnsExternalBytes(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			fixture := newGraphFixture(t, architecture, "a")
			callerBytes := append([]byte(nil), fixture.external...)
			input := fixture.inputs
			input.ExternalManifest = callerBytes
			set, err := New(input, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := set.SnapshotAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Format != Format || snapshot.Architecture != architecture ||
				snapshot.AttemptID != "attempt-a" || len(snapshot.BindingSHA256) != 64 ||
				snapshot.PlanNotBefore.Unix() != 1700000000 || snapshot.PlanNotAfter.Unix() != 1700003600 ||
				snapshot.AttestationNotBefore.Unix() != 1700000050 || snapshot.AttestationNotAfter.Unix() != 1700003500 ||
				snapshot.CredentialNotBefore.Unix() != 1700000100 || snapshot.CredentialNotAfter.Unix() != 1700003400 ||
				snapshot.EffectiveNotBefore != snapshot.CredentialNotBefore || snapshot.EffectiveNotAfter != snapshot.CredentialNotAfter {
				t.Fatalf("aggregate snapshot invalid: %+v", snapshot)
			}
			callerBytes[0] ^= 0xff
			input.StorageDescriptor.Entries[0].Path = "/attacker-controlled"
			if _, err := set.VerifiedCopyAt(fixture.now); err != nil {
				t.Fatalf("caller-owned input mutation changed aggregate: %v", err)
			}
			first, err := set.ExternalManifestBytesAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			first[0] ^= 0xff
			second, err := set.ExternalManifestBytesAt(fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(first, second) {
				t.Fatal("external manifest accessor returned aliased storage")
			}
		})
	}
}

func TestSetDerivesJournalBindingOnlyFromReverifiedGraph(t *testing.T) {
	fixture := newGraphFixture(t, "amd64", "journal")
	set, err := New(fixture.inputs, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := set.JournalBindingAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	values, err := binding.ValuesAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := set.SnapshotAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	wantCore, err := ca42releasev3.ContractCoreSHA256Hex(fixture.inputs.Release, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	releaseSnapshot, err := fixture.inputs.Release.SnapshotAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if values.AttemptID != snapshot.AttemptID || values.ReleaseContractCoreSHA256 != wantCore ||
		values.ReleaseJournalHeadSHA256 != releaseSnapshot.Value(ca42releasev3.FieldReleaseJournalHeadSHA256) ||
		values.ReleaseJournalSnapshotSHA256 != releaseSnapshot.Value(ca42releasev3.FieldReleaseJournalSnapshotSHA256) ||
		values.ArtifactSetBindingSHA256 != snapshot.BindingSHA256 {
		t.Fatalf("journal binding mismatch: %+v", values)
	}
	copyBinding := binding
	if _, err := copyBinding.ValuesAt(fixture.now.Add(4 * time.Hour)); err == nil {
		t.Fatal("expired set produced a journal binding")
	}
	copyBinding.values.AttemptID = "attempt-forged"
	if _, err := copyBinding.ValuesAt(fixture.now); err == nil {
		t.Fatal("mutated opaque journal binding remained valid")
	}
	if _, err := (JournalBinding{}).ValuesAt(fixture.now); err == nil {
		t.Fatal("zero journal binding was accepted")
	}
}

func TestNewRejectsIndependentlyValidMixAndMatch(t *testing.T) {
	a := newGraphFixture(t, "amd64", "a")
	b := newGraphFixture(t, "amd64", "b")
	arm := newGraphFixture(t, "arm64", "a")
	if _, err := New(a.inputs, a.now); err != nil {
		t.Fatalf("set A invalid: %v", err)
	}
	if _, err := New(b.inputs, b.now); err != nil {
		t.Fatalf("set B invalid: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Inputs)
	}{
		{"release", func(in *Inputs) { in.Release = b.inputs.Release }},
		{"plan", func(in *Inputs) { in.Plan = b.inputs.Plan }},
		{"capsule", func(in *Inputs) { in.Capsule = b.inputs.Capsule }},
		{"expected", func(in *Inputs) { in.Expected = b.inputs.Expected }},
		{"attestation", func(in *Inputs) { in.Attestation = b.inputs.Attestation }},
		{"external", func(in *Inputs) { in.ExternalManifest = b.inputs.ExternalManifest }},
		{"credential", func(in *Inputs) { in.CredentialDescriptor = b.inputs.CredentialDescriptor }},
		{"runtime_closure", func(in *Inputs) { in.RuntimeClosure = b.inputs.RuntimeClosure }},
		{"goose_build_info", func(in *Inputs) { in.GooseBuildInfo = b.inputs.GooseBuildInfo }},
		{"storage_descriptor", func(in *Inputs) { in.StorageDescriptor = b.inputs.StorageDescriptor }},
		{"runtime_and_goose", func(in *Inputs) {
			in.RuntimeClosure, in.GooseBuildInfo = b.inputs.RuntimeClosure, b.inputs.GooseBuildInfo
		}},
		{"all_sidecars", func(in *Inputs) {
			in.CredentialDescriptor, in.RuntimeClosure = b.inputs.CredentialDescriptor, b.inputs.RuntimeClosure
			in.GooseBuildInfo, in.StorageDescriptor = b.inputs.GooseBuildInfo, b.inputs.StorageDescriptor
		}},
		{"cross_architecture_sidecars", func(in *Inputs) {
			in.CredentialDescriptor, in.RuntimeClosure = arm.inputs.CredentialDescriptor, arm.inputs.RuntimeClosure
			in.GooseBuildInfo, in.StorageDescriptor = arm.inputs.GooseBuildInfo, arm.inputs.StorageDescriptor
		}},
		{"release_and_plan", func(in *Inputs) { in.Release, in.Plan = b.inputs.Release, b.inputs.Plan }},
		{"attestation_subgraph", func(in *Inputs) {
			in.Capsule, in.Expected, in.Attestation = b.inputs.Capsule, b.inputs.Expected, b.inputs.Attestation
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := a.inputs
			test.mutate(&candidate)
			if _, err := New(candidate, a.now); err == nil {
				t.Fatal("independently valid mixed artifact set accepted")
			}
		})
	}
}

func TestAggregateRechecksTimeAndCanonicalPlanIdentity(t *testing.T) {
	fixture := newGraphFixture(t, "amd64", "a")
	set, err := New(fixture.inputs, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.VerifiedCopyAt(time.Unix(1700000099, 0).UTC()); err == nil {
		t.Fatal("aggregate survived credential not-before boundary")
	}
	if _, err := set.VerifiedCopyAt(time.Unix(1700003400, 0).UTC()); err == nil {
		t.Fatal("aggregate survived credential expires-at boundary")
	}
	polluted := fixture.inputs
	polluted.Plan.ReleaseID = "attacker"
	polluted.Plan.AttestationSHA256 = strings.Repeat("f", 64)
	polluted.Plan.ExpectedSHA256 = strings.Repeat("e", 64)
	polluted.Plan.NotAfter = time.Unix(1, 0).UTC()
	if _, err := New(polluted, fixture.now); err != nil {
		t.Fatalf("public Plan projection pollution changed canonical capability: %v", err)
	}
	polluted.Plan.SHA256[0] ^= 0xff
	if _, err := New(polluted, fixture.now); err == nil {
		t.Fatal("mutated Plan identity accepted")
	}
}

func TestBindingInventoryIsExactAndAllBindingsRun(t *testing.T) {
	want := []string{
		"release_to_plan", "capsule_to_plan", "expected_to_plan", "expected_to_release",
		"attestation_to_plan", "attestation_to_release", "attestation_to_capsule", "attestation_to_external",
		"credential_to_plan", "runtime_closure_to_plan", "goose_build_info_to_plan", "storage_descriptor_to_plan",
	}
	if !reflect.DeepEqual(requiredBindingNames[:], want) {
		t.Fatalf("binding inventory drift: got=%q want=%q", requiredBindingNames, want)
	}
	called := make([]string, 0, len(want))
	bindings := make([]binding, len(want))
	for index, name := range want {
		name := name
		bindings[index] = binding{name: name, run: func() error {
			called = append(called, name)
			return nil
		}}
	}
	if err := runBindings(bindings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(called, want) {
		t.Fatalf("not every binding ran: got=%q want=%q", called, want)
	}
}

func TestBindingInventoryFailsClosedOnOrderCountAndFirstError(t *testing.T) {
	if err := runBindings(nil); err == nil {
		t.Fatal("empty binding inventory accepted")
	}
	wrong := make([]binding, len(requiredBindingNames))
	for index, name := range requiredBindingNames {
		wrong[index] = binding{name: name, run: func() error { return nil }}
	}
	wrong[0], wrong[1] = wrong[1], wrong[0]
	if err := runBindings(wrong); err == nil {
		t.Fatal("reordered binding inventory accepted")
	}

	for failAt, failName := range requiredBindingNames {
		called := 0
		failing := make([]binding, len(requiredBindingNames))
		for index, name := range requiredBindingNames {
			index := index
			failing[index] = binding{name: name, run: func() error {
				called++
				if index == failAt {
					return errors.New("sentinel")
				}
				return nil
			}}
		}
		err := runBindings(failing)
		if err == nil || !strings.Contains(err.Error(), failName) || called != failAt+1 {
			t.Fatalf("binding %s failure was not fail-closed: called=%d err=%v", failName, called, err)
		}
	}
}

func TestZeroAndMalformedAggregateCapabilitiesAreRejected(t *testing.T) {
	now := time.Unix(1700000100, 0).UTC()
	if _, err := New(Inputs{}, now); err == nil {
		t.Fatal("zero artifact set input accepted")
	}
	if _, err := New(Inputs{ExternalManifest: []byte("{}\n")}, now); err == nil {
		t.Fatal("unparsed artifact capabilities accepted")
	}
	if _, err := (Set{}).VerifiedCopyAt(now); err == nil {
		t.Fatal("zero aggregate capability accepted")
	}
	if _, err := (Set{}).SnapshotAt(now); err == nil {
		t.Fatal("zero aggregate snapshot accepted")
	}
	if _, err := (Set{}).ExternalManifestBytesAt(now); err == nil {
		t.Fatal("zero aggregate external bytes accepted")
	}
	if _, err := (Set{}).CredentialKeyIDAt(now); err == nil {
		t.Fatal("zero aggregate credential routing accepted")
	}
	if err := (Set{}).VerifyCredentialAt(nil, nil, now); err == nil {
		t.Fatal("nil credential key accepted")
	}
	if _, err := (Set{}).RetainInventoryAt(nil, nil, now); err == nil {
		t.Fatal("nil inventory context accepted")
	}
}

func TestEveryMandatorySidecarFailsClosedWhenMissingOrIdentityMutated(t *testing.T) {
	fixture := newGraphFixture(t, "amd64", "a")
	tests := []struct {
		name   string
		mutate func(*Inputs)
	}{
		{"credential_missing", func(in *Inputs) { in.CredentialDescriptor = ca42credential.Descriptor{} }},
		{"runtime_missing", func(in *Inputs) { in.RuntimeClosure = ca42runtimeclosure.Manifest{} }},
		{"goose_missing", func(in *Inputs) { in.GooseBuildInfo = ca42gooseinfo.BuildInfo{} }},
		{"storage_missing", func(in *Inputs) { in.StorageDescriptor = ca42storage.Descriptor{} }},
		{"credential_identity", func(in *Inputs) { in.CredentialDescriptor.SHA256[0] ^= 0xff }},
		{"runtime_identity", func(in *Inputs) { in.RuntimeClosure.SHA256[0] ^= 0xff }},
		{"goose_identity", func(in *Inputs) { in.GooseBuildInfo.SHA256[0] ^= 0xff }},
		{"storage_identity", func(in *Inputs) { in.StorageDescriptor.SHA256[0] ^= 0xff }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := fixture.inputs
			test.mutate(&candidate)
			if _, err := New(candidate, fixture.now); err == nil {
				t.Fatal("missing or identity-mutated mandatory sidecar accepted")
			}
		})
	}
}

func TestValidityIntersectionMatrix(t *testing.T) {
	at := func(epoch int64) time.Time { return time.Unix(epoch, 0).UTC() }
	tests := []struct {
		name                               string
		planNB, planNA, attestNB, attestNA int64
		credentialNB, credentialNA         int64
		wantNB, wantNA                     int64
		wantErr                            bool
	}{
		{"plan_dominates", 20, 80, 10, 90, 5, 100, 20, 80, false},
		{"attestation_dominates", 10, 100, 20, 80, 5, 90, 20, 80, false},
		{"credential_dominates", 10, 100, 5, 90, 20, 80, 20, 80, false},
		{"mixed_bounds", 10, 80, 20, 100, 15, 70, 20, 70, false},
		{"touching_is_empty", 10, 20, 20, 30, 5, 40, 0, 0, true},
		{"disjoint", 10, 20, 30, 40, 50, 60, 0, 0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotNB, gotNA, err := intersectValidity(
				at(test.planNB), at(test.planNA), at(test.attestNB), at(test.attestNA),
				at(test.credentialNB), at(test.credentialNA),
			)
			if test.wantErr {
				if err == nil {
					t.Fatal("empty validity intersection accepted")
				}
				return
			}
			if err != nil || gotNB != at(test.wantNB) || gotNA != at(test.wantNA) {
				t.Fatalf("intersection got=(%v,%v,%v) want=(%v,%v,nil)", gotNB, gotNA, err, at(test.wantNB), at(test.wantNA))
			}
			if _, _, err := intersectValidity(
				at(test.planNB-1), at(test.planNA), at(test.attestNB), at(test.attestNA),
				at(test.credentialNB), at(test.credentialNA),
			); err != nil {
				t.Fatalf("backward trusted clock changed static interval intersection: %v", err)
			}
		})
	}
}

func TestBindingSHAIsDomainSeparatedAndCoversEverySnapshotField(t *testing.T) {
	base := Snapshot{
		Format: Format, ProfileID: "profile", ProfileSHA256: strings.Repeat("1", 64),
		ReleaseID: "release", ReleaseRunID: "run", AttemptID: "attempt", Architecture: "amd64",
		ReleaseManifestSHA256: strings.Repeat("2", 64), ExecutionPlanSHA256: strings.Repeat("3", 64),
		TrustCapsuleSHA256: strings.Repeat("4", 64), ExpectedSHA256: strings.Repeat("5", 64),
		AttestationSHA256: strings.Repeat("6", 64), ExternalManifestSHA256: strings.Repeat("7", 64),
		CredentialSourceDescriptorSHA256: strings.Repeat("9", 64), RuntimeClosureManifestSHA256: strings.Repeat("b", 64),
		GooseBuildInfoSHA256: strings.Repeat("c", 64), ArtifactStorageDescriptorSHA256: strings.Repeat("d", 64),
		CredentialCommitmentKeyID: "keyring-a",
		AttestationNonce:          strings.Repeat("8", 64),
		PlanNotBefore:             time.Unix(1700000000, 0).UTC(), PlanNotAfter: time.Unix(1700003600, 0).UTC(),
		AttestationNotBefore: time.Unix(1700000050, 0).UTC(), AttestationNotAfter: time.Unix(1700003500, 0).UTC(),
		CredentialNotBefore: time.Unix(1700000100, 0).UTC(), CredentialNotAfter: time.Unix(1700003400, 0).UTC(),
		EffectiveNotBefore: time.Unix(1700000100, 0).UTC(), EffectiveNotAfter: time.Unix(1700003400, 0).UTC(),
	}
	want := bindingSHA256(base)
	mutations := []func(*Snapshot){
		func(s *Snapshot) { s.Format += "x" }, func(s *Snapshot) { s.ProfileID += "x" },
		func(s *Snapshot) { s.ProfileSHA256 = strings.Repeat("a", 64) }, func(s *Snapshot) { s.ReleaseID += "x" },
		func(s *Snapshot) { s.ReleaseRunID += "x" }, func(s *Snapshot) { s.AttemptID += "x" },
		func(s *Snapshot) { s.Architecture = "arm64" }, func(s *Snapshot) { s.ReleaseManifestSHA256 = strings.Repeat("a", 64) },
		func(s *Snapshot) { s.ExecutionPlanSHA256 = strings.Repeat("a", 64) }, func(s *Snapshot) { s.TrustCapsuleSHA256 = strings.Repeat("a", 64) },
		func(s *Snapshot) { s.ExpectedSHA256 = strings.Repeat("a", 64) }, func(s *Snapshot) { s.AttestationSHA256 = strings.Repeat("a", 64) },
		func(s *Snapshot) { s.ExternalManifestSHA256 = strings.Repeat("a", 64) }, func(s *Snapshot) { s.AttestationNonce = strings.Repeat("a", 64) },
		func(s *Snapshot) { s.CredentialSourceDescriptorSHA256 = strings.Repeat("a", 64) },
		func(s *Snapshot) { s.RuntimeClosureManifestSHA256 = strings.Repeat("a", 64) },
		func(s *Snapshot) { s.GooseBuildInfoSHA256 = strings.Repeat("a", 64) },
		func(s *Snapshot) { s.ArtifactStorageDescriptorSHA256 = strings.Repeat("a", 64) },
		func(s *Snapshot) { s.CredentialCommitmentKeyID += "x" },
		func(s *Snapshot) { s.PlanNotBefore = s.PlanNotBefore.Add(time.Second) }, func(s *Snapshot) { s.PlanNotAfter = s.PlanNotAfter.Add(time.Second) },
		func(s *Snapshot) { s.AttestationNotBefore = s.AttestationNotBefore.Add(time.Second) }, func(s *Snapshot) { s.AttestationNotAfter = s.AttestationNotAfter.Add(time.Second) },
		func(s *Snapshot) { s.CredentialNotBefore = s.CredentialNotBefore.Add(time.Second) }, func(s *Snapshot) { s.CredentialNotAfter = s.CredentialNotAfter.Add(time.Second) },
		func(s *Snapshot) { s.EffectiveNotBefore = s.EffectiveNotBefore.Add(time.Second) }, func(s *Snapshot) { s.EffectiveNotAfter = s.EffectiveNotAfter.Add(time.Second) },
	}
	for index, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		if got := bindingSHA256(candidate); got == want {
			t.Fatalf("snapshot field mutation %d did not change binding SHA", index)
		}
	}
}
