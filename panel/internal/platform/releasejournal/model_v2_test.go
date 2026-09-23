package releasejournal

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func TestV2PreparedAndSharedStateMachine(t *testing.T) {
	identity := testPreparedV2()
	prepared, preparedHead, err := PreparedRecordBytes(identity)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(prepared, []byte("format="+FormatV2+"\n")) ||
		bytes.Contains(prepared, []byte("release_manifest_sha256=")) ||
		!bytes.Contains(prepared, []byte("release_contract_core_sha256="+identity.ReleaseContractCoreSHA256+"\n")) {
		t.Fatalf("v2 prepared envelope is not isolated from v1: %q", prepared)
	}
	segments := map[string][]byte{stateSegment[statePrepared]: prepared}
	snapshot, err := Parse(segments)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.JournalFormat != FormatV2 || snapshot.Prepared.ReleaseRunID != identity.ReleaseRunID ||
		snapshot.HeadSHA256 != preparedHead || snapshot.ManifestSHA == "" {
		t.Fatalf("v2 prepared snapshot lost identity: %#v", snapshot)
	}

	for next := normalNext[snapshot.State]; next != "" && snapshot.State != stateLayoutSwitched; next = normalNext[snapshot.State] {
		record := TransitionRecord{
			JournalFormat: FormatV2, JournalID: identity.JournalID, ReleaseAttemptID: identity.ReleaseAttemptID,
			Sequence: strconv.Itoa(len(snapshot.Names)), EventID: SHA256Bytes([]byte("event-" + string(next))),
			OccurredAtEpoch: "1700000001", PreviousState: snapshot.State, State: next,
			PreviousRecordSHA256: snapshot.HeadSHA256, EvidenceSHA256: SHA256Bytes([]byte("evidence-" + string(next))),
			EvidenceSize: "1",
		}
		data, _, buildErr := TransitionRecordBytes(record)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		segments[stateSegment[next]] = data
		snapshot, err = Parse(segments)
		if err != nil {
			t.Fatal(err)
		}
	}
	if snapshot.State != LayoutSwitched || len(snapshot.Names) != 7 || snapshot.JournalFormat != FormatV2 {
		t.Fatalf("shared state machine did not reach v2 layout-switched boundary: %#v", snapshot)
	}
}

func TestReceiptBoundarySequenceTableAndCopiesEvidence(t *testing.T) {
	before, _ := v2SegmentsToState(t, LayoutSwitchAttempted)
	if _, err := ParseReceipt(before); err == nil || err.Error() != "layout_switched_boundary_missing" {
		t.Fatalf("sequence 5 result=%v", err)
	}
	atBoundary, boundarySnapshot := v2SegmentsToState(t, LayoutSwitched)
	receipt, err := ParseReceipt(atBoundary)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.CurrentSequence() != 6 || receipt.CurrentState() != LayoutSwitched ||
		receipt.CurrentHeadSHA256() != receipt.LayoutSwitchedHeadSHA256() ||
		receipt.CurrentJournalSHA256() != receipt.LayoutSwitchedJournalSHA256() {
		t.Fatalf("wrong sequence-6 boundary: state=%s sequence=%d", receipt.CurrentState(), receipt.CurrentSequence())
	}
	if len(boundarySnapshot.Transitions) != 6 || boundarySnapshot.Transitions[5].Sequence != "6" {
		t.Fatal("layout-switched record is not explicitly sequence 6")
	}
	originalHead, originalJournal := receipt.CurrentHeadSHA256(), receipt.CurrentJournalSHA256()
	for name, data := range atBoundary {
		data[0] ^= 0xff
		atBoundary[name] = []byte("mutated")
	}
	if receipt.CurrentHeadSHA256() != originalHead || receipt.CurrentJournalSHA256() != originalJournal {
		t.Fatal("receipt borrowed mutable input evidence")
	}
	after, _ := v2SegmentsToState(t, AdmissionAttempted)
	advanced, err := ParseReceipt(after)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.CurrentSequence() != 7 || advanced.CurrentState() != AdmissionAttempted ||
		advanced.LayoutSwitchedHeadSHA256() != originalHead || advanced.LayoutSwitchedJournalSHA256() != originalJournal {
		t.Fatal("advanced journal lost its immutable sequence-6 prefix")
	}
}

func TestReceiptRejectsEquivalentV1Journal(t *testing.T) {
	snapshot := testJournal(t, stateLayoutSwitched)
	segments := make(map[string][]byte, len(snapshot.Names))
	for index, name := range snapshot.Names {
		segments[name] = append([]byte(nil), snapshot.RecordBytes[index]...)
	}
	if _, err := ParseReceipt(segments); err == nil || err.Error() != "journal_v2_required" {
		t.Fatalf("v1 receipt result=%v", err)
	}
}

func TestV2RejectsDowngradeAndInvalidContract(t *testing.T) {
	base := testPreparedV2()
	mutations := []struct {
		name string
		edit func(*PreparedIdentity)
	}{
		{name: "zero journal", edit: func(v *PreparedIdentity) { v.JournalID = strings.Repeat("0", 64) }},
		{name: "missing release run", edit: func(v *PreparedIdentity) { v.ReleaseRunID = "" }},
		{name: "legacy manifest hash", edit: func(v *PreparedIdentity) { v.ReleaseManifestSHA256 = strings.Repeat("a", 64) }},
		{name: "wrong core format", edit: func(v *PreparedIdentity) { v.ReleaseContractCoreFormat = "legacy" }},
		{name: "zero core", edit: func(v *PreparedIdentity) { v.ReleaseContractCoreSHA256 = strings.Repeat("0", 64) }},
		{name: "wrong controller domain", edit: func(v *PreparedIdentity) { v.ControllerContract = "migration-runner" }},
		{name: "OID overflow", edit: func(v *PreparedIdentity) { v.DatabaseOID = "4294967296" }},
		{name: "wrong source waterline", edit: func(v *PreparedIdentity) { v.SourceWaterline = "40" }},
		{name: "wrong target waterline", edit: func(v *PreparedIdentity) { v.AuthorizedTargetWaterline = "43" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := base
			mutation.edit(&candidate)
			if _, _, err := PreparedRecordBytes(candidate); err == nil {
				t.Fatal("accepted invalid v2 prepared contract")
			}
		})
	}

	prepared, head, err := PreparedRecordBytes(base)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := Parse(map[string][]byte{stateSegment[statePrepared]: prepared})
	if err != nil {
		t.Fatal(err)
	}
	record := TransitionRecord{
		JournalFormat: FormatV2, JournalID: base.JournalID, ReleaseAttemptID: base.ReleaseAttemptID,
		Sequence: "1", EventID: strings.Repeat("b", 64), OccurredAtEpoch: "1700000001",
		PreviousState: Prepared, State: IsolationAttempted, PreviousRecordSHA256: head,
		EvidenceSHA256: strings.Repeat("c", 64), EvidenceSize: "1",
	}
	transition, _, err := TransitionRecordBytes(record)
	if err != nil {
		t.Fatal(err)
	}
	downgraded := bytes.Replace(transition, []byte("format="+FormatV2), []byte("format="+FormatV1), 1)
	if _, _, err := ParseTransitionRecord(downgraded, prior); err == nil {
		t.Fatal("accepted v1 transition in a v2 journal")
	}
	if journalManifestSHA(FormatV1, []string{"000.prepared.record"}, []string{strings.Repeat("a", 64)}, strings.Repeat("b", 64)) ==
		journalManifestSHA(FormatV2, []string{"000.prepared.record"}, []string{strings.Repeat("a", 64)}, strings.Repeat("b", 64)) {
		t.Fatal("v1 and v2 journal manifest domains collide")
	}
}

func testPreparedV2() PreparedIdentity {
	return PreparedIdentity{
		JournalFormat: FormatV2, JournalID: strings.Repeat("1", 64), ReleaseAttemptID: "attempt-ca42-1",
		CreatedAtEpoch: "1700000000", ReleaseID: "release-ca42-1", ReleaseRunID: "run-ca42-1", Architecture: "amd64",
		ReleaseContractCoreFormat: ReleaseCoreFormatV1, ReleaseContractCoreSHA256: strings.Repeat("2", 64),
		ControllerContract: ControllerContractV1, ReleaseControllerSHA256: strings.Repeat("3", 64),
		TargetIdentitySHA256: strings.Repeat("4", 64), TargetRootDevice: "2049", TargetRootInode: "4096",
		StagedTreeManifestSHA256: strings.Repeat("5", 64), LiveTreeManifestSHA256: strings.Repeat("6", 64),
		EnvironmentFileSHA256: strings.Repeat("7", 64), BackupControllerSHA256: strings.Repeat("8", 64),
		MachineIdentitySHA256: strings.Repeat("9", 64), BootIDSHA256: strings.Repeat("a", 64),
		PostgresSystemIdentifier: "1111111111111111111", DatabaseName: "aegis", DatabaseOID: "16384",
		SourceWaterline: "41", AuthorizedTargetWaterline: "42", MigrationSetSHA256: strings.Repeat("b", 64),
		IngressUnitSHA256: strings.Repeat("c", 64), WriterUnitsSHA256: strings.Repeat("d", 64),
		HealthConfigSHA256: strings.Repeat("e", 64),
	}
}

func v2SegmentsToState(t *testing.T, terminal State) (map[string][]byte, Snapshot) {
	t.Helper()
	identity := testPreparedV2()
	prepared, _, err := PreparedRecordBytes(identity)
	if err != nil {
		t.Fatal(err)
	}
	segments := map[string][]byte{stateSegment[statePrepared]: prepared}
	snapshot, err := Parse(segments)
	if err != nil {
		t.Fatal(err)
	}
	for snapshot.State != terminal {
		next := normalNext[snapshot.State]
		if next == "" {
			t.Fatalf("terminal %s is not on normal path", terminal)
		}
		record := TransitionRecord{
			JournalFormat: FormatV2, JournalID: identity.JournalID, ReleaseAttemptID: identity.ReleaseAttemptID,
			Sequence: strconv.Itoa(len(snapshot.Names)), EventID: SHA256Bytes([]byte("event-" + string(next))),
			OccurredAtEpoch: strconv.Itoa(1700000000 + len(snapshot.Names)), PreviousState: snapshot.State, State: next,
			PreviousRecordSHA256: snapshot.HeadSHA256, EvidenceSHA256: SHA256Bytes([]byte("evidence-" + string(next))),
			EvidenceSize: "1",
		}
		data, _, buildErr := TransitionRecordBytes(record)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		segments[stateSegment[next]] = data
		snapshot, err = Parse(segments)
		if err != nil {
			t.Fatal(err)
		}
	}
	return segments, snapshot
}
