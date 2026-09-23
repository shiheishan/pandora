//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
)

func TestV3LayoutSwitchedBindsOpaqueArtifactValues(t *testing.T) {
	_, boundary := v3RecordsTo(t, V3LayoutSwitched)
	values := ca42artifactsv2.JournalBindingValues{AttemptID: boundary.AttemptID(),
		ReleaseContractCoreSHA256: boundary.ReleaseContractCoreSHA256(), ReleaseJournalHeadSHA256: boundary.HeadSHA256(),
		ReleaseJournalSnapshotSHA256: boundary.ManifestSHA256(), ArtifactSetBindingSHA256: v3H("artifact-set")}
	if err := bindV3LayoutSwitchedValues(boundary, values); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*ca42artifactsv2.JournalBindingValues){
		func(v *ca42artifactsv2.JournalBindingValues) { v.AttemptID = "other-attempt" },
		func(v *ca42artifactsv2.JournalBindingValues) { v.ReleaseContractCoreSHA256 = v3H("other-core") },
		func(v *ca42artifactsv2.JournalBindingValues) { v.ReleaseJournalHeadSHA256 = v3H("other-head") },
		func(v *ca42artifactsv2.JournalBindingValues) { v.ReleaseJournalSnapshotSHA256 = v3H("other-manifest") },
		func(v *ca42artifactsv2.JournalBindingValues) { v.ArtifactSetBindingSHA256 = "" },
	}
	for index, mutate := range mutations {
		candidate := values
		mutate(&candidate)
		if err := bindV3LayoutSwitchedValues(boundary, candidate); err == nil {
			t.Fatalf("mutation %d was accepted", index)
		}
	}
}

func TestV3LayoutSwitchedRejectsZeroOpaqueArtifactCapabilityWithoutMutation(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, snapshot, _ := writeV3JournalFixture(t, policy, V3LayoutSwitched)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, snapshot.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	before, err := session.InspectLayoutSwitched()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.InspectLayoutSwitchedFor(ca42artifactsv2.JournalBinding{}, time.Unix(1700000100, 0).UTC()); err == nil {
		t.Fatal("zero artifact capability was accepted")
	}
	after, err := session.InspectLayoutSwitched()
	if err != nil || after.HeadSHA256() != before.HeadSHA256() || after.ManifestSHA256() != before.ManifestSHA256() {
		t.Fatalf("failed artifact bind mutated journal: after=%+v err=%v", after, err)
	}
}
