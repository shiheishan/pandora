//go:build linux && (amd64 || arm64)

package ca42nonce

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func nonceStoreFields(label string) reservationFields {
	h := func(value string) string {
		if value == "zero" {
			return strings.Repeat("0", 64)
		}
		return domainSHA256([]byte(value))
	}
	return reservationFields{
		NonceID: h("nonce"), TransactionID: h("transaction-" + label),
		ClaimFormat: "client-auth-00042-consumption-claim-v2", ClaimSHA256: h("claim-" + label), ClaimCanonicalSHA256: h("canonical-" + label),
		ArtifactSetFormat: "client-auth-00042-artifact-set-v3", ArtifactSetBindingSHA256: h("set-" + label), ProfileSHA256: h("profile"),
		ReleaseID: "release-a", ReleaseRunID: "run-a", AttemptID: "attempt-a", Architecture: "amd64",
		JournalID: h("journal"), JournalBoundaryHeadSHA256: h("head"), JournalBoundaryManifestSHA256: h("manifest"),
		LedgerBeforeSHA256: h("zero"), LedgerPlannedSHA256: h("planned"), AuthorityDescriptorSHA256: h("authority"),
		AuthorityEpoch: 1, AuthoritySequence: 1, EffectiveNotBeforeEpoch: 1700000000, EffectiveNotAfterEpoch: 1700001000, ReservedAtEpoch: 1700000100,
	}
}

func nonceCommittedFields(fields reservationFields) committedFields {
	return committedFields{JournalVerifiedHeadSHA256: domainSHA256([]byte("verified-head")), JournalVerifiedManifestSHA256: domainSHA256([]byte("verified-manifest")),
		LedgerAfterSHA256: fields.LedgerPlannedSHA256, ConsumerOperationID: domainSHA256([]byte("operation")), ConsumerReceiptSHA256: domainSHA256([]byte("receipt")),
		ConsumerReceiptSize: 128, ConsumerCompletedAtEpoch: 1700000200, CommittedAtEpoch: 1700000300}
}

func nonceRecoveryFields() recoveryFields {
	return recoveryFields{ReasonCode: "consumer_outcome_ambiguous", OccurredAtEpoch: 1700000400}
}

func openNonceStoreFixture(t *testing.T, hooks nonceStoreHooks) (*retainedStore, *os.File, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixture")
	}
	rootPath := filepath.Join(t.TempDir(), "nonce-root")
	if err := os.Mkdir(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRetainedStore(root, hooks)
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	return store, root, rootPath
}

func TestRetainedStoreContentionIsNonblockingAndClassified(t *testing.T) {
	store, root, _ := openNonceStoreFixture(t, nonceStoreHooks{})
	defer root.Close()
	contender, err := openRetainedStore(root, nonceStoreHooks{})
	if contender != nil {
		_ = contender.close()
		t.Fatal("competing nonce store acquired exclusive root lock")
	}
	if !errors.Is(err, ErrStoreBusy) {
		t.Fatalf("nonce contention was not classified: %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openRetainedStore(root, nonceStoreHooks{})
	if err != nil {
		t.Fatalf("nonce store did not recover after unlock: %v", err)
	}
	if err := reopened.close(); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedStoreIncompleteDirectoryPermanentlyFencesNonce(t *testing.T) {
	store, root, rootPath := openNonceStoreFixture(t, nonceStoreHooks{afterDirectoryCreate: func(string) error {
		return syscall.EIO
	}})
	defer root.Close()
	fields := nonceStoreFields("a")
	if session, _, err := store.reserve(fields); session != nil || !errors.Is(err, syscall.EIO) {
		t.Fatalf("setup did not leave the intended incomplete directory: session=%v err=%v", session, err)
	}
	store.hooks = nonceStoreHooks{}
	if session, _, err := store.reserve(fields); session != nil || !errors.Is(err, ErrReservationIncomplete) {
		t.Fatalf("exact ordinary retry claimed incomplete directory: session=%v err=%v", session, err)
	}
	divergent := fields
	divergent.ClaimSHA256 = domainSHA256([]byte("different-claim"))
	if session, _, err := store.reserve(divergent); session != nil || !errors.Is(err, ErrReservationIncomplete) {
		t.Fatalf("divergent ordinary retry claimed incomplete directory: session=%v err=%v", session, err)
	}
	if session, err := store.openExisting(fields.NonceID, fields.TransactionID); session != nil || !errors.Is(err, ErrReservationIncomplete) {
		t.Fatalf("incomplete directory opened as a reservation: session=%v err=%v", session, err)
	}
	differentTransaction := fields
	differentTransaction.TransactionID = domainSHA256([]byte("different-transaction"))
	if session, _, err := store.reserve(differentTransaction); session != nil || err == nil || !strings.Contains(err.Error(), "replay transaction conflict") {
		t.Fatalf("incomplete nonce did not fence another transaction: session=%v err=%v", session, err)
	}
	entries, err := os.ReadDir(filepath.Join(rootPath, nonceDirectoryName(fields.NonceID, fields.TransactionID)))
	if err != nil || len(entries) != 0 {
		t.Fatalf("incomplete fenced inventory mutated: entries=%v err=%v", entries, err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedStoreCreateRechecksInventoryBeforePublishing(t *testing.T) {
	var rootPath string
	store, root, fixturePath := openNonceStoreFixture(t, nonceStoreHooks{afterDirectoryCreate: func(name string) error {
		return os.WriteFile(filepath.Join(rootPath, name, "evil.record"), []byte("foreign inventory"), 0600)
	}})
	rootPath = fixturePath
	defer root.Close()
	defer store.close()

	fields := nonceStoreFields("a")
	session, recovered, err := store.reserve(fields)
	if session != nil || recovered || err == nil || !strings.Contains(err.Error(), "inventory changed") {
		t.Fatalf("foreign create-hook inventory was not rejected before publication: session=%v recovered=%v err=%v", session, recovered, err)
	}
	entries, readErr := os.ReadDir(filepath.Join(rootPath, nonceDirectoryName(fields.NonceID, fields.TransactionID)))
	if readErr != nil || len(entries) != 1 || entries[0].Name() != "evil.record" {
		t.Fatalf("failed create mutated foreign inventory: entries=%v err=%v", entries, readErr)
	}
}

func TestRetainedStoreReserveCreateExactRetryAndReplay(t *testing.T) {
	store, root, rootPath := openNonceStoreFixture(t, nonceStoreHooks{})
	fields := nonceStoreFields("a")
	session, recovered, err := store.reserve(fields)
	if err != nil || recovered {
		t.Fatalf("first reserve recovered=%v err=%v", recovered, err)
	}
	snapshot, err := session.inspect()
	if err != nil || snapshot.State() != Reserved || snapshot.NonceID() != fields.NonceID || snapshot.TransactionID() != fields.TransactionID {
		t.Fatalf("first snapshot=%+v err=%v", snapshot, err)
	}
	head := snapshot.HeadSHA256()
	if err := session.close(); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	root.Close()

	root, err = os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store, err = openRetainedStore(root, nonceStoreHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	session, recovered, err = store.reserve(fields)
	if err != nil || !recovered {
		t.Fatalf("retry recovered=%v err=%v", recovered, err)
	}
	defer session.close()
	replayed, err := session.inspect()
	if err != nil || replayed.HeadSHA256() != head {
		t.Fatalf("retry head=%q want=%q err=%v", replayed.HeadSHA256(), head, err)
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil || len(entries) != 1 {
		t.Fatalf("root inventory=%d err=%v", len(entries), err)
	}
	records, err := os.ReadDir(filepath.Join(rootPath, entries[0].Name()))
	if err != nil || len(records) != 1 || records[0].Name() != ReservedRecordName {
		t.Fatalf("record inventory=%v err=%v", records, err)
	}
}

func TestRetainedStoreCloseIsBusyWhileSessionAuthorityIsLive(t *testing.T) {
	store, root, _ := openNonceStoreFixture(t, nonceStoreHooks{})
	defer root.Close()
	session, _, err := store.reserve(nonceStoreFields("a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.close(); !errors.Is(err, ErrStoreBusy) {
		t.Fatalf("store close released global authority while child session was live: %v", err)
	}
	if err := session.close(); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("store close did not succeed after child authority closed: %v", err)
	}
}

func TestRetainedStoreDivergentRetryConflictsWithoutMutation(t *testing.T) {
	store, root, rootPath := openNonceStoreFixture(t, nonceStoreHooks{})
	defer root.Close()
	defer store.close()
	base := nonceStoreFields("a")
	session, _, err := store.reserve(base)
	if err != nil {
		t.Fatal(err)
	}
	before, err := session.inspect()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.close(); err != nil {
		t.Fatal(err)
	}

	mutations := []func(*reservationFields){
		func(f *reservationFields) { f.ClaimSHA256 = domainSHA256([]byte("different-claim")) },
		func(f *reservationFields) { f.ArtifactSetBindingSHA256 = domainSHA256([]byte("different-set")) },
		func(f *reservationFields) { f.JournalBoundaryHeadSHA256 = domainSHA256([]byte("different-head")) },
		func(f *reservationFields) { f.LedgerPlannedSHA256 = domainSHA256([]byte("different-ledger")) },
		func(f *reservationFields) { f.EffectiveNotAfterEpoch++ },
	}
	for index, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		if conflicting, _, err := store.reserve(candidate); err == nil {
			if conflicting != nil {
				_ = conflicting.close()
			}
			t.Fatalf("mutation %d accepted", index)
		}
	}
	differentTx := base
	differentTx.TransactionID = domainSHA256([]byte("different-transaction"))
	if _, _, err := store.reserve(differentTx); err == nil {
		t.Fatal("different transaction accepted")
	}
	reopened, err := store.openExisting(base.NonceID, base.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	after, err := reopened.inspect()
	if err != nil || after.HeadSHA256() != before.HeadSHA256() {
		t.Fatalf("winner mutated before=%q after=%q err=%v", before.HeadSHA256(), after.HeadSHA256(), err)
	}
	entries, _ := os.ReadDir(rootPath)
	if len(entries) != 1 {
		t.Fatalf("unexpected root inventory: %d", len(entries))
	}
}

func TestRetainedStorePostLinkFailureReconcilesExactPublication(t *testing.T) {
	store, root, _ := openNonceStoreFixture(t, nonceStoreHooks{afterRecordLink: func(name string) error {
		return errors.New("injected post-link failure")
	}})
	defer root.Close()
	defer store.close()
	session, recovered, err := store.reserve(nonceStoreFields("a"))
	if err != nil || !recovered {
		t.Fatalf("post-link recovery=%v err=%v", recovered, err)
	}
	defer session.close()
	if snapshot, err := session.inspect(); err != nil || snapshot.State() != Reserved {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestRetainedStoreExactRetryReconcilesPostFsyncHookFailure(t *testing.T) {
	store, root, rootPath := openNonceStoreFixture(t, nonceStoreHooks{})
	fields := nonceStoreFields("a")
	session, _, err := store.reserve(fields)
	if err != nil {
		t.Fatal(err)
	}
	_ = session.close()
	_ = store.close()
	_ = root.Close()

	root, err = os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store, err = openRetainedStore(root, nonceStoreHooks{afterDirectorySync: func(operation string) error {
		if operation == ReservedRecordName || operation == CommittedRecordName {
			return errors.New("injected post-fsync reporting failure")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	session, recovered, err := store.reserve(fields)
	if err != nil || !recovered {
		t.Fatalf("reservation exact repair recovered=%v err=%v", recovered, err)
	}
	commit := nonceCommittedFields(fields)
	if _, _, err := session.commit(commit); err != nil {
		t.Fatal(err)
	}
	if _, recovered, err := session.commit(commit); err != nil || !recovered {
		t.Fatalf("terminal exact repair recovered=%v err=%v", recovered, err)
	}
	_ = session.close()
}

func TestRetainedStoreRejectsUnknownInventorySymlinkAndModes(t *testing.T) {
	store, root, rootPath := openNonceStoreFixture(t, nonceStoreHooks{})
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "unknown"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openRetainedStore(root, nonceStoreHooks{}); err == nil {
		t.Fatal("unknown root entry accepted")
	}
	root.Close()

	badRoot := filepath.Join(t.TempDir(), "bad-root")
	if err := os.Mkdir(badRoot, 0755); err != nil {
		t.Fatal(err)
	}
	bad, err := os.Open(badRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	if _, err := openRetainedStore(bad, nonceStoreHooks{}); err == nil {
		t.Fatal("0755 root accepted")
	}
}

func TestRetainedStoreSessionSharedCloseLeaseAndTerminalFork(t *testing.T) {
	store, root, _ := openNonceStoreFixture(t, nonceStoreHooks{})
	defer root.Close()
	defer store.close()
	fields := nonceStoreFields("a")
	session, _, err := store.reserve(fields)
	if err != nil {
		t.Fatal(err)
	}
	copySession := *session
	if err := session.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := copySession.inspect(); err == nil {
		t.Fatal("shallow-copy session remained authoritative after close")
	}
	if err := copySession.close(); err != nil {
		t.Fatal(err)
	}

	session, err = store.openExisting(fields.NonceID, fields.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	commit := nonceCommittedFields(fields)
	committed, _, err := session.commit(commit)
	if err != nil || committed.State() != Committed {
		t.Fatalf("commit state=%s err=%v", committed.State(), err)
	}
	retried, recovered, err := session.commit(commit)
	if err != nil || !recovered || retried.HeadSHA256() != committed.HeadSHA256() {
		t.Fatalf("terminal exact retry recovered=%v state=%s err=%v", recovered, retried.State(), err)
	}
	divergentCommit := commit
	divergentCommit.ConsumerReceiptSHA256 = domainSHA256([]byte("different-receipt"))
	if _, _, err := session.commit(divergentCommit); err == nil {
		t.Fatal("divergent committed retry accepted")
	}
	if _, _, err := session.recover(nonceRecoveryFields()); err == nil {
		t.Fatal("terminal fork accepted")
	}
}

func TestRetainedStoreDescriptorsAreCLOEXEC(t *testing.T) {
	store, root, _ := openNonceStoreFixture(t, nonceStoreHooks{})
	defer root.Close()
	defer store.close()
	session, _, err := store.reserve(nonceStoreFields("a"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	for _, fd := range []int{store.rootFD, session.dirFD} {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
		if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
			t.Fatalf("fd %d CLOEXEC flags=%x errno=%v", fd, flags, errno)
		}
	}
}

func TestRetainedStoreSharedCloseLeaseRejectsShallowCopy(t *testing.T) {
	store, root, _ := openNonceStoreFixture(t, nonceStoreHooks{})
	defer root.Close()
	copyStore := *store
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := copyStore.reserve(nonceStoreFields("a")); err == nil {
		t.Fatal("shallow-copy store remained authoritative after close")
	}
	if err := copyStore.close(); err != nil {
		t.Fatal(err)
	}
}
