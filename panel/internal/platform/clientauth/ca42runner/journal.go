package ca42runner

import (
	"crypto/subtle"
	"errors"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42release"
	"github.com/aegispanel/aegis/internal/platform/releasejournal"
)

// BindLayoutSwitchedJournal proves the exact v2 admission boundary. It is
// deliberately pure: durable authority-ledger reservation and journal Advance
// happen later while the Store and artifact descriptors remain retained.
func BindLayoutSwitchedJournal(bundle VerifiedBundle, receipt releasejournal.Receipt) error {
	if err := BindAdmissionJournal(bundle, receipt); err != nil {
		return err
	}
	if receipt.JournalFormat() != releasejournal.FormatV2 ||
		receipt.CurrentState() != releasejournal.LayoutSwitched || receipt.CurrentSequence() != 6 {
		return errors.New("CA42 journal layout-switched boundary invalid")
	}
	if !constantHexEqual(receipt.CurrentHeadSHA256(), receipt.LayoutSwitchedHeadSHA256()) ||
		!constantHexEqual(receipt.CurrentJournalSHA256(), receipt.LayoutSwitchedJournalSHA256()) {
		return errors.New("CA42 journal current boundary mismatch")
	}
	return nil
}

// BindAdmissionJournal validates the immutable sequence-6 prefix and accepts
// either that fresh boundary or its sole sequence-7 ADMISSION_ATTEMPTED exact-
// retry successor. It does not reserve authority or authorize execution.
func BindAdmissionJournal(bundle VerifiedBundle, receipt releasejournal.Receipt) error {
	if receipt.JournalFormat() != releasejournal.FormatV2 ||
		!((receipt.CurrentState() == releasejournal.LayoutSwitched && receipt.CurrentSequence() == 6) ||
			(receipt.CurrentState() == releasejournal.AdmissionAttempted && receipt.CurrentSequence() == 7)) {
		return errors.New("CA42 journal admission boundary invalid")
	}
	coreSHA256, err := ca42release.ContractCoreSHA256Hex(bundle.Release)
	if err != nil {
		return err
	}
	if receipt.ReleaseCoreFormat() != ca42release.ContractCoreFormat ||
		!constantHexEqual(receipt.ReleaseCoreSHA256(), coreSHA256) ||
		receipt.ReleaseAttemptID() != bundle.Release.AttemptID || receipt.ReleaseAttemptID() != bundle.Execution.AttemptID ||
		receipt.ReleaseID() != bundle.Release.ReleaseID || receipt.ReleaseID() != bundle.Execution.ReleaseID ||
		receipt.ReleaseRunID() != bundle.Release.ReleaseRunID || receipt.ReleaseRunID() != bundle.Execution.ReleaseRunID ||
		receipt.Architecture() != bundle.Release.Architecture || receipt.Architecture() != bundle.Execution.Architecture {
		return errors.New("CA42 journal release identity mismatch")
	}
	if receipt.PostgresSystemIdentifier() != bundle.Release.ProductionSourceSystemIdentifier ||
		receipt.PostgresSystemIdentifier() != bundle.Execution.SourceSystemIdentifier ||
		receipt.DatabaseName() != bundle.Release.ProductionSourceDatabase || receipt.DatabaseName() != bundle.Execution.SourceDatabase ||
		receipt.DatabaseOID() != bundle.Release.ProductionSourceDatabaseOID || receipt.DatabaseOID() != bundle.Execution.SourceDatabaseOID ||
		receipt.SourceWaterline() != "41" || receipt.AuthorizedTargetWaterline() != "42" ||
		receipt.MigrationSetSHA256() != bundle.Release.MigrationSetSHA256 ||
		receipt.MigrationSetSHA256() != bundle.Execution.MigrationSetSHA256 {
		return errors.New("CA42 journal database contract mismatch")
	}
	if !constantHexEqual(receipt.LayoutSwitchedHeadSHA256(), bundle.Release.ReleaseJournalHeadSHA256) ||
		!constantHexEqual(receipt.LayoutSwitchedHeadSHA256(), bundle.Execution.ReleaseJournalHeadSHA256) ||
		!constantHexEqual(receipt.LayoutSwitchedJournalSHA256(), bundle.Execution.ReleaseJournalSnapshotSHA256) {
		return errors.New("CA42 journal hash binding mismatch")
	}
	return nil
}

func constantHexEqual(left, right string) bool {
	return len(left) == 64 && len(right) == 64 && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
