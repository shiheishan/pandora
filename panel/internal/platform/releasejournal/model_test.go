package releasejournal

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

const testHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testPreparedIdentity() preparedIdentity {
	return preparedIdentity{
		JournalID: testHash, ReleaseAttemptID: "attempt-1", CreatedAtEpoch: "1700000000",
		ReleaseID: "release-1", Architecture: "amd64", ControllerSHA256: testHash,
		ReleaseManifestSHA256: testHash, TargetIdentitySHA256: testHash,
		TargetRootDevice: "1", TargetRootInode: "2", StagedTreeManifestSHA256: testHash,
		LiveTreeManifestSHA256: testHash, EnvironmentFileSHA256: testHash,
		BackupControllerSHA256: testHash, MachineIdentitySHA256: testHash,
		BootIDSHA256: testHash, PostgresSystemIdentifier: "3", DatabaseName: "pandora",
		DatabaseOID: "4", SourceWaterline: "42", AuthorizedTargetWaterline: "43",
		MigrationSetSHA256: testHash, IngressUnitSHA256: testHash,
		WriterUnitsSHA256: testHash, HealthConfigSHA256: testHash,
	}
}

func testJournal(t *testing.T, terminal releaseState) releaseJournalSnapshot {
	t.Helper()
	identity := testPreparedIdentity()
	Prepared, _, err := preparedRecordBytes(identity)
	if err != nil {
		t.Fatal(err)
	}
	segments := map[string][]byte{stateSegment[statePrepared]: Prepared}
	snapshot, err := parseReleaseJournal(segments)
	if err != nil {
		t.Fatal(err)
	}
	for snapshot.State != terminal {
		next := normalNext[snapshot.State]
		if next == "" {
			t.Fatalf("no path from %s to %s", snapshot.State, terminal)
		}
		record := transitionRecord{
			JournalID: identity.JournalID, ReleaseAttemptID: identity.ReleaseAttemptID,
			Sequence: strconv.Itoa(len(snapshot.Names)), EventID: sha256Bytes([]byte(strconv.Itoa(len(snapshot.Names)))),
			OccurredAtEpoch: strconv.Itoa(1700000000 + len(snapshot.Names)),
			PreviousState:   snapshot.State, State: next, PreviousRecordSHA256: snapshot.HeadSHA256,
			EvidenceSHA256: testHash, EvidenceSize: "1",
		}
		data, _, buildErr := transitionRecordBytes(record)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		segments[stateSegment[next]] = data
		snapshot, err = parseReleaseJournal(segments)
		if err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func TestCompleteStateGraph(t *testing.T) {
	snapshot := testJournal(t, stateCommitted)
	if snapshot.State != stateCommitted || len(snapshot.Names) != 15 || !hex64RE.MatchString(snapshot.ManifestSHA) {
		t.Fatalf("unexpected terminal snapshot: state=%s segments=%d manifest=%q", snapshot.State, len(snapshot.Names), snapshot.ManifestSHA)
	}
	if _, err := snapshotPrefix(snapshot, len(snapshot.Names)-1); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRequiredFromEveryNonterminalState(t *testing.T) {
	states := []releaseState{statePrepared}
	for State := statePrepared; normalNext[State] != ""; State = normalNext[State] {
		states = append(states, normalNext[State])
	}
	for _, State := range states[:len(states)-1] {
		if err := validateTransition(State, stateRecoveryRequired); err != nil {
			t.Fatalf("%s: %v", State, err)
		}
	}
	if err := validateTransition(stateCommitted, stateRecoveryRequired); err == nil {
		t.Fatal("committed state accepted transition")
	}
}

func TestIllegalTransitionsRejected(t *testing.T) {
	for from := range stateSegment {
		for to := range stateSegment {
			err := validateTransition(from, to)
			allowed := from != stateCommitted && from != stateRecoveryRequired && (to == stateRecoveryRequired || normalNext[from] == to)
			if (err == nil) != allowed {
				t.Fatalf("transition %s -> %s allowed=%v err=%v", from, to, allowed, err)
			}
		}
	}
}

func TestCanonicalRecordTamperingRejected(t *testing.T) {
	data, _, err := preparedRecordBytes(testPreparedIdentity())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"missing-final-lf": bytes.TrimSuffix(data, []byte{'\n'}),
		"cr":               append([]byte(nil), append(data[:10], append([]byte{'\r'}, data[10:]...)...)...),
		"nul":              append([]byte(nil), append(data[:10], append([]byte{0}, data[10:]...)...)...),
		"hash":             bytes.Replace(data, []byte("record_sha256="), []byte("record_sha256=b"), 1),
		"order":            bytes.Replace(data, []byte("format="), []byte("xformat="), 1),
	}
	for name, mutated := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, parseErr := parsePreparedRecord(mutated); parseErr == nil {
				t.Fatal("tampering accepted")
			}
		})
	}
}

func TestSegmentSkipAndBytesAfterTerminalRejected(t *testing.T) {
	identity := testPreparedIdentity()
	_, preparedSHA, err := preparedRecordBytes(identity)
	if err != nil {
		t.Fatal(err)
	}
	skipped := transitionRecord{JournalID: identity.JournalID, ReleaseAttemptID: identity.ReleaseAttemptID, Sequence: "1", EventID: testHash, OccurredAtEpoch: "1700000001", PreviousState: statePrepared, State: stateIsolated, PreviousRecordSHA256: preparedSHA, EvidenceSHA256: testHash, EvidenceSize: "1"}
	if _, _, err = transitionRecordBytes(skipped); err == nil {
		t.Fatal("skipped transition built")
	}

	snapshot := testJournal(t, stateCommitted)
	segments := map[string][]byte{}
	for i, name := range snapshot.Names {
		segments[name] = snapshot.RecordBytes[i]
	}
	segments[stateSegment[stateRecoveryRequired]] = snapshot.RecordBytes[len(snapshot.RecordBytes)-1]
	if _, err = parseReleaseJournal(segments); err == nil {
		t.Fatal("bytes after terminal accepted")
	}
}

func TestPreparedIdentityWaterlineAndCanonicalIntegers(t *testing.T) {
	identity := testPreparedIdentity()
	identity.AuthorizedTargetWaterline = "42"
	if _, _, err := preparedRecordBytes(identity); err == nil {
		t.Fatal("equal waterline accepted")
	}
	identity = testPreparedIdentity()
	identity.TargetRootDevice = "01"
	if _, _, err := preparedRecordBytes(identity); err == nil {
		t.Fatal("non-canonical integer accepted")
	}
}

func TestTransitionRecordAllBindingsAndHashTamperRejected(t *testing.T) {
	base := testJournal(t, stateIsolationAttempted)
	segments := map[string][]byte{}
	for index, name := range base.Names {
		segments[name] = append([]byte(nil), base.RecordBytes[index]...)
	}
	lastName := base.Names[len(base.Names)-1]
	cases := map[string]func([]byte) []byte{
		"journal": func(data []byte) []byte {
			return bytes.Replace(data, []byte("journal_id="+testHash), []byte("journal_id="+strings.Repeat("b", 64)), 1)
		},
		"attempt": func(data []byte) []byte {
			return bytes.Replace(data, []byte("release_attempt_id=attempt-1"), []byte("release_attempt_id=attempt-2"), 1)
		},
		"sequence": func(data []byte) []byte { return bytes.Replace(data, []byte("sequence=1"), []byte("sequence=2"), 1) },
		"previous-state": func(data []byte) []byte {
			return bytes.Replace(data, []byte("previous_state=PREPARED"), []byte("previous_state=ISOLATED"), 1)
		},
		"previous-hash": func(data []byte) []byte {
			return bytes.Replace(data, []byte("previous_record_sha256="), []byte("previous_record_sha256=b"), 1)
		},
		"record-hash": func(data []byte) []byte {
			return bytes.Replace(data, []byte("record_sha256="), []byte("record_sha256=b"), 1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			copySegments := map[string][]byte{}
			for segmentName, data := range segments {
				copySegments[segmentName] = append([]byte(nil), data...)
			}
			copySegments[lastName] = mutate(copySegments[lastName])
			if _, err := parseReleaseJournal(copySegments); err == nil {
				t.Fatal("tampered transition accepted")
			}
		})
	}
	wrongName := map[string][]byte{stateSegment[statePrepared]: base.RecordBytes[0], stateSegment[stateIsolated]: base.RecordBytes[1]}
	if _, err := parseReleaseJournal(wrongName); err == nil {
		t.Fatal("segment name mismatch accepted")
	}
}

func TestTransitionReplayAndEvidenceBoundsRejected(t *testing.T) {
	base := testJournal(t, stateIsolated)
	segments := map[string][]byte{}
	for index, name := range base.Names {
		segments[name] = append([]byte(nil), base.RecordBytes[index]...)
	}
	last := base.Transitions[len(base.Transitions)-1]
	last.EventID = base.Transitions[0].EventID
	last.PreviousRecordSHA256 = base.Transitions[0].RecordSHA256
	last.PreviousState = stateIsolationAttempted
	data, _, err := transitionRecordBytes(last)
	if err != nil {
		t.Fatal(err)
	}
	segments[stateSegment[stateIsolated]] = data
	if _, err := parseReleaseJournal(segments); err == nil {
		t.Fatal("event replay accepted")
	}

	for _, value := range []string{"0", strconv.FormatInt(maxRecordBytes+1, 10), "18446744073709551616"} {
		record := transitionRecord{JournalID: testHash, ReleaseAttemptID: "attempt-1", Sequence: "1", EventID: strings.Repeat("b", 64), OccurredAtEpoch: "1", PreviousState: statePrepared, State: stateIsolationAttempted, PreviousRecordSHA256: testHash, EvidenceSHA256: testHash, EvidenceSize: value}
		built, _, buildErr := transitionRecordBytes(record)
		if buildErr == nil {
			Prepared, _, _ := preparedRecordBytes(testPreparedIdentity())
			if _, parseErr := parseReleaseJournal(map[string][]byte{stateSegment[statePrepared]: Prepared, stateSegment[stateIsolationAttempted]: built}); parseErr == nil {
				t.Fatalf("evidence size %s accepted", value)
			}
		}
	}
}

func TestEveryCrashPrefixAndRecoveryTerminalParsesExactly(t *testing.T) {
	full := testJournal(t, stateCommitted)
	for count := 1; count < len(full.Names); count++ {
		prefix, err := snapshotPrefix(full, count)
		if err != nil {
			t.Fatal(err)
		}
		record := transitionRecord{JournalID: prefix.Prepared.JournalID, ReleaseAttemptID: prefix.Prepared.ReleaseAttemptID, Sequence: strconv.Itoa(len(prefix.Names)), EventID: sha256Bytes([]byte("recovery-" + strconv.Itoa(count))), OccurredAtEpoch: strconv.Itoa(1800000000 + count), PreviousState: prefix.State, State: stateRecoveryRequired, PreviousRecordSHA256: prefix.HeadSHA256, EvidenceSHA256: testHash, EvidenceSize: "1"}
		data, _, err := transitionRecordBytes(record)
		if err != nil {
			t.Fatal(err)
		}
		segments := map[string][]byte{}
		for index, name := range prefix.Names {
			segments[name] = prefix.RecordBytes[index]
		}
		segments[stateSegment[stateRecoveryRequired]] = data
		recovered, err := parseReleaseJournal(segments)
		if err != nil {
			t.Fatalf("prefix %d: %v", count, err)
		}
		if recovered.State != stateRecoveryRequired || len(recovered.Names) != count+1 {
			t.Fatalf("prefix %d recovery mismatch", count)
		}
	}
}

func TestPreparedIdentityAllFieldsCanonical(t *testing.T) {
	cases := []func(*preparedIdentity){
		func(v *preparedIdentity) { v.ReleaseAttemptID = "bad/value" },
		func(v *preparedIdentity) { v.ReleaseID = "" },
		func(v *preparedIdentity) { v.Architecture = "riscv64" },
		func(v *preparedIdentity) { v.ControllerSHA256 = strings.Repeat("A", 64) },
		func(v *preparedIdentity) { v.DatabaseName = "bad-name" },
		func(v *preparedIdentity) { v.DatabaseOID = "0" },
		func(v *preparedIdentity) { v.PostgresSystemIdentifier = "18446744073709551616" },
		func(v *preparedIdentity) { v.SourceWaterline = "01" },
		func(v *preparedIdentity) { v.AuthorizedTargetWaterline = "0" },
	}
	for index, mutate := range cases {
		identity := testPreparedIdentity()
		mutate(&identity)
		if _, _, err := preparedRecordBytes(identity); err == nil {
			t.Fatalf("invalid identity case %d accepted", index)
		}
	}
}

func TestSameSecondTransitionAcceptedButEarlierRejected(t *testing.T) {
	identity := testPreparedIdentity()
	Prepared, preparedSHA, err := preparedRecordBytes(identity)
	if err != nil {
		t.Fatal(err)
	}
	record := transitionRecord{JournalID: identity.JournalID, ReleaseAttemptID: identity.ReleaseAttemptID, Sequence: "1", EventID: strings.Repeat("b", 64), OccurredAtEpoch: identity.CreatedAtEpoch, PreviousState: statePrepared, State: stateIsolationAttempted, PreviousRecordSHA256: preparedSHA, EvidenceSHA256: testHash, EvidenceSize: "1"}
	data, _, err := transitionRecordBytes(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseReleaseJournal(map[string][]byte{stateSegment[statePrepared]: Prepared, stateSegment[stateIsolationAttempted]: data}); err != nil {
		t.Fatalf("same-second transition rejected: %v", err)
	}
	record.OccurredAtEpoch = "1699999999"
	data, _, err = transitionRecordBytes(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseReleaseJournal(map[string][]byte{stateSegment[statePrepared]: Prepared, stateSegment[stateIsolationAttempted]: data}); err == nil {
		t.Fatal("earlier transition accepted")
	}
}
