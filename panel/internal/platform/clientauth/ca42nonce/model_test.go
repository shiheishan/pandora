package ca42nonce

import (
	"bytes"
	"strings"
	"testing"
)

func TestReservationCommittedAndRecoveryStateMachine(t *testing.T) {
	reserved, reservationSHA := reservationFixture(t)
	reservedReceipt, err := Parse(map[string][]byte{ReservedRecordName: reserved})
	if err != nil || reservedReceipt.State() != Reserved || reservedReceipt.HeadSHA256() != reservationSHA {
		t.Fatalf("reserved parse got=%+v err=%v", reservedReceipt, err)
	}
	committed, committedSHA, err := committedRecordBytes(committedFields{
		NonceID: sha("1"), TransactionID: sha("2"), ReservationSHA256: reservationSHA,
		JournalVerifiedHeadSHA256: sha("e"), JournalVerifiedManifestSHA256: sha("f"),
		LedgerAfterSHA256: sha("d"), ConsumerOperationID: sha("7"), ConsumerReceiptSHA256: sha("8"),
		ConsumerReceiptSize: 4096, ConsumerCompletedAtEpoch: 1700000190, CommittedAtEpoch: 1700000200,
	})
	if err != nil {
		t.Fatal(err)
	}
	committedReceipt, err := Parse(map[string][]byte{ReservedRecordName: reserved, CommittedRecordName: committed})
	if err != nil || committedReceipt.State() != Committed || committedReceipt.HeadSHA256() != committedSHA {
		t.Fatalf("committed parse got=%+v err=%v", committedReceipt, err)
	}
	recovery, recoverySHA, err := recoveryRecordBytes(recoveryFields{
		NonceID: sha("1"), TransactionID: sha("2"), ReservationSHA256: reservationSHA,
		ReasonCode: "claim_expired_before_attempt", OccurredAtEpoch: 1700003400,
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveryReceipt, err := Parse(map[string][]byte{ReservedRecordName: reserved, RecoveryRecordName: recovery})
	if err != nil || recoveryReceipt.State() != RecoveryRequired || recoveryReceipt.HeadSHA256() != recoverySHA {
		t.Fatalf("recovery parse got=%+v err=%v", recoveryReceipt, err)
	}
	if _, err := Parse(map[string][]byte{ReservedRecordName: reserved, CommittedRecordName: committed, RecoveryRecordName: recovery}); err == nil {
		t.Fatal("terminal fork accepted")
	}
}

func TestReceiptOwnsRecordsAndRevalidatesIdentity(t *testing.T) {
	reserved, _ := reservationFixture(t)
	caller := append([]byte(nil), reserved...)
	receipt, err := Parse(map[string][]byte{ReservedRecordName: caller})
	if err != nil {
		t.Fatal(err)
	}
	caller[0] ^= 0xff
	verified, err := receipt.VerifiedCopy()
	if err != nil || verified.NonceID() != sha("1") || verified.TransactionID() != sha("2") {
		t.Fatalf("caller mutation changed receipt: %+v %v", verified, err)
	}
	receipt.records[ReservedRecordName][0] ^= 0xff
	if _, err := receipt.VerifiedCopy(); err == nil {
		t.Fatal("mutated receipt capability accepted")
	}
	if _, err := (Snapshot{}).VerifiedCopy(); err == nil {
		t.Fatal("zero receipt capability accepted")
	}
}

func TestParserFailsClosedOnInventoryBindingAndCanonicalMutations(t *testing.T) {
	reserved, reservationSHA := reservationFixture(t)
	committed, _, err := committedRecordBytes(committedFields{
		NonceID: sha("1"), TransactionID: sha("2"), ReservationSHA256: reservationSHA,
		JournalVerifiedHeadSHA256: sha("e"), JournalVerifiedManifestSHA256: sha("f"),
		LedgerAfterSHA256: sha("d"), ConsumerOperationID: sha("7"), ConsumerReceiptSHA256: sha("8"),
		ConsumerReceiptSize: 1, ConsumerCompletedAtEpoch: 1700000190, CommittedAtEpoch: 1700000200,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		records map[string][]byte
	}{
		{"empty", nil},
		{"terminal_only", map[string][]byte{CommittedRecordName: committed}},
		{"unknown", map[string][]byte{ReservedRecordName: reserved, "evil.record": committed}},
		{"wrong_transaction", map[string][]byte{ReservedRecordName: reserved, CommittedRecordName: replaceCanonical(t, committed, "transaction_id="+sha("2"), "transaction_id="+sha("9"))}},
		{"wrong_ledger", map[string][]byte{ReservedRecordName: reserved, CommittedRecordName: replaceCanonical(t, committed, "ledger_after_sha256="+sha("d"), "ledger_after_sha256="+sha("9"))}},
		{"crlf", map[string][]byte{ReservedRecordName: bytes.ReplaceAll(reserved, []byte("\n"), []byte("\r\n"))}},
		{"truncated", map[string][]byte{ReservedRecordName: reserved[:len(reserved)-1]}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.records); err == nil {
				t.Fatal("invalid nonce record set accepted")
			}
		})
	}
}

func TestReservationWindowAndGenesisLedgerRules(t *testing.T) {
	base := reservationFieldsFixture()
	for _, mutate := range []func(*reservationFields){
		func(f *reservationFields) { f.ReservedAtEpoch = f.EffectiveNotBeforeEpoch - 1 },
		func(f *reservationFields) { f.ReservedAtEpoch = f.EffectiveNotAfterEpoch },
		func(f *reservationFields) { f.EffectiveNotAfterEpoch = f.EffectiveNotBeforeEpoch },
		func(f *reservationFields) { f.LedgerBeforeSHA256 = "" },
		func(f *reservationFields) { f.LedgerPlannedSHA256 = strings.Repeat("0", 64) },
	} {
		candidate := base
		mutate(&candidate)
		if _, _, err := reservationRecordBytes(candidate); err == nil {
			t.Fatal("invalid reservation window or ledger identity accepted")
		}
	}
	base.LedgerBeforeSHA256 = strings.Repeat("0", 64)
	if _, _, err := reservationRecordBytes(base); err != nil {
		t.Fatalf("explicit genesis ledger identity rejected: %v", err)
	}
	base.AuthorityEpoch = 2
	if _, _, err := reservationRecordBytes(base); err == nil {
		t.Fatal("zero ledger before accepted outside authority genesis")
	}
	base = reservationFieldsFixture()
	base.LedgerBeforeSHA256 = sha("9")
	if _, _, err := reservationRecordBytes(base); err == nil {
		t.Fatal("nonzero ledger before accepted for authority genesis")
	}
}

func TestExactClaimAndArtifactVersionsAreRequired(t *testing.T) {
	for _, mutate := range []func(*reservationFields){
		func(f *reservationFields) { f.ClaimFormat = "client-auth-00042-consumption-claim-v1" },
		func(f *reservationFields) { f.ClaimFormat = "unknown-claim-v9" },
		func(f *reservationFields) { f.ArtifactSetFormat = "client-auth-00042-artifact-set-v2" },
		func(f *reservationFields) { f.ArtifactSetFormat = "unknown-set-v9" },
	} {
		candidate := reservationFieldsFixture()
		mutate(&candidate)
		if _, _, err := reservationRecordBytes(candidate); err == nil {
			t.Fatal("legacy or unknown claim/artifact format accepted")
		}
	}
}

func TestTerminalTimeBindingsAndPostExpiryMetadataRepair(t *testing.T) {
	reserved, reservationSHA := reservationFixture(t)
	committed := func(completedAt, committedAt int64) []byte {
		data, _, err := committedRecordBytes(committedFields{
			NonceID: sha("1"), TransactionID: sha("2"), ReservationSHA256: reservationSHA,
			JournalVerifiedHeadSHA256: sha("e"), JournalVerifiedManifestSHA256: sha("f"), LedgerAfterSHA256: sha("d"),
			ConsumerOperationID: sha("7"), ConsumerReceiptSHA256: sha("8"), ConsumerReceiptSize: 1,
			ConsumerCompletedAtEpoch: completedAt, CommittedAtEpoch: committedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, test := range []struct {
		name                     string
		completedAt, committedAt int64
		wantValid                bool
	}{
		{"before_reservation", 1700000149, 1700000200, false},
		{"completion_at_expiry", 1700003400, 1700003500, false},
		{"metadata_repair_after_expiry", 1700003399, 1700003500, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(map[string][]byte{ReservedRecordName: reserved, CommittedRecordName: committed(test.completedAt, test.committedAt)})
			if (err == nil) != test.wantValid {
				t.Fatalf("terminal time validity got err=%v wantValid=%v", err, test.wantValid)
			}
		})
	}
	valid := committed(1700000200, 1700000200)
	backward := replaceCanonical(t, valid, "committed_at_epoch=1700000200", "committed_at_epoch=1700000199")
	if _, err := Parse(map[string][]byte{ReservedRecordName: reserved, CommittedRecordName: backward}); err == nil {
		t.Fatal("metadata timestamp before consumer completion accepted")
	}
	for _, occurredAt := range []int64{1700000149, 1700000150, 1700003500} {
		recovery, _, err := recoveryRecordBytes(recoveryFields{NonceID: sha("1"), TransactionID: sha("2"),
			ReservationSHA256: reservationSHA, ReasonCode: "trusted_clock_rollback", OccurredAtEpoch: occurredAt})
		if err != nil {
			t.Fatal(err)
		}
		_, parseErr := Parse(map[string][]byte{ReservedRecordName: reserved, RecoveryRecordName: recovery})
		if (parseErr == nil) != (occurredAt >= 1700000150) {
			t.Fatalf("recovery occurred_at=%d got err=%v", occurredAt, parseErr)
		}
	}
	earlyExpiry, _, err := recoveryRecordBytes(recoveryFields{NonceID: sha("1"), TransactionID: sha("2"),
		ReservationSHA256: reservationSHA, ReasonCode: "claim_expired_before_attempt", OccurredAtEpoch: 1700003399})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(map[string][]byte{ReservedRecordName: reserved, RecoveryRecordName: earlyExpiry}); err == nil {
		t.Fatal("claim-expired recovery reason accepted before effective expiry")
	}
}

func TestCanonicalRecordGoldenVectors(t *testing.T) {
	reserved, reservationSHA := reservationFixture(t)
	committed, committedSHA, err := committedRecordBytes(committedFields{
		NonceID: sha("1"), TransactionID: sha("2"), ReservationSHA256: reservationSHA,
		JournalVerifiedHeadSHA256: sha("e"), JournalVerifiedManifestSHA256: sha("f"), LedgerAfterSHA256: sha("d"),
		ConsumerOperationID: sha("7"), ConsumerReceiptSHA256: sha("8"), ConsumerReceiptSize: 4096,
		ConsumerCompletedAtEpoch: 1700000190, CommittedAtEpoch: 1700000200,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovery, recoverySHA, err := recoveryRecordBytes(recoveryFields{
		NonceID: sha("1"), TransactionID: sha("2"), ReservationSHA256: reservationSHA,
		ReasonCode: "claim_expired_before_attempt", OccurredAtEpoch: 1700003400,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reservationSHA != "8a0f7086cf9cd59d285866947bdc419f535d81f0f2bf5b467028ab380cc07eb3" ||
		committedSHA != "aa390b80f75d898b1cf6de0643769597e4f3c4ed4d4419a61d983c595aa3ab95" ||
		recoverySHA != "2ac4191a085d9c05fca0ee85fe8599b901ad95df94c89cbea0ca87a566a1222c" {
		t.Fatalf("goldens reserved=%s committed=%s recovery=%s lengths=%d/%d/%d",
			reservationSHA, committedSHA, recoverySHA, len(reserved), len(committed), len(recovery))
	}
}

func TestTerminalEnumsAndReceiptBoundsFailClosed(t *testing.T) {
	_, reservationSHA := reservationFixture(t)
	committed := committedFields{
		NonceID: sha("1"), TransactionID: sha("2"), ReservationSHA256: reservationSHA,
		JournalVerifiedHeadSHA256: sha("e"), JournalVerifiedManifestSHA256: sha("f"), LedgerAfterSHA256: sha("d"),
		ConsumerOperationID: sha("7"), ConsumerReceiptSHA256: sha("8"), ConsumerReceiptSize: MaxConsumerReceiptBytes,
		ConsumerCompletedAtEpoch: 1700000190, CommittedAtEpoch: 1700000200,
	}
	if _, _, err := committedRecordBytes(committed); err != nil {
		t.Fatalf("maximum bounded consumer receipt rejected: %v", err)
	}
	committed.ConsumerReceiptSize++
	if _, _, err := committedRecordBytes(committed); err == nil {
		t.Fatal("oversized consumer receipt accepted")
	}
	if _, _, err := recoveryRecordBytes(recoveryFields{NonceID: sha("1"), TransactionID: sha("2"),
		ReservationSHA256: reservationSHA, ReasonCode: "caller_invented_reason", OccurredAtEpoch: 1700000200}); err == nil {
		t.Fatal("unknown recovery reason accepted")
	}
}

func TestParserEnvelopeAndCanonicalIntegerMatrix(t *testing.T) {
	reserved, _ := reservationFixture(t)
	oversized := bytes.Repeat([]byte("a"), MaxRecordBytes+1)
	oversized[len(oversized)-1] = '\n'
	nul := append([]byte(nil), reserved...)
	nul[10] = 0
	invalidUTF8 := append([]byte(nil), reserved...)
	invalidUTF8[10] = 0xff
	doubleLF := append(append([]byte(nil), reserved...), '\n')
	uppercaseSHA := append([]byte(nil), reserved...)
	lastLine := bytes.LastIndex(uppercaseSHA[:len(uppercaseSHA)-1], []byte("record_sha256="))
	uppercaseSHA[lastLine+len("record_sha256=")] = 'A'
	nonCanonicalEpoch := replaceCanonical(t, reserved, "reserved_at_epoch=1700000150", "reserved_at_epoch=01700000150")
	for name, data := range map[string][]byte{
		"oversized": oversized, "nul": nul, "invalid_utf8": invalidUTF8, "double_lf": doubleLF,
		"uppercase_sha": uppercaseSHA, "noncanonical_epoch": nonCanonicalEpoch,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(map[string][]byte{ReservedRecordName: data}); err == nil {
				t.Fatal("invalid record envelope accepted")
			}
		})
	}
}

func TestPublicParserRejectsRehashedSemanticAttacks(t *testing.T) {
	reserved, reservationSHA := reservationFixture(t)
	committed, _, err := committedRecordBytes(committedFields{
		NonceID: sha("1"), TransactionID: sha("2"), ReservationSHA256: reservationSHA,
		JournalVerifiedHeadSHA256: sha("e"), JournalVerifiedManifestSHA256: sha("f"), LedgerAfterSHA256: sha("d"),
		ConsumerOperationID: sha("7"), ConsumerReceiptSHA256: sha("8"), ConsumerReceiptSize: 1,
		ConsumerCompletedAtEpoch: 1700000190, CommittedAtEpoch: 1700000200,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovery, _, err := recoveryRecordBytes(recoveryFields{NonceID: sha("1"), TransactionID: sha("2"),
		ReservationSHA256: reservationSHA, ReasonCode: "trusted_clock_rollback", OccurredAtEpoch: 1700000200})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		records map[string][]byte
	}{
		{"claim_v1", map[string][]byte{ReservedRecordName: replaceCanonical(t, reserved,
			"claim_format=client-auth-00042-consumption-claim-v2", "claim_format=client-auth-00042-consumption-claim-v1")}},
		{"set_v2", map[string][]byte{ReservedRecordName: replaceCanonical(t, reserved,
			"artifact_set_format=client-auth-00042-artifact-set-v3", "artifact_set_format=client-auth-00042-artifact-set-v2")}},
		{"non_genesis_epoch_with_zero_before", map[string][]byte{ReservedRecordName: replaceCanonical(t, reserved,
			"authority_epoch=1", "authority_epoch=2")}},
		{"oversized_receipt", map[string][]byte{ReservedRecordName: reserved, CommittedRecordName: replaceCanonical(t, committed,
			"consumer_receipt_size=1", "consumer_receipt_size=1048577")}},
		{"unknown_recovery_reason", map[string][]byte{ReservedRecordName: reserved, RecoveryRecordName: replaceCanonical(t, recovery,
			"reason_code=trusted_clock_rollback", "reason_code=caller_invented_reason")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.records); err == nil {
				t.Fatal("rehashed semantic attack accepted by public parser")
			}
		})
	}
}

func TestTerminalSnapshotsOwnCallerMapAndBuffers(t *testing.T) {
	reserved, reservationSHA := reservationFixture(t)
	committed, _, err := committedRecordBytes(committedFields{
		NonceID: sha("1"), TransactionID: sha("2"), ReservationSHA256: reservationSHA,
		JournalVerifiedHeadSHA256: sha("e"), JournalVerifiedManifestSHA256: sha("f"), LedgerAfterSHA256: sha("d"),
		ConsumerOperationID: sha("7"), ConsumerReceiptSHA256: sha("8"), ConsumerReceiptSize: 1,
		ConsumerCompletedAtEpoch: 1700000190, CommittedAtEpoch: 1700000200,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovery, _, err := recoveryRecordBytes(recoveryFields{NonceID: sha("1"), TransactionID: sha("2"),
		ReservationSHA256: reservationSHA, ReasonCode: "trusted_clock_rollback", OccurredAtEpoch: 1700000200})
	if err != nil {
		t.Fatal(err)
	}
	for name, terminal := range map[string][]byte{CommittedRecordName: committed, RecoveryRecordName: recovery} {
		t.Run(name, func(t *testing.T) {
			callerReserved, callerTerminal := append([]byte(nil), reserved...), append([]byte(nil), terminal...)
			callerMap := map[string][]byte{ReservedRecordName: callerReserved, name: callerTerminal}
			snapshot, err := Parse(callerMap)
			if err != nil {
				t.Fatal(err)
			}
			callerReserved[0], callerTerminal[0] = 0xff, 0xff
			delete(callerMap, name)
			if _, err := snapshot.VerifiedCopy(); err != nil {
				t.Fatalf("caller map or terminal mutation changed snapshot: %v", err)
			}
		})
	}
}

func TestRecoveryReasonInventoryIsExact(t *testing.T) {
	want := []string{"claim_expired_before_attempt", "consumer_outcome_ambiguous", "cross_store_state_divergent", "durability_outcome_ambiguous", "trusted_clock_rollback"}
	if len(allowedRecoveryReasons) != len(want) {
		t.Fatalf("recovery reason inventory count got=%d want=%d", len(allowedRecoveryReasons), len(want))
	}
	for _, reason := range want {
		if !allowedRecoveryReasons[reason] {
			t.Fatalf("recovery reason inventory omitted %s", reason)
		}
	}
}

func reservationFixture(t *testing.T) ([]byte, string) {
	t.Helper()
	data, digest, err := reservationRecordBytes(reservationFieldsFixture())
	if err != nil {
		t.Fatal(err)
	}
	return data, digest
}

func reservationFieldsFixture() reservationFields {
	return reservationFields{
		NonceID: sha("1"), TransactionID: sha("2"), ClaimFormat: "client-auth-00042-consumption-claim-v2",
		ClaimSHA256: sha("3"), ClaimCanonicalSHA256: sha("4"), ArtifactSetFormat: "client-auth-00042-artifact-set-v3",
		ArtifactSetBindingSHA256: sha("5"), ProfileSHA256: sha("6"), ReleaseID: "release-a", ReleaseRunID: "run-a",
		AttemptID: "attempt-a", Architecture: "amd64", JournalID: sha("a"), JournalBoundaryHeadSHA256: sha("b"),
		JournalBoundaryManifestSHA256: sha("c"), LedgerBeforeSHA256: strings.Repeat("0", 64), LedgerPlannedSHA256: sha("d"),
		AuthorityDescriptorSHA256: sha("e"), AuthorityEpoch: 1, AuthoritySequence: 1,
		EffectiveNotBeforeEpoch: 1700000100, EffectiveNotAfterEpoch: 1700003400, ReservedAtEpoch: 1700000150,
	}
}

func replaceCanonical(t *testing.T, data []byte, old, replacement string) []byte {
	t.Helper()
	mutated := bytes.Replace(data, []byte(old), []byte(replacement), 1)
	lines := strings.Split(strings.TrimSuffix(string(mutated), "\n"), "\n")
	body := strings.Join(lines[:len(lines)-1], "\n") + "\n"
	lines[len(lines)-1] = "record_sha256=" + domainSHA256([]byte(body))
	return []byte(strings.Join(lines, "\n") + "\n")
}

func sha(value string) string { return strings.Repeat(value, 64) }
