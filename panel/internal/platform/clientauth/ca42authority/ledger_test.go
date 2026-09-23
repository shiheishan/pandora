package ca42authority

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestAuthorityLedgerGenesisAdvanceAndExactRetry(t *testing.T) {
	fixture := newAuthorityFixture()
	now := time.Unix(1700000100, 0)
	first := mustDescriptor(t, fixture, nil, now)
	ledger, retry, err := Advance(nil, first, now)
	if err != nil || retry {
		t.Fatalf("genesis failed: retry=%v err=%v", retry, err)
	}
	if err := ledger.Validate(); err != nil {
		t.Fatal(err)
	}
	if encoded, err := ledger.CanonicalBytes(); err != nil || !strings.HasSuffix(string(encoded), "\n") {
		t.Fatalf("canonical ledger failed: bytes=%q err=%v", encoded, err)
	} else if decoded, err := ParseLedger(encoded); err != nil || decoded.RecordSHA256 != ledger.RecordSHA256 {
		t.Fatalf("ledger round trip failed: ledger=%#v err=%v", decoded, err)
	}
	if same, exact, err := Advance(&ledger, first, now.Add(time.Second)); err != nil || !exact || same.RecordSHA256 != ledger.RecordSHA256 {
		t.Fatalf("exact retry failed: exact=%v err=%v", exact, err)
	}
	second := mustDescriptor(t, fixture, func(values []string) {
		values[8] = "2"
		values[9] = hex.EncodeToString(first.SHA256[:])
		values[13] = "release-ca42-2"
		values[14] = "run-ca42-2"
		values[15] = "attempt-ca42-2"
		values[16] = strings.Repeat("d", 64)
	}, now)
	advanced, exact, err := Advance(&ledger, second, now.Add(2*time.Second))
	if err != nil || exact || advanced.AuthoritySequence != 2 || advanced.PreviousDescriptorSHA != first.SHA256 {
		t.Fatalf("advance failed: ledger=%#v exact=%v err=%v", advanced, exact, err)
	}
}

func TestParseLedgerRejectsNoncanonicalAndTamperedData(t *testing.T) {
	fixture := newAuthorityFixture()
	now := time.Unix(1700000100, 0)
	descriptor := mustDescriptor(t, fixture, nil, now)
	ledger, _, err := Advance(nil, descriptor, now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := ledger.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range [][]byte{
		encoded[:len(encoded)-1],
		[]byte(strings.Replace(string(encoded), "authority_epoch=1", "authority_epoch=01", 1)),
		[]byte(strings.Replace(string(encoded), "last_attempt_id=attempt-ca42-1", "last_attempt_id=other", 1)),
		append([]byte{0xef, 0xbb, 0xbf}, encoded...),
	} {
		if result, err := ParseLedger(candidate); err == nil || result.LedgerID != "" {
			t.Fatalf("accepted invalid ledger: result=%#v err=%v", result, err)
		}
	}
}

func TestAuthorityLedgerRejectsRollbackDivergenceAndReplay(t *testing.T) {
	fixture := newAuthorityFixture()
	now := time.Unix(1700000100, 0)
	first := mustDescriptor(t, fixture, nil, now)
	ledger, _, err := Advance(nil, first, now)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func([]string)
	}{
		{"divergent same sequence", func(v []string) { v[16] = strings.Repeat("e", 64) }},
		{"sequence gap", func(v []string) {
			v[8] = "3"
			v[9] = hex.EncodeToString(first.SHA256[:])
			v[15] = "attempt-gap"
			v[16] = strings.Repeat("e", 64)
		}},
		{"previous hash mismatch", func(v []string) {
			v[8] = "2"
			v[9] = strings.Repeat("f", 64)
			v[15] = "attempt-prev"
			v[16] = strings.Repeat("e", 64)
		}},
		{"attempt replay", func(v []string) {
			v[8] = "2"
			v[9] = hex.EncodeToString(first.SHA256[:])
			v[16] = strings.Repeat("e", 64)
		}},
		{"manifest replay", func(v []string) { v[8] = "2"; v[9] = hex.EncodeToString(first.SHA256[:]); v[15] = "attempt-new" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := mustDescriptor(t, fixture, test.mutate, now)
			if _, _, err := Advance(&ledger, candidate, now.Add(time.Second)); err == nil {
				t.Fatal("accepted authority ledger drift")
			}
		})
	}
	second := mustDescriptor(t, fixture, func(v []string) {
		v[8] = "2"
		v[9] = hex.EncodeToString(first.SHA256[:])
		v[13], v[14], v[15], v[16] = "release-2", "run-2", "attempt-2", strings.Repeat("e", 64)
	}, now)
	if _, _, err := Advance(&ledger, second, time.Unix(1700000099, 0)); err == nil {
		t.Fatal("accepted trusted clock rollback")
	}
}

func TestAuthorityLedgerRecoveryRequiresHigherEpochAndCurrentHead(t *testing.T) {
	fixture := newAuthorityFixture()
	now := time.Unix(1700000100, 0)
	first := mustDescriptor(t, fixture, nil, now)
	ledger, _, err := Advance(nil, first, now)
	if err != nil {
		t.Fatal(err)
	}
	recovery := mustDescriptor(t, fixture, func(v []string) {
		v[7], v[8], v[9], v[10] = "3", "1", hex.EncodeToString(first.SHA256[:]), ModeRecovery
		v[13], v[14], v[15], v[16] = "release-recovery", "run-recovery", "attempt-recovery", strings.Repeat("f", 64)
	}, now)
	if result, retry, err := Advance(&ledger, recovery, now.Add(time.Second)); err != nil || retry || result.AuthorityEpoch != 3 {
		t.Fatalf("recovery advance failed: result=%#v retry=%v err=%v", result, retry, err)
	}
}

func TestReserveTransitionRequiresExactRereadSnapshot(t *testing.T) {
	fixture := newAuthorityFixture()
	now := time.Unix(1700000100, 0)
	first := mustDescriptor(t, fixture, nil, now)
	current, _, err := Advance(nil, first, now)
	if err != nil {
		t.Fatal(err)
	}
	second := mustDescriptor(t, fixture, func(v []string) {
		v[8] = "2"
		v[9] = hex.EncodeToString(first.SHA256[:])
		v[13], v[14], v[15], v[16] = "release-cas", "run-cas", "attempt-cas", strings.Repeat("e", 64)
	}, now)
	stale := current
	stale.RecordSHA256[0] ^= 0xff
	if _, _, err := ReserveTransition(&stale, &current, second, now.Add(time.Second)); err == nil {
		t.Fatal("accepted stale ledger CAS snapshot")
	}
	if next, retry, err := ReserveTransition(&current, &current, second, now.Add(time.Second)); err != nil || retry || next.AuthoritySequence != 2 {
		t.Fatalf("exact ledger CAS failed: next=%#v retry=%v err=%v", next, retry, err)
	}
	if _, _, err := ReserveTransition(nil, &current, second, now.Add(time.Second)); err == nil {
		t.Fatal("accepted unexpected pre-existing ledger")
	}
}

func mustDescriptor(t *testing.T, fixture authorityFixture, mutate func([]string), now time.Time) Descriptor {
	t.Helper()
	data := fixture.descriptor(t, mutate)
	result, err := ParseAndVerify(data, fixture.roots, "amd64", fixture.host, now)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
