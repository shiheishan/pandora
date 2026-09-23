package ca42runner

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42release"
	"github.com/aegispanel/aegis/internal/platform/releasejournal"
)

func TestBindLayoutSwitchedJournalSinglePassAndRejectsMix(t *testing.T) {
	bundle := journalBundleFixture()
	coreBefore, err := ca42release.ContractCoreSHA256Hex(bundle.Release)
	if err != nil {
		t.Fatal(err)
	}
	segments := journalV2Segments(t, bundle, coreBefore, releasejournal.LayoutSwitched)
	receipt, err := releasejournal.ParseReceipt(segments)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Release.ReleaseJournalHeadSHA256 = receipt.LayoutSwitchedHeadSHA256()
	bundle.Execution.ReleaseJournalHeadSHA256 = receipt.LayoutSwitchedHeadSHA256()
	bundle.Execution.ReleaseJournalSnapshotSHA256 = receipt.LayoutSwitchedJournalSHA256()
	coreAfter, err := ca42release.ContractCoreSHA256Hex(bundle.Release)
	if err != nil || coreAfter != coreBefore {
		t.Fatalf("journal pins changed stable release core: before=%s after=%s err=%v", coreBefore, coreAfter, err)
	}
	if err := BindLayoutSwitchedJournal(bundle, receipt); err != nil {
		t.Fatal(err)
	}

	driftedRelease := bundle
	driftedRelease.Release.DatabaseDumpSHA256 = strings.Repeat("0", 64)
	if err := BindLayoutSwitchedJournal(driftedRelease, receipt); err == nil {
		t.Fatal("accepted journal bound to a different release contract core")
	}
	driftedPlan := bundle
	driftedPlan.Execution.ReleaseJournalSnapshotSHA256 = strings.Repeat("9", 64)
	if err := BindLayoutSwitchedJournal(driftedPlan, receipt); err == nil {
		t.Fatal("accepted a different layout-switched snapshot pin")
	}

	bundleB := bundle
	bundleB.Release.ReleaseRunID = "run-ca42-b"
	bundleB.Execution.ReleaseRunID = "run-ca42-b"
	coreB, err := ca42release.ContractCoreSHA256Hex(bundleB.Release)
	if err != nil {
		t.Fatal(err)
	}
	receiptB, err := releasejournal.ParseReceipt(journalV2Segments(t, bundleB, coreB, releasejournal.LayoutSwitched))
	if err != nil {
		t.Fatal(err)
	}
	if err := BindLayoutSwitchedJournal(bundle, receiptB); err == nil {
		t.Fatal("accepted independently valid journal B for release A")
	}
}

func TestBindLayoutSwitchedJournalRejectsAlreadyAdvancedCurrentCAS(t *testing.T) {
	bundle := journalBundleFixture()
	core, err := ca42release.ContractCoreSHA256Hex(bundle.Release)
	if err != nil {
		t.Fatal(err)
	}
	boundarySegments := journalV2Segments(t, bundle, core, releasejournal.LayoutSwitched)
	boundary, err := releasejournal.ParseReceipt(boundarySegments)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Release.ReleaseJournalHeadSHA256 = boundary.LayoutSwitchedHeadSHA256()
	bundle.Execution.ReleaseJournalHeadSHA256 = boundary.LayoutSwitchedHeadSHA256()
	bundle.Execution.ReleaseJournalSnapshotSHA256 = boundary.LayoutSwitchedJournalSHA256()
	advanced, err := releasejournal.ParseReceipt(journalV2Segments(t, bundle, core, releasejournal.AdmissionAttempted))
	if err != nil {
		t.Fatal(err)
	}
	if advanced.LayoutSwitchedHeadSHA256() != boundary.LayoutSwitchedHeadSHA256() ||
		advanced.LayoutSwitchedJournalSHA256() != boundary.LayoutSwitchedJournalSHA256() {
		t.Fatal("advanced receipt lost immutable layout-switched prefix")
	}
	if err := BindLayoutSwitchedJournal(bundle, advanced); err == nil {
		t.Fatal("accepted already advanced journal as a fresh admission boundary")
	}
	if err := BindAdmissionJournal(bundle, advanced); err != nil {
		t.Fatalf("rejected exact-retry admission prefix: %v", err)
	}
}

func journalBundleFixture() VerifiedBundle {
	release := ca42release.Manifest{
		AuthorityEpoch: 1, AuthoritySequence: 1, AuthorityBindingSHA256: strings.Repeat("1", 64),
		ReleaseSignerKeyID: "release-signer-1", Architecture: "amd64", ReleaseID: "release-ca42-1",
		ReleaseRunID: "run-ca42-1", AttemptID: "attempt-ca42-1", MigrationRunnerSHA256: strings.Repeat("2", 64),
		ManifestVerifierSHA256: strings.Repeat("3", 64), PreflightRunnerSHA256: strings.Repeat("4", 64),
		GooseBinarySHA256: strings.Repeat("5", 64), MigrationSetSHA256: strings.Repeat("6", 64),
		ClientAuth00042SHA256: ca42manifest.FrozenMigrationSHA256, GlobalsDumpSHA256: strings.Repeat("7", 64),
		DatabaseDumpSHA256: strings.Repeat("8", 64), PostgresImageSHA256: strings.Repeat("c", 64),
		ProductionSourceContainerID: strings.Repeat("a", 64), ProductionSourceSystemIdentifier: "1111111111111111111",
		ProductionSourceDatabase: "aegis", ProductionSourceDatabaseOID: "16384", ProductionSourceDatabaseOwnerOID: "10",
		ProductionSourceDatabaseOwner: "aegis_owner", ProductionSourceGooseWaterline: 41,
		IsolatedTargetContainerID: strings.Repeat("b", 64), IsolatedTargetSystemIdentifier: "2222222222222222222",
		IsolatedTargetNetworkID: strings.Repeat("d", 64), IsolatedTargetDatabase: "aegis", IsolatedTargetDatabaseOID: "24576",
		IsolatedTargetImageID: "sha256:" + strings.Repeat("c", 64), IsolatedTargetRunID: "pandoraisolatedpg18ABC123-1700000000",
		ExternalManifestSHA256: strings.Repeat("e", 64), ExternalObjectManifestSHA256: strings.Repeat("f", 64),
		AttestationPublicKeySHA256: strings.Repeat("9", 64), ReleaseJournalHeadSHA256: strings.Repeat("1", 64),
		ExecutionPlanSHA256: strings.Repeat("2", 64), NotBefore: time.Unix(1700000000, 0).UTC(), NotAfter: time.Unix(1700000300, 0).UTC(),
	}
	plan := ca42execution.Plan{
		ReleaseID: release.ReleaseID, ReleaseRunID: release.ReleaseRunID, AttemptID: release.AttemptID,
		Architecture: release.Architecture, MigrationSetSHA256: release.MigrationSetSHA256,
		SourceSystemIdentifier: release.ProductionSourceSystemIdentifier, SourceDatabase: release.ProductionSourceDatabase,
		SourceDatabaseOID: release.ProductionSourceDatabaseOID, ReleaseJournalHeadSHA256: release.ReleaseJournalHeadSHA256,
		ReleaseJournalSnapshotSHA256: strings.Repeat("3", 64),
	}
	return VerifiedBundle{Release: release, Execution: plan}
}

func journalV2Segments(t *testing.T, bundle VerifiedBundle, core string, terminal releasejournal.State) map[string][]byte {
	t.Helper()
	identity := releasejournal.PreparedIdentity{
		JournalFormat: releasejournal.FormatV2, JournalID: releasejournal.SHA256Bytes([]byte("journal-" + bundle.Release.ReleaseRunID)),
		ReleaseAttemptID: bundle.Release.AttemptID, CreatedAtEpoch: "1700000000", ReleaseID: bundle.Release.ReleaseID,
		ReleaseRunID: bundle.Release.ReleaseRunID, Architecture: bundle.Release.Architecture,
		ReleaseContractCoreFormat: ca42release.ContractCoreFormat, ReleaseContractCoreSHA256: core,
		ControllerContract: releasejournal.ControllerContractV1, ReleaseControllerSHA256: strings.Repeat("3", 64),
		TargetIdentitySHA256: strings.Repeat("4", 64), TargetRootDevice: "2049", TargetRootInode: "4096",
		StagedTreeManifestSHA256: strings.Repeat("5", 64), LiveTreeManifestSHA256: strings.Repeat("6", 64),
		EnvironmentFileSHA256: strings.Repeat("7", 64), BackupControllerSHA256: strings.Repeat("8", 64),
		MachineIdentitySHA256: strings.Repeat("9", 64), BootIDSHA256: strings.Repeat("a", 64),
		PostgresSystemIdentifier: bundle.Release.ProductionSourceSystemIdentifier, DatabaseName: bundle.Release.ProductionSourceDatabase,
		DatabaseOID: bundle.Release.ProductionSourceDatabaseOID, SourceWaterline: "41", AuthorizedTargetWaterline: "42",
		MigrationSetSHA256: bundle.Release.MigrationSetSHA256, IngressUnitSHA256: strings.Repeat("c", 64),
		WriterUnitsSHA256: strings.Repeat("d", 64), HealthConfigSHA256: strings.Repeat("e", 64),
	}
	prepared, _, err := releasejournal.PreparedRecordBytes(identity)
	if err != nil {
		t.Fatal(err)
	}
	segments := map[string][]byte{releasejournal.StateSegments()[releasejournal.Prepared]: prepared}
	snapshot, err := releasejournal.Parse(segments)
	if err != nil {
		t.Fatal(err)
	}
	for snapshot.State != terminal {
		next := releasejournal.NormalTransitions()[snapshot.State]
		if next == "" {
			t.Fatalf("terminal %s is not on normal journal path", terminal)
		}
		record := releasejournal.TransitionRecord{
			JournalFormat: releasejournal.FormatV2, JournalID: identity.JournalID, ReleaseAttemptID: identity.ReleaseAttemptID,
			Sequence: strconv.Itoa(len(snapshot.Names)), EventID: releasejournal.SHA256Bytes([]byte("event-" + string(next))),
			OccurredAtEpoch: strconv.Itoa(1700000000 + len(snapshot.Names)), PreviousState: snapshot.State, State: next,
			PreviousRecordSHA256: snapshot.HeadSHA256, EvidenceSHA256: releasejournal.SHA256Bytes([]byte("evidence-" + string(next))),
			EvidenceSize: "1",
		}
		data, _, err := releasejournal.TransitionRecordBytes(record)
		if err != nil {
			t.Fatal(err)
		}
		segments[releasejournal.StateSegments()[next]] = data
		snapshot, err = releasejournal.Parse(segments)
		if err != nil {
			t.Fatal(err)
		}
	}
	return segments
}
