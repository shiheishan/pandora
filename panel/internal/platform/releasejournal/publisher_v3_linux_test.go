//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestV3PublisherGenericCreateExactRetryAndDivergence(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, prepared, name := writeV3JournalFixture(t, policy, V3Prepared)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, prepared.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	checkpoint, err := session.mutationCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	intent := v3GenericIntent{target: V3IsolationAttempted, eventID: v3H("publisher-event"), evidenceSHA256: v3H("publisher-evidence"), occurredAtEpoch: 1700000001, evidenceSize: 1}
	first, recovered, err := session.advanceGenericV3(checkpoint, intent)
	if err != nil || recovered {
		t.Fatalf("first publish: recovered=%v err=%v", recovered, err)
	}
	if first.State() != V3IsolationAttempted || first.Sequence() != 1 {
		t.Fatalf("wrong published state: %s/%d", first.State(), first.Sequence())
	}
	path := filepath.Join(policy.root, name, v3Segments[V3IsolationAttempted])
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0400 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 {
		t.Fatalf("published identity invalid: mode=%o uid=%d gid=%d nlink=%d", info.Mode().Perm(), stat.Uid, stat.Gid, stat.Nlink)
	}
	again, recovered, err := session.advanceGenericV3(checkpoint, intent)
	if err != nil || !recovered || again.HeadSHA256() != first.HeadSHA256() || again.ManifestSHA256() != first.ManifestSHA256() {
		t.Fatalf("exact retry did not converge: recovered=%v err=%v", recovered, err)
	}
	divergent := intent
	divergent.eventID = v3H("divergent-event")
	if _, _, err := session.advanceGenericV3(checkpoint, divergent); err == nil {
		t.Fatal("divergent retry accepted")
	}
	final, err := session.Inspect()
	if err != nil || final.HeadSHA256() != first.HeadSHA256() || final.ManifestSHA256() != first.ManifestSHA256() {
		t.Fatal("divergent retry mutated journal")
	}
}

func TestV3PublisherPostLinkSyncFailureRecoversExact(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, prepared, _ := writeV3JournalFixture(t, policy, V3Prepared)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, prepared.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	checkpoint, err := session.mutationCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	intent := v3GenericIntent{target: V3IsolationAttempted, eventID: v3H("post-link-event"), evidenceSHA256: v3H("post-link-evidence"), occurredAtEpoch: 1700000001, evidenceSize: 1}
	realFsync := releaseFsync
	t.Cleanup(func() { releaseFsync = realFsync })
	releaseFsync = func(fd int) error {
		if fd == session.journalFD {
			return syscall.EIO
		}
		return realFsync(fd)
	}
	if _, _, err := session.advanceGenericV3(checkpoint, intent); err == nil || !strings.Contains(err.Error(), "commit_ambiguous") {
		t.Fatalf("post-link durability failure not ambiguous: %v", err)
	}
	releaseFsync = realFsync
	visible, err := session.Inspect()
	if err != nil || visible.State() != V3IsolationAttempted {
		t.Fatalf("linked record not replayable after injected sync failure: %v", err)
	}
	recovered, exact, err := session.advanceGenericV3(checkpoint, intent)
	if err != nil || !exact || recovered.HeadSHA256() != visible.HeadSHA256() || recovered.ManifestSHA256() != visible.ManifestSHA256() {
		t.Fatalf("exact recovery failed: exact=%v err=%v", exact, err)
	}
}

func TestV3PublisherRejectsStaleAndForeignCheckpoint(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, prepared, _ := writeV3JournalFixture(t, policy, V3Prepared)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, prepared.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	checkpoint, err := session.mutationCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	intent := v3GenericIntent{target: V3IsolationAttempted, eventID: v3H("foreign-event"), evidenceSHA256: v3H("foreign-evidence"), occurredAtEpoch: 1700000001, evidenceSize: 1}
	foreign := checkpoint
	foreign.journalIno++
	if _, _, err := session.advanceGenericV3(foreign, intent); err == nil {
		t.Fatal("foreign checkpoint accepted")
	}
	if _, _, err := session.advanceGenericV3(checkpoint, intent); err != nil {
		t.Fatal(err)
	}
	staleNext := v3GenericIntent{target: V3Isolated, eventID: v3H("stale-event"), evidenceSHA256: v3H("stale-evidence"), occurredAtEpoch: 1700000002, evidenceSize: 1}
	if _, _, err := session.advanceGenericV3(checkpoint, staleNext); err == nil {
		t.Fatal("stale checkpoint advanced a second state")
	}
}

func TestV3PublisherGenericPrefixZeroThroughSix(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, prepared, _ := writeV3JournalFixture(t, policy, V3Prepared)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, prepared.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for sequence := uint64(1); sequence <= 6; sequence++ {
		target := v3NormalOrder[sequence]
		checkpoint, err := session.mutationCheckpoint()
		if err != nil {
			t.Fatal(err)
		}
		intent := v3GenericIntent{target: target, eventID: v3H("event-" + string(target)), evidenceSHA256: v3H("evidence-" + string(target)), occurredAtEpoch: int64(1700000000 + sequence), evidenceSize: 1}
		published, recovered, err := session.advanceGenericV3(checkpoint, intent)
		if err != nil || recovered {
			t.Fatalf("sequence %d publish: recovered=%v err=%v", sequence, recovered, err)
		}
		_, golden := v3RecordsTo(t, target)
		if published.HeadSHA256() != golden.HeadSHA256() || published.ManifestSHA256() != golden.ManifestSHA256() {
			t.Fatalf("sequence %d differs from portable golden", sequence)
		}
		retry, recovered, err := session.advanceGenericV3(checkpoint, intent)
		if err != nil || !recovered || retry.HeadSHA256() != published.HeadSHA256() {
			t.Fatalf("sequence %d exact retry: recovered=%v err=%v", sequence, recovered, err)
		}
	}
	checkpoint, err := session.mutationCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.advanceGenericV3(checkpoint, v3GenericIntent{target: V3AdmissionReserved}); err == nil {
		t.Fatal("generic publisher crossed retained admission boundary")
	}
}

func TestV3PublisherBasenameReplacementIsCommitAmbiguousAndReplacementUntouched(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, prepared, name := writeV3JournalFixture(t, policy, V3Prepared)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, prepared.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	checkpoint, err := session.mutationCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	realLink := v3LinkAnonymous
	t.Cleanup(func() { v3LinkAnonymous = realLink })
	originalPath := filepath.Join(policy.root, name)
	movedPath := originalPath + ".moved"
	v3LinkAnonymous = func(fd, dirFD int, segment string) error {
		if err := realLink(fd, dirFD, segment); err != nil {
			return err
		}
		if err := os.Rename(originalPath, movedPath); err != nil {
			return err
		}
		return os.Mkdir(originalPath, 0700)
	}
	intent := v3GenericIntent{target: V3IsolationAttempted, eventID: v3H("replace-event"), evidenceSHA256: v3H("replace-evidence"), occurredAtEpoch: 1700000001, evidenceSize: 1}
	if _, _, err := session.advanceGenericV3(checkpoint, intent); err == nil || !strings.Contains(err.Error(), "commit_ambiguous") {
		t.Fatalf("basename replacement was not ambiguous: %v", err)
	}
	entries, err := os.ReadDir(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("replacement journal directory was written")
	}
	movedRecords, err := os.ReadDir(movedPath)
	if err != nil || len(movedRecords) != 2 {
		t.Fatalf("retained original did not receive exactly one append: entries=%d err=%v", len(movedRecords), err)
	}
}

func TestV3PublisherLinkErrorAfterCommitRecoversExact(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, prepared, _ := writeV3JournalFixture(t, policy, V3Prepared)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, prepared.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	checkpoint, err := session.mutationCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	realLink := v3LinkAnonymous
	t.Cleanup(func() { v3LinkAnonymous = realLink })
	v3LinkAnonymous = func(fd, dirFD int, segment string) error {
		if err := realLink(fd, dirFD, segment); err != nil {
			return err
		}
		return syscall.EIO
	}
	intent := v3GenericIntent{target: V3IsolationAttempted, eventID: v3H("link-error-event"), evidenceSHA256: v3H("link-error-evidence"), occurredAtEpoch: 1700000001, evidenceSize: 1}
	snapshot, recovered, err := session.advanceGenericV3(checkpoint, intent)
	if err != nil || !recovered || snapshot.State() != V3IsolationAttempted {
		t.Fatalf("post-commit link error did not exact-recover: recovered=%v err=%v", recovered, err)
	}
}

func TestV3PublisherEEXISTExactMustRepairDurability(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, prepared, _ := writeV3JournalFixture(t, policy, V3Prepared)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, prepared.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	checkpoint, err := session.mutationCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	realLink := v3LinkAnonymous
	realFsync := releaseFsync
	t.Cleanup(func() { v3LinkAnonymous, releaseFsync = realLink, realFsync })
	v3LinkAnonymous = func(fd, dirFD int, segment string) error {
		if err := realLink(fd, dirFD, segment); err != nil {
			return err
		}
		return syscall.EEXIST
	}
	releaseFsync = func(fd int) error {
		if fd == session.journalFD {
			return syscall.EIO
		}
		return realFsync(fd)
	}
	intent := v3GenericIntent{target: V3IsolationAttempted, eventID: v3H("eexist-event"), evidenceSHA256: v3H("eexist-evidence"), occurredAtEpoch: 1700000001, evidenceSize: 1}
	if _, _, err := session.advanceGenericV3(checkpoint, intent); err == nil || !strings.Contains(err.Error(), "exact_recovery_sync_failed") {
		t.Fatalf("EEXIST exact recovery skipped durability repair: %v", err)
	}
}
