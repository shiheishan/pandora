package releasejournal

import "errors"

const layoutSwitchedSegmentCount = 7

// Receipt is an immutable authorization-facing projection produced only after
// a complete canonical journal replay. Raw segment slices remain outside this
// type so callers cannot mutate replay evidence and then reuse the projection.
type Receipt struct {
	journalFormat, journalID                                string
	releaseAttemptID, releaseID, releaseRunID, architecture string
	releaseCoreFormat, releaseCoreSHA256                    string
	postgresSystemIdentifier, databaseName, databaseOID     string
	sourceWaterline, targetWaterline, migrationSetSHA256    string
	currentState                                            State
	currentSequence                                         uint64
	currentHeadSHA256, currentJournalSHA256                 string
	boundaryHeadSHA256, boundaryJournalSHA256               string
}

func ParseReceipt(segments map[string][]byte) (Receipt, error) {
	snapshot, err := parseReleaseJournal(segments)
	if err != nil {
		return Receipt{}, err
	}
	return receiptFromSnapshot(snapshot)
}

func receiptFromSnapshot(snapshot releaseJournalSnapshot) (Receipt, error) {
	if snapshot.JournalFormat != releaseJournalFormatV2 || snapshot.Prepared.JournalFormat != releaseJournalFormatV2 {
		return Receipt{}, errors.New("journal_v2_required")
	}
	if len(snapshot.Names) < layoutSwitchedSegmentCount {
		return Receipt{}, errors.New("layout_switched_boundary_missing")
	}
	boundary, err := snapshotPrefix(snapshot, layoutSwitchedSegmentCount)
	if err != nil || boundary.State != stateLayoutSwitched || len(boundary.Names) != layoutSwitchedSegmentCount {
		return Receipt{}, errors.New("layout_switched_boundary_invalid")
	}
	prepared := snapshot.Prepared
	return Receipt{
		journalFormat: snapshot.JournalFormat, journalID: prepared.JournalID,
		releaseAttemptID: prepared.ReleaseAttemptID, releaseID: prepared.ReleaseID,
		releaseRunID: prepared.ReleaseRunID, architecture: prepared.Architecture,
		releaseCoreFormat: prepared.ReleaseContractCoreFormat, releaseCoreSHA256: prepared.ReleaseContractCoreSHA256,
		postgresSystemIdentifier: prepared.PostgresSystemIdentifier, databaseName: prepared.DatabaseName,
		databaseOID: prepared.DatabaseOID, sourceWaterline: prepared.SourceWaterline,
		targetWaterline: prepared.AuthorizedTargetWaterline, migrationSetSHA256: prepared.MigrationSetSHA256,
		currentState: snapshot.State, currentSequence: uint64(len(snapshot.Names) - 1),
		currentHeadSHA256: snapshot.HeadSHA256, currentJournalSHA256: snapshot.ManifestSHA,
		boundaryHeadSHA256: boundary.HeadSHA256, boundaryJournalSHA256: boundary.ManifestSHA,
	}, nil
}

func (r Receipt) JournalFormat() string               { return r.journalFormat }
func (r Receipt) JournalID() string                   { return r.journalID }
func (r Receipt) ReleaseAttemptID() string            { return r.releaseAttemptID }
func (r Receipt) ReleaseID() string                   { return r.releaseID }
func (r Receipt) ReleaseRunID() string                { return r.releaseRunID }
func (r Receipt) Architecture() string                { return r.architecture }
func (r Receipt) ReleaseCoreFormat() string           { return r.releaseCoreFormat }
func (r Receipt) ReleaseCoreSHA256() string           { return r.releaseCoreSHA256 }
func (r Receipt) PostgresSystemIdentifier() string    { return r.postgresSystemIdentifier }
func (r Receipt) DatabaseName() string                { return r.databaseName }
func (r Receipt) DatabaseOID() string                 { return r.databaseOID }
func (r Receipt) SourceWaterline() string             { return r.sourceWaterline }
func (r Receipt) AuthorizedTargetWaterline() string   { return r.targetWaterline }
func (r Receipt) MigrationSetSHA256() string          { return r.migrationSetSHA256 }
func (r Receipt) CurrentState() State                 { return r.currentState }
func (r Receipt) CurrentSequence() uint64             { return r.currentSequence }
func (r Receipt) CurrentHeadSHA256() string           { return r.currentHeadSHA256 }
func (r Receipt) CurrentJournalSHA256() string        { return r.currentJournalSHA256 }
func (r Receipt) LayoutSwitchedHeadSHA256() string    { return r.boundaryHeadSHA256 }
func (r Receipt) LayoutSwitchedJournalSHA256() string { return r.boundaryJournalSHA256 }

// LayoutSwitchedBoundary returns the immutable sequence-6 projection even when
// the current journal is its sequence-7 admission-attempted exact-retry form.
func (r Receipt) LayoutSwitchedBoundary() (Receipt, error) {
	if r.journalFormat != releaseJournalFormatV2 || r.boundaryHeadSHA256 == "" || r.boundaryJournalSHA256 == "" {
		return Receipt{}, errors.New("layout_switched_boundary_invalid")
	}
	boundary := r
	boundary.currentState = stateLayoutSwitched
	boundary.currentSequence = layoutSwitchedSegmentCount - 1
	boundary.currentHeadSHA256 = boundary.boundaryHeadSHA256
	boundary.currentJournalSHA256 = boundary.boundaryJournalSHA256
	return boundary, nil
}
