package releasejournal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
)

func TestV3FullAdmissionChainAndLegacyIsolation(t *testing.T) {
	records, snapshot := v3RecordsTo(t, V3AdmissionVerified)
	if snapshot.State() != V3AdmissionVerified || snapshot.Sequence() != 9 || snapshot.TransactionID() == "" || snapshot.NonceID() == "" {
		t.Fatalf("unexpected v3 snapshot: state=%s sequence=%d", snapshot.State(), snapshot.Sequence())
	}
	sameBody := []byte("same-canonical-manifest-body\n")
	v2Digest := sha256.Sum256(sameBody)
	v3Hasher := sha256.New()
	_, _ = v3Hasher.Write([]byte(v3ManifestDomain))
	_, _ = v3Hasher.Write(sameBody)
	if hex.EncodeToString(v2Digest[:]) == hex.EncodeToString(v3Hasher.Sum(nil)) {
		t.Fatal("v2 and v3 manifest domains collided")
	}
	if _, err := Parse(records); err == nil {
		t.Fatal("legacy parser accepted v3")
	}
	legacy, _ := v2SegmentsToState(t, LayoutSwitched)
	if _, err := ParseV3(legacy); err == nil {
		t.Fatal("v3 parser accepted v2")
	}
}

func TestV3AllPrefixesAndPostAdmissionChain(t *testing.T) {
	for _, terminal := range v3NormalOrder {
		t.Run(string(terminal), func(t *testing.T) {
			records, snapshot := v3RecordsTo(t, terminal)
			if len(records) != int(snapshot.Sequence())+1 || snapshot.State() != terminal {
				t.Fatalf("wrong prefix: %#v", snapshot)
			}
			if _, err := snapshot.VerifiedCopy(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestV3RecoveryPathsAndExpirySemantics(t *testing.T) {
	zero := strings.Repeat("0", 64)
	for _, previous := range v3NormalOrder[:len(v3NormalOrder)-1] {
		records, snapshot := v3RecordsTo(t, previous)
		tx, nonce := zero, zero
		if v3StateSequence(previous) >= 7 {
			tx, nonce = snapshot.TransactionID(), snapshot.NonceID()
		}
		reason, occurred := "durability_outcome_ambiguous", int64(1700001000)
		if previous == V3AdmissionReserved {
			reason = "claim_expired_before_attempt"
		} else if previous == V3AdmissionAttempted {
			reason = "consumer_outcome_ambiguous"
		} else if v3StateSequence(previous) > 8 {
			reason = "cross_store_state_divergent"
		}
		data, _, err := v3RecoveryRecordBytes(v3RecoveryFields{JournalID: v3H("journal"), AttemptID: "attempt-a", TransactionID: tx,
			NonceID: nonce, ReasonCode: reason, OccurredAtEpoch: occurred, PreviousState: previous, PreviousRecordSHA256: snapshot.HeadSHA256()}, snapshot.Sequence()+1)
		if err != nil {
			t.Fatal(err)
		}
		records[v3Segments[V3RecoveryRequired]] = data
		recovered, err := ParseV3(records)
		if err != nil {
			t.Fatal(err)
		}
		if recovered.State() != V3RecoveryRequired {
			t.Fatal("recovery not terminal")
		}
		records["999.after.record"] = []byte("x")
		if _, err := ParseV3(records); err == nil {
			t.Fatal("bytes after recovery accepted")
		}
	}
	reservedRecords, reserved := v3RecordsTo(t, V3AdmissionReserved)
	early, _, err := v3RecoveryRecordBytes(v3RecoveryFields{JournalID: v3H("journal"), AttemptID: "attempt-a", TransactionID: v3H("tx"),
		NonceID: v3H("nonce"), ReasonCode: "claim_expired_before_attempt", OccurredAtEpoch: 1700000999,
		PreviousState: V3AdmissionReserved, PreviousRecordSHA256: reserved.HeadSHA256()}, 8)
	if err != nil {
		t.Fatal(err)
	}
	reservedRecords[v3Segments[V3RecoveryRequired]] = early
	if _, err := ParseV3(reservedRecords); err == nil {
		t.Fatal("early expiry recovery accepted")
	}
	if _, _, err := v3RecoveryRecordBytes(v3RecoveryFields{JournalID: v3H("journal"), AttemptID: "attempt-a", TransactionID: zero,
		NonceID: zero, ReasonCode: "consumer_outcome_ambiguous", OccurredAtEpoch: 1700000001, PreviousState: V3Prepared,
		PreviousRecordSHA256: v3H("head")}, 1); err == nil {
		t.Fatal("pre-consumer ambiguity reason accepted")
	}
	if _, _, err := v3RecoveryRecordBytes(v3RecoveryFields{JournalID: v3H("journal"), AttemptID: "attempt-a", TransactionID: zero,
		NonceID: zero, ReasonCode: "durability_outcome_ambiguous", OccurredAtEpoch: 1700000001, PreviousState: V3Prepared,
		PreviousRecordSHA256: v3H("head")}, 15); err == nil {
		t.Fatal("recovery sequence mismatch accepted")
	}
}

func TestV3PublicParserRejectsRehashedSemanticAttacks(t *testing.T) {
	base, _ := v3RecordsTo(t, V3AdmissionVerified)
	attacks := []struct{ name, segment, key, value, kind string }{
		{"journal id", v3Segments[V3IsolationAttempted], "journal_id", v3H("other-journal"), "transition"},
		{"attempt id", v3Segments[V3IsolationAttempted], "attempt_id", "other-attempt", "transition"},
		{"sequence", v3Segments[V3IsolationAttempted], "sequence", "2", "transition"},
		{"previous state", v3Segments[V3IsolationAttempted], "previous_state", string(V3Isolated), "transition"},
		{"state", v3Segments[V3IsolationAttempted], "state", string(V3Isolated), "transition"},
		{"previous hash", v3Segments[V3IsolationAttempted], "previous_record_sha256", v3H("other-head"), "transition"},
		{"duplicate event", v3Segments[V3Isolated], "event_id", v3H("event-" + string(V3IsolationAttempted)), "transition"},
		{"legacy core", v3Segments[V3Prepared], "release_contract_core_format", ReleaseCoreFormatV1, "prepared"},
		{"profile digest", v3Segments[V3AdmissionReserved], "profile_sha256", strings.Repeat("f", 64), "admission-reserved"},
		{"claim format", v3Segments[V3AdmissionReserved], "claim_format", "client-auth-00042-consumption-claim-v1", "admission-reserved"},
		{"artifact format", v3Segments[V3AdmissionReserved], "artifact_set_format", "client-auth-00042-artifact-set-v2", "admission-reserved"},
		{"record kind", v3Segments[V3AdmissionReserved], "record", "transition", "admission-reserved"},
		{"architecture", v3Segments[V3AdmissionReserved], "architecture", "riscv64", "admission-reserved"},
		{"transaction", v3Segments[V3AdmissionReserved], "transaction_id", v3H("other-tx"), "admission-reserved"},
		{"nonce", v3Segments[V3AdmissionReserved], "nonce_id", v3H("other-nonce"), "admission-reserved"},
		{"boundary head", v3Segments[V3AdmissionReserved], "boundary_head_sha256", v3H("other"), "admission-reserved"},
		{"boundary manifest", v3Segments[V3AdmissionReserved], "boundary_manifest_sha256", v3H("other"), "admission-reserved"},
		{"non-genesis zero ledger", v3Segments[V3AdmissionReserved], "ledger_before_sha256", strings.Repeat("0", 64), "admission-reserved"},
		{"ledger planned", v3Segments[V3AdmissionReserved], "ledger_planned_sha256", v3H("other-ledger"), "admission-reserved"},
		{"authority sequence", v3Segments[V3AdmissionReserved], "authority_sequence", "0", "admission-reserved"},
		{"invalid window", v3Segments[V3AdmissionReserved], "effective_not_before_epoch", "1700001000", "admission-reserved"},
		{"reservation expired", v3Segments[V3AdmissionReserved], "reserved_at_epoch", "1700001000", "admission-reserved"},
		{"attempt transaction", v3Segments[V3AdmissionAttempted], "transaction_id", v3H("other-tx"), "admission-attempted"},
		{"attempt nonce", v3Segments[V3AdmissionAttempted], "nonce_id", v3H("other-nonce"), "admission-attempted"},
		{"nonce reservation", v3Segments[V3AdmissionAttempted], "nonce_reservation_sha256", v3H("other"), "admission-attempted"},
		{"consumer operation", v3Segments[V3AdmissionVerified], "consumer_operation_id", v3H("other"), "admission-verified"},
		{"verified transaction", v3Segments[V3AdmissionVerified], "transaction_id", v3H("other-tx"), "admission-verified"},
		{"verified nonce", v3Segments[V3AdmissionVerified], "nonce_id", v3H("other-nonce"), "admission-verified"},
		{"receipt size", v3Segments[V3AdmissionVerified], "consumer_receipt_size", "1048577", "admission-verified"},
		{"ledger after", v3Segments[V3AdmissionVerified], "ledger_after_sha256", v3H("other"), "admission-verified"},
		{"completion at expiry", v3Segments[V3AdmissionVerified], "consumer_completed_at_epoch", "1700001000", "admission-verified"},
		{"noncanonical plus time", v3Segments[V3AdmissionAttempted], "attempted_at_epoch", "+1700000200", "admission-attempted"},
		{"noncanonical plus authority", v3Segments[V3AdmissionReserved], "authority_epoch", "+2", "admission-reserved"},
	}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			candidate := cloneV3Records(base)
			candidate[attack.segment] = v3RehashField(t, candidate[attack.segment], attack.key, attack.value, attack.kind)
			// Rehash every successor so the test reaches semantic validation rather than only stale-hash rejection.
			names := []string{v3Segments[V3Prepared], v3Segments[V3IsolationAttempted], v3Segments[V3Isolated], v3Segments[V3BackupAttempted], v3Segments[V3BackupVerified], v3Segments[V3LayoutSwitchAttempted], v3Segments[V3LayoutSwitched], v3Segments[V3AdmissionReserved], v3Segments[V3AdmissionAttempted], v3Segments[V3AdmissionVerified]}
			found := false
			previous := ""
			for _, name := range names {
				if name == attack.segment {
					found = true
					previous = name
					continue
				}
				if found {
					candidate[name] = v3RehashField(t, candidate[name], "previous_record_sha256", v3RecordDigest(candidate[previous]), v3KindForName(name))
				}
				previous = name
			}
			if _, err := ParseV3(candidate); err == nil {
				t.Fatal("accepted rehashed semantic attack")
			}
		})
	}
}

func TestV3EnvelopeManifestAndDeepCopy(t *testing.T) {
	records, snapshot := v3RecordsTo(t, V3AdmissionVerified)
	originalHead, originalManifest := snapshot.HeadSHA256(), snapshot.ManifestSHA256()
	for name, data := range records {
		data[0] ^= 0xff
		records[name] = []byte("mutated")
	}
	copySnapshot, err := snapshot.VerifiedCopy()
	if err != nil {
		t.Fatal(err)
	}
	if copySnapshot.HeadSHA256() != originalHead || copySnapshot.ManifestSHA256() != originalManifest {
		t.Fatal("snapshot borrowed caller buffers")
	}
	if _, err := (V3Snapshot{}).VerifiedCopy(); err == nil {
		t.Fatal("zero snapshot verified")
	}
	manifest, err := snapshot.ManifestBytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseV3Bundle(manifest, snapshot.records); err != nil {
		t.Fatal(err)
	}
	manifest[0] ^= 0xff
	if _, err := ParseV3Bundle(manifest, snapshot.records); err == nil {
		t.Fatal("mutated manifest accepted")
	}
	boundary, err := snapshot.LayoutSwitchedBoundary()
	if err != nil || boundary.State() != V3LayoutSwitched || boundary.Sequence() != 6 {
		t.Fatalf("boundary=%#v err=%v", boundary, err)
	}

	valid, _ := v3RecordsTo(t, V3LayoutSwitched)
	preparedName := v3Segments[V3Prepared]
	for _, mutation := range [][]byte{nil, {}, bytes.TrimSuffix(valid[preparedName], []byte("\n")), bytes.Replace(valid[preparedName], []byte("\n"), []byte("\r\n"), 1), append(append([]byte(nil), valid[preparedName]...), 0)} {
		candidate := cloneV3Records(valid)
		candidate[preparedName] = mutation
		if _, err := ParseV3(candidate); err == nil {
			t.Fatal("accepted invalid envelope")
		}
	}
	unknown := cloneV3Records(valid)
	unknown["061.unknown.record"] = []byte("x")
	if _, err := ParseV3(unknown); err == nil {
		t.Fatal("accepted unknown segment")
	}
}

func TestV3RecoveryRehashedAttackMatrix(t *testing.T) {
	base, attempted := v3RecordsTo(t, V3AdmissionAttempted)
	recovery, _, err := v3RecoveryRecordBytes(v3RecoveryFields{JournalID: v3H("journal"), AttemptID: "attempt-a", TransactionID: v3H("tx"),
		NonceID: v3H("nonce"), ReasonCode: "consumer_outcome_ambiguous", OccurredAtEpoch: 1700001000,
		PreviousState: V3AdmissionAttempted, PreviousRecordSHA256: attempted.HeadSHA256()}, 9)
	if err != nil {
		t.Fatal(err)
	}
	base[v3Segments[V3RecoveryRequired]] = recovery
	attacks := []struct{ key, value string }{
		{"transaction_id", v3H("other-tx")}, {"nonce_id", v3H("other-nonce")},
		{"reason_code", "claim_expired_before_attempt"}, {"reason_code", "unknown_reason"},
		{"occurred_at_epoch", "+1700001000"}, {"previous_state", string(V3AdmissionReserved)},
		{"previous_record_sha256", v3H("other-head")}, {"record", "transition"},
	}
	for _, attack := range attacks {
		candidate := cloneV3Records(base)
		candidate[v3Segments[V3RecoveryRequired]] = v3RehashField(t, recovery, attack.key, attack.value, "recovery-required")
		if _, err := ParseV3(candidate); err == nil {
			t.Fatalf("accepted recovery attack %s=%s", attack.key, attack.value)
		}
	}
}

func TestV3CanonicalGolden(t *testing.T) {
	prepared, digest, err := v3PreparedRecordBytes(v3PreparedFields{JournalID: v3H("journal"), AttemptID: "attempt-a",
		ReleaseContractCoreSHA256: v3H("core"), ControllerSHA256: v3H("controller"), CreatedAtEpoch: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	const wantDigest = "18075affa5838b9a996648056fd283bd09d1398578e908572dd989107ccc2a0f"
	const wantManifest = "8cbcb1abed3c875e7165647c135adc53f712b5d6d3d35cb77c1af07537fcfac4"
	if digest != wantDigest {
		t.Fatalf("prepared golden digest=%s len=%d", digest, len(prepared))
	}
	snapshot, err := ParseV3(map[string][]byte{v3Segments[V3Prepared]: prepared})
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := snapshot.ManifestBytes()
	if err != nil {
		t.Fatal(err)
	}
	const wantManifestBytes = "format=pandora-release-journal-manifest-v3\njournal_format=pandora-release-journal-v3\n" +
		"journal_namespace=client-auth-00042-v2\njournal_id=81dd6b775afcccb6dbb8a25a58ea844271bbefaeea7cb1d91c1687d7450f850c\n" +
		"attempt_id=attempt-a\nrecord_count=1\nhead_sequence=0\nhead_state=PREPARED\n" +
		"head_record_sha256=18075affa5838b9a996648056fd283bd09d1398578e908572dd989107ccc2a0f\n" +
		"segment=000.prepared.record\nsegment_sha256=4a0e5ccc8137b5dc4d4fe7d3911592beba2b132c163deff09784443cce3b0bb4\n"
	if string(manifestBytes) != wantManifestBytes {
		t.Fatalf("manifest bytes changed: %q", manifestBytes)
	}
	if snapshot.ManifestSHA256() != wantManifest {
		t.Fatalf("prepared manifest golden=%s", snapshot.ManifestSHA256())
	}
}

func TestV3AllRecordDomainAndTerminalManifestGoldens(t *testing.T) {
	records, committed := v3RecordsTo(t, V3Committed)
	attemptedRecords, attempted := v3RecordsTo(t, V3AdmissionAttempted)
	recovery, _, err := v3RecoveryRecordBytes(v3RecoveryFields{JournalID: v3H("journal"), AttemptID: "attempt-a", TransactionID: v3H("tx"),
		NonceID: v3H("nonce"), ReasonCode: "consumer_outcome_ambiguous", OccurredAtEpoch: 1700001000,
		PreviousState: V3AdmissionAttempted, PreviousRecordSHA256: attempted.HeadSHA256()}, 9)
	if err != nil {
		t.Fatal(err)
	}
	attemptedRecords[v3Segments[V3RecoveryRequired]] = recovery
	recovered, err := ParseV3(attemptedRecords)
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]string{
		"generic":   v3RecordDigest(records[v3Segments[V3IsolationAttempted]]),
		"reserved":  v3RecordDigest(records[v3Segments[V3AdmissionReserved]]),
		"attempted": v3RecordDigest(records[v3Segments[V3AdmissionAttempted]]),
		"verified":  v3RecordDigest(records[v3Segments[V3AdmissionVerified]]),
		"committed": v3RecordDigest(records[v3Segments[V3Committed]]),
		"recovery":  v3RecordDigest(recovery), "full_manifest": committed.ManifestSHA256(), "recovery_manifest": recovered.ManifestSHA256(),
	}
	expected := map[string]string{
		"generic":           "17825b4363a3502c98f598474edf324d2a0c50ed3214bc3d90365fb74f3ffe45",
		"reserved":          "79713fac538db84311281b09ddf92e305f36801dae9bb4596690d35aa2b86709",
		"attempted":         "dfc446412a32ad4736ce982f481bb13fa771466f6ecbd4fbdef289e07c70406f",
		"verified":          "206418817d6cca335eb671819c99d182cf636060e0240d70f794741774ba3c96",
		"committed":         "b35457f07200442d715d83f9917be5999a7c442ecb8d3e542499722e39ba573c",
		"recovery":          "2d691244b044efe7b689159dfe50ce0b998001a3b16d892fd44b4d10422c0251",
		"full_manifest":     "1b603e8b6dca103102e3dc54f835d5ce4f441927e10a9cf2283df79ec9b92be5",
		"recovery_manifest": "d121af938f3e9c1e7b8c85820a0f2a0079ee491ec2ee9a6e2cd80eb006db6a68",
	}
	var mismatches []string
	for key, want := range expected {
		if actual[key] != want {
			mismatches = append(mismatches, key+"="+actual[key])
		}
	}
	if len(mismatches) != 0 {
		t.Fatalf("goldens: %s", strings.Join(mismatches, " "))
	}
}

func v3RecordsTo(t *testing.T, terminal V3State) (map[string][]byte, V3Snapshot) {
	t.Helper()
	prepared, _, err := v3PreparedRecordBytes(v3PreparedFields{JournalID: v3H("journal"), AttemptID: "attempt-a", ReleaseContractCoreSHA256: v3H("core"), ControllerSHA256: v3H("controller"), CreatedAtEpoch: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	records := map[string][]byte{v3Segments[V3Prepared]: prepared}
	snapshot, err := ParseV3(records)
	if err != nil {
		t.Fatal(err)
	}
	for snapshot.State() != terminal {
		sequence := snapshot.Sequence() + 1
		next := v3NormalOrder[sequence]
		var data []byte
		switch next {
		case V3AdmissionReserved:
			data, _, err = v3ReservedRecordBytes(v3ReservedFields{JournalID: v3H("journal"), AttemptID: "attempt-a", TransactionID: v3H("tx"), NonceID: v3H("nonce"), ProfileSHA256: ca42executionv2.RequiredProfileSHA256, ClaimSHA256: v3H("claim"), ClaimCanonicalSHA256: v3H("claim-canonical"), ArtifactSetBindingSHA256: v3H("set"), ReleaseID: "release-a", ReleaseRunID: "run-a", Architecture: "amd64", BoundaryHeadSHA256: snapshot.HeadSHA256(), BoundaryManifestSHA256: snapshot.ManifestSHA256(), NonceReservationSHA256: v3H("reservation"), LedgerBeforeSHA256: v3H("ledger-before"), LedgerPlannedSHA256: v3H("ledger-planned"), AuthorityDescriptorSHA256: v3H("authority"), AuthorityEpoch: 2, AuthoritySequence: 2, EffectiveNotBeforeEpoch: 1699999990, EffectiveNotAfterEpoch: 1700001000, ReservedAtEpoch: 1700000100, PreviousRecordSHA256: snapshot.HeadSHA256()})
		case V3AdmissionAttempted:
			data, _, err = v3AttemptedRecordBytes(v3AttemptedFields{JournalID: v3H("journal"), AttemptID: "attempt-a", TransactionID: v3H("tx"), NonceID: v3H("nonce"), NonceReservationSHA256: v3H("reservation"), ConsumerOperationID: v3H("operation"), ConsumerInputSHA256: v3H("input"), InventoryManifestSHA256: v3H("inventory"), AttemptedAtEpoch: 1700000200, PreviousRecordSHA256: snapshot.HeadSHA256()})
		case V3AdmissionVerified:
			data, _, err = v3VerifiedRecordBytes(v3VerifiedFields{JournalID: v3H("journal"), AttemptID: "attempt-a", TransactionID: v3H("tx"), NonceID: v3H("nonce"), NonceReservationSHA256: v3H("reservation"), ConsumerOperationID: v3H("operation"), ConsumerReceiptSHA256: v3H("receipt"), ConsumerReceiptSize: 4096, ConsumerCompletedAtEpoch: 1700000300, LedgerAfterSHA256: v3H("ledger-planned"), VerifiedAtEpoch: 1700000400, PreviousRecordSHA256: snapshot.HeadSHA256()})
		default:
			occurred := int64(1700000000 + sequence)
			if sequence >= 10 {
				occurred = int64(1700000500 + sequence)
			}
			data, _, err = v3GenericRecordBytes(v3GenericFields{JournalID: v3H("journal"), AttemptID: "attempt-a", Sequence: sequence, EventID: v3H("event-" + string(next)), OccurredAtEpoch: occurred, PreviousState: snapshot.State(), State: next, PreviousRecordSHA256: snapshot.HeadSHA256(), EvidenceSHA256: v3H("evidence-" + string(next)), EvidenceSize: 1})
		}
		if err != nil {
			t.Fatal(err)
		}
		records[v3Segments[next]] = data
		snapshot, err = ParseV3(records)
		if err != nil {
			t.Fatal(err)
		}
	}
	return records, snapshot
}

func v3H(value string) string { return SHA256Bytes([]byte(value)) }
func cloneV3Records(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for name, data := range in {
		out[name] = append([]byte(nil), data...)
	}
	return out
}
func v3RecordDigest(data []byte) string {
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	return strings.TrimPrefix(lines[len(lines)-1], "record_sha256=")
}
func v3KindForName(name string) string {
	switch name {
	case v3Segments[V3Prepared]:
		return "prepared"
	case v3Segments[V3AdmissionReserved]:
		return "admission-reserved"
	case v3Segments[V3AdmissionAttempted]:
		return "admission-attempted"
	case v3Segments[V3AdmissionVerified]:
		return "admission-verified"
	case v3Segments[V3RecoveryRequired]:
		return "recovery-required"
	default:
		return "transition"
	}
}
func v3RehashField(t *testing.T, data []byte, key, value, kind string) []byte {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	found := false
	for i := range lines {
		if strings.HasPrefix(lines[i], key+"=") {
			lines[i] = key + "=" + value
			found = true
		}
	}
	if !found {
		t.Fatalf("field %s missing", key)
	}
	body := strings.Join(lines[:len(lines)-1], "\n") + "\n"
	return []byte(body + "record_sha256=" + v3RecordSHA(kind, []byte(body)) + "\n")
}
