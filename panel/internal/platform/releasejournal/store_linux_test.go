//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func linuxJournalFixture(t *testing.T) (pathPolicy, preparedIdentity) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("Linux root required")
	}
	fixtureBase := os.Getenv("PANDORA_RELEASEJOURNAL_TEST_ROOT")
	if fixtureBase == "" {
		fixtureBase = "/root"
	}
	if !filepath.IsAbs(fixtureBase) || filepath.Clean(fixtureBase) != fixtureBase || (fixtureBase != "/root" && !strings.HasPrefix(fixtureBase, "/root/")) {
		t.Fatalf("release journal test root must be canonical beneath /root: %q", fixtureBase)
	}
	baseInfo, err := os.Lstat(fixtureBase)
	if err != nil || !baseInfo.IsDir() {
		t.Fatalf("invalid release journal test root %q: %v", fixtureBase, err)
	}
	baseStat, ok := baseInfo.Sys().(*syscall.Stat_t)
	if !ok || baseStat.Uid != 0 || baseStat.Gid != 0 || baseInfo.Mode().Perm()&0077 != 0 {
		t.Fatalf("release journal test root must be root-owned and private: %q", fixtureBase)
	}
	root, err := os.MkdirTemp(fixtureBase, "pandora-release-journal-test.")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	allowed := map[uint64]struct{}{}
	for _, path := range []string{"/", fixtureBase, root} {
		var stat syscall.Stat_t
		if err := syscall.Stat(path, &stat); err != nil {
			t.Fatal(err)
		}
		allowed[uint64(stat.Dev)] = struct{}{}
	}
	var rootStat syscall.Stat_t
	if err := syscall.Stat(root, &rootStat); err != nil {
		t.Fatal(err)
	}
	identity := testPreparedIdentity()
	identity.Architecture = runtime.GOARCH
	identity.TargetRootDevice = strconv.FormatUint(uint64(rootStat.Dev), 10)
	identity.TargetRootInode = strconv.FormatUint(rootStat.Ino, 10)
	return pathPolicy{root: root, expectedDevice: uint64(rootStat.Dev), allowedDevices: allowed}, identity
}

func fixtureSnapshot(t *testing.T, policy pathPolicy, attempt string) (string, releaseJournalSnapshot) {
	t.Helper()
	rootFD, rootStat, err := openTrustedRoot(policy)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(rootFD)
	name, err := discoverJournal(rootFD, attempt, policy.allowedDevices)
	if err != nil {
		t.Fatal(err)
	}
	journalFD, journalStat, err := openJournalDirectory(rootFD, name, uint64(rootStat.Dev), policy.allowedDevices)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(journalFD)
	snapshot, err := loadSnapshot(journalFD, uint64(journalStat.Dev))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJournalName(name, snapshot); err != nil {
		t.Fatal(err)
	}
	return name, snapshot
}

func TestPrepareExactRetryIdentityConflictAndBasenameBinding(t *testing.T) {
	policy, identity := linuxJournalFixture(t)
	if err := prepareJournal(policy, identity); err != nil {
		t.Fatal(err)
	}
	name, first := fixtureSnapshot(t, policy, identity.ReleaseAttemptID)
	if err := prepareJournal(policy, identity); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	_, second := fixtureSnapshot(t, policy, identity.ReleaseAttemptID)
	if first.ManifestSHA != second.ManifestSHA {
		t.Fatal("prepare retry changed journal")
	}
	conflict := identity
	conflict.ReleaseID = "different-release"
	if err := prepareJournal(policy, conflict); err == nil {
		t.Fatal("identity conflict accepted")
	}
	if err := validateJournalName("other."+first.Prepared.JournalID+".release-journal", first); err == nil {
		t.Fatal("attempt basename mismatch accepted")
	}
	if err := validateJournalName(identity.ReleaseAttemptID+"."+testHash+".release-journal", first); err == nil {
		t.Fatal("journal id basename mismatch accepted")
	}
	if name == "" {
		t.Fatal("empty journal name")
	}
}

func TestAdvanceCASExactRetryDivergenceAndPostRenameRecovery(t *testing.T) {
	policy, identity := linuxJournalFixture(t)
	if err := prepareJournal(policy, identity); err != nil {
		t.Fatal(err)
	}
	_, Prepared := fixtureSnapshot(t, policy, identity.ReleaseAttemptID)
	evidence := evidenceDigest{sha256: testHash, size: 1}
	EventID := sha256Bytes([]byte("isolation-attempt"))
	originalFsync := releaseFsync
	t.Cleanup(func() { releaseFsync = originalFsync })
	count := 0
	releaseFsync = func(fd int) error {
		count++
		if count == 6 {
			return syscall.EIO
		}
		return originalFsync(fd)
	}
	err := advanceJournal(policy, identity.ReleaseAttemptID, Prepared.ManifestSHA, statePrepared, stateIsolationAttempted, EventID, "1700000001", evidence)
	if err == nil {
		t.Fatal("injected post-rename fsync failure not observed")
	}
	_, published := fixtureSnapshot(t, policy, identity.ReleaseAttemptID)
	if published.State != stateIsolationAttempted {
		t.Fatalf("published state=%s", published.State)
	}
	if err := advanceJournal(policy, identity.ReleaseAttemptID, Prepared.ManifestSHA, statePrepared, stateIsolationAttempted, EventID, "1700000001", evidence); err != nil {
		t.Fatalf("exact recovery: %v", err)
	}
	_, recovered := fixtureSnapshot(t, policy, identity.ReleaseAttemptID)
	if recovered.ManifestSHA != published.ManifestSHA {
		t.Fatal("recovery changed journal")
	}
	divergent := sha256Bytes([]byte("different-event"))
	if err := advanceJournal(policy, identity.ReleaseAttemptID, Prepared.ManifestSHA, statePrepared, stateIsolationAttempted, divergent, "1700000001", evidence); err == nil {
		t.Fatal("divergent retry accepted")
	}
}

func TestStageGrammarAndUnknownRootEntryFailClosed(t *testing.T) {
	policy, identity := linuxJournalFixture(t)
	if err := os.WriteFile(policy.root+"/.pandora-release-record.nothex.stage", []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	rootFD, _, err := openTrustedRoot(policy)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(rootFD)
	_, err = discoverJournal(rootFD, identity.ReleaseAttemptID, policy.allowedDevices)
	var denied *policyError
	if err == nil || !errors.As(err, &denied) || denied.reason != "unknown_entry_in_journal_root" {
		t.Fatalf("unexpected result: %v", err)
	}
}

func TestTransitionStageNameIsolatedByAttempt(t *testing.T) {
	EventID := sha256Bytes([]byte("shared-event"))
	left := transitionStageName(releaseJournalFormat, "attempt-a", EventID)
	right := transitionStageName(releaseJournalFormat, "attempt-b", EventID)
	if left == right {
		t.Fatal("stage name is not isolated by attempt")
	}
	if !recordStageRE.MatchString(left) || !recordStageRE.MatchString(right) {
		t.Fatal("stage name grammar invalid")
	}
}

func TestStageDomainsSeparateV1AndV2(t *testing.T) {
	eventID := sha256Bytes([]byte("shared-event"))
	if journalStageID(releaseJournalFormat, "attempt-a") == journalStageID(releaseJournalFormatV2, "attempt-a") {
		t.Fatal("journal stage domains are not version separated")
	}
	if transitionStageName(releaseJournalFormat, "attempt-a", eventID) == transitionStageName(releaseJournalFormatV2, "attempt-a", eventID) {
		t.Fatal("record stage domains are not version separated")
	}
	journalIDs := map[string]bool{journalStageID(releaseJournalFormat, "attempt-a"): true, journalStageID(releaseJournalFormatV2, "attempt-a"): true, journalStageID(FormatV3, "attempt-a"): true}
	recordIDs := map[string]bool{transitionStageName(releaseJournalFormat, "attempt-a", eventID): true, transitionStageName(releaseJournalFormatV2, "attempt-a", eventID): true, transitionStageName(FormatV3, "attempt-a", eventID): true}
	if len(journalIDs) != 3 || len(recordIDs) != 3 {
		t.Fatal("v1/v2/v3 stage domains are not pairwise separated")
	}
}

func TestStrictTemporaryResidueDoesNotBlockOtherAttempt(t *testing.T) {
	policy, identity := linuxJournalFixture(t)
	directoryTemp := policy.root + "/.pandora-release-journal." + strings.Repeat("a", 32) + ".tmp"
	if err := os.Mkdir(directoryTemp, 0700); err != nil {
		t.Fatal(err)
	}
	fileTemp := policy.root + "/.pandora-release-record." + strings.Repeat("b", 32) + ".tmp"
	if err := os.WriteFile(fileTemp, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	rootFD, _, err := openTrustedRoot(policy)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(rootFD)
	_, err = discoverJournal(rootFD, identity.ReleaseAttemptID, policy.allowedDevices)
	var denied *policyError
	if err == nil || !errors.As(err, &denied) || denied.reason != "release_journal_not_found" {
		t.Fatalf("strict temp residue blocked discovery: %v", err)
	}
}

func TestV2SessionRetainsDescriptorsAndAdvancesFixedAdmission(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, boundary := writeV2JournalFixture(t, policy, stateLayoutSwitched)
	defer root.Close()
	session, err := OpenV2Session(root, boundary.Prepared.ReleaseAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	receipt, err := session.InspectLayoutSwitched()
	if err != nil {
		t.Fatal(err)
	}
	evidence := admissionEvidenceFixture(t)
	defer evidence.Close()
	baseline, err := InspectAdmissionEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	advanced, recovered, err := session.AdvanceAdmissionAttempted(receipt, evidence, baseline, time.Unix(1700000100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if recovered || advanced.CurrentState() != AdmissionAttempted || advanced.CurrentSequence() != 7 ||
		advanced.LayoutSwitchedJournalSHA256() != receipt.CurrentJournalSHA256() {
		t.Fatalf("unexpected admission result: recovered=%v state=%s sequence=%d", recovered, advanced.CurrentState(), advanced.CurrentSequence())
	}
	retried, recovered, err := session.AdvanceAdmissionAttempted(receipt, evidence, baseline, time.Unix(1700000200, 0).UTC())
	if err != nil || !recovered || retried.CurrentJournalSHA256() != advanced.CurrentJournalSHA256() {
		t.Fatalf("exact retry failed: recovered=%v err=%v", recovered, err)
	}
	divergentEvidence := admissionEvidenceFixtureWithContent(t, "different-evidence\n")
	defer divergentEvidence.Close()
	divergentBaseline, err := InspectAdmissionEvidence(divergentEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.AdvanceAdmissionAttempted(receipt, divergentEvidence, divergentBaseline, time.Unix(1700000200, 0).UTC()); err == nil || !strings.Contains(err.Error(), "exact_retry_record_mismatch") {
		t.Fatalf("divergent exact retry was not rejected by record identity: %v", err)
	}
}

func TestV2SessionRejectsBasenameReplacementAndDoesNotWriteReplacement(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, boundary := writeV2JournalFixture(t, policy, stateLayoutSwitched)
	defer root.Close()
	session, err := OpenV2Session(root, boundary.Prepared.ReleaseAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	receipt, err := session.InspectLayoutSwitched()
	if err != nil {
		t.Fatal(err)
	}
	originalName := boundary.Prepared.ReleaseAttemptID + "." + boundary.Prepared.JournalID + ".release-journal"
	movedName := "retained-old." + boundary.Prepared.JournalID + ".release-journal"
	if err := os.Rename(policy.root+"/"+originalName, policy.root+"/"+movedName); err != nil {
		t.Fatal(err)
	}
	replacementSegments, replacement := v2SegmentsToState(t, LayoutSwitched)
	replacement.Prepared.ReleaseAttemptID = boundary.Prepared.ReleaseAttemptID
	if err := os.Mkdir(policy.root+"/"+originalName, 0700); err != nil {
		t.Fatal(err)
	}
	for segment, data := range replacementSegments {
		if err := os.WriteFile(policy.root+"/"+originalName+"/"+segment, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	evidence := admissionEvidenceFixture(t)
	defer evidence.Close()
	baseline, err := InspectAdmissionEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.AdvanceAdmissionAttempted(receipt, evidence, baseline, time.Unix(1700000100, 0).UTC()); err == nil {
		t.Fatal("retained session followed replacement basename")
	}
	if _, err := os.Stat(policy.root + "/" + originalName + "/" + stateSegment[stateAdmissionAttempted]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement journal was modified: %v, fixture=%s", err, replacement.ManifestSHA)
	}
}

func TestV2SessionRejectsEvidenceChangedAfterBaseline(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, boundary := writeV2JournalFixture(t, policy, stateLayoutSwitched)
	defer root.Close()
	session, err := OpenV2Session(root, boundary.Prepared.ReleaseAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	receipt, err := session.InspectLayoutSwitched()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(policy.root, "mutable-admission.evidence")
	writer, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.WriteString("approved\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	baseline, err := InspectAdmissionEvidence(reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("tampered\n"), 0); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.AdvanceAdmissionAttempted(receipt, reader, baseline, time.Unix(1700000100, 0).UTC()); err == nil {
		t.Fatal("evidence mutation after baseline was accepted")
	}
	current, err := session.InspectLayoutSwitched()
	if err != nil || current.CurrentJournalSHA256() != receipt.CurrentJournalSHA256() {
		t.Fatalf("evidence rejection changed journal: receipt=%v err=%v", current, err)
	}
}

func TestAdmissionEvidenceRejectsWritableDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writable.evidence")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0400)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("approved\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectAdmissionEvidence(file); err == nil || !strings.Contains(err.Error(), "evidence_fd_must_be_read_only") {
		t.Fatalf("writable evidence descriptor accepted: %v", err)
	}
}

func TestV2SessionExactRetryRechecksEvidenceAfterSync(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, boundary := writeV2JournalFixture(t, policy, stateLayoutSwitched)
	defer root.Close()
	session, err := OpenV2Session(root, boundary.Prepared.ReleaseAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	receipt, err := session.InspectLayoutSwitched()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(policy.root, "retry-mutable.evidence")
	writer, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.WriteString("approved\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	baseline, err := InspectAdmissionEvidence(reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.AdvanceAdmissionAttempted(receipt, reader, baseline, time.Unix(1700000100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	realFsync := releaseFsync
	t.Cleanup(func() { releaseFsync = realFsync })
	mutated := false
	releaseFsync = func(fd int) error {
		if !mutated {
			mutated = true
			if _, writeErr := writer.WriteAt([]byte("tampered\n"), 0); writeErr != nil {
				return writeErr
			}
			if syncErr := writer.Sync(); syncErr != nil {
				return syncErr
			}
		}
		return realFsync(fd)
	}
	if _, _, err := session.AdvanceAdmissionAttempted(receipt, reader, baseline, time.Unix(1700000200, 0).UTC()); err == nil || !strings.Contains(err.Error(), "admission_recovery_evidence_ambiguous") {
		t.Fatalf("exact retry evidence mutation was not reported as ambiguous: %v", err)
	}
	if !mutated {
		t.Fatal("recovery sync mutation hook was not reached")
	}
}

func TestNilReleaseJournalSessionMethodsFailClosed(t *testing.T) {
	var session *Session
	if _, err := session.Inspect(); err == nil {
		t.Fatal("nil session inspect accepted")
	}
	if _, _, err := session.AdvanceAdmissionAttempted(Receipt{}, nil, AdmissionEvidence{}, time.Now()); err == nil {
		t.Fatal("nil session advance accepted")
	}
}

func TestV2SessionReportsAmbiguousCommitWhenBasenameChangesDuringPublish(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, boundary := writeV2JournalFixture(t, policy, stateLayoutSwitched)
	defer root.Close()
	session, err := OpenV2Session(root, boundary.Prepared.ReleaseAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	receipt, err := session.InspectLayoutSwitched()
	if err != nil {
		t.Fatal(err)
	}
	evidence := admissionEvidenceFixture(t)
	defer evidence.Close()
	baseline, err := InspectAdmissionEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	originalName := boundary.Prepared.ReleaseAttemptID + "." + boundary.Prepared.JournalID + ".release-journal"
	movedName := "publish-race-old." + boundary.Prepared.JournalID + ".release-journal"
	replacementSegments, _ := v2SegmentsToState(t, LayoutSwitched)
	realRename := releaseRename
	changed := false
	releaseRename = func(oldDirFD int, oldName string, newDirFD int, newName string) error {
		if !changed && newName == stateSegment[stateAdmissionAttempted] {
			changed = true
			if renameErr := os.Rename(filepath.Join(policy.root, originalName), filepath.Join(policy.root, movedName)); renameErr != nil {
				return renameErr
			}
			if mkdirErr := os.Mkdir(filepath.Join(policy.root, originalName), 0700); mkdirErr != nil {
				return mkdirErr
			}
			for segment, data := range replacementSegments {
				if writeErr := os.WriteFile(filepath.Join(policy.root, originalName, segment), data, 0600); writeErr != nil {
					return writeErr
				}
			}
		}
		return realRename(oldDirFD, oldName, newDirFD, newName)
	}
	defer func() { releaseRename = realRename }()
	if _, _, err := session.AdvanceAdmissionAttempted(receipt, evidence, baseline, time.Unix(1700000100, 0).UTC()); err == nil || !strings.Contains(err.Error(), "admission_commit_binding_ambiguous") {
		t.Fatalf("publish basename race was not reported as ambiguous: %v", err)
	}
	if !changed {
		t.Fatal("publish race hook was not reached")
	}
	if _, err := os.Stat(filepath.Join(policy.root, originalName, stateSegment[stateAdmissionAttempted])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement journal was modified: %v", err)
	}
}

func TestV2SessionCloseIsIdempotentAndV1IsRejected(t *testing.T) {
	policy, identity := linuxJournalFixture(t)
	if err := prepareJournal(policy, identity); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(policy.root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if session, err := OpenV2Session(root, identity.ReleaseAttemptID); err == nil {
		session.Close()
		t.Fatal("v1 journal opened as a v2 session")
	}
	if err := os.RemoveAll(policy.root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(policy.root, 0700); err != nil {
		t.Fatal(err)
	}
	root.Close()
	root, boundary := writeV2JournalFixture(t, policy, stateLayoutSwitched)
	defer root.Close()
	session, err := OpenV2Session(root, boundary.Prepared.ReleaseAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Inspect(); err == nil {
		t.Fatal("closed session remained usable")
	}
}

func writeV2JournalFixture(t *testing.T, policy pathPolicy, terminal State) (*os.File, Snapshot) {
	t.Helper()
	segments, snapshot := v2SegmentsToState(t, terminal)
	name := snapshot.Prepared.ReleaseAttemptID + "." + snapshot.Prepared.JournalID + ".release-journal"
	directory := policy.root + "/" + name
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for segment, data := range segments {
		if err := os.WriteFile(directory+"/"+segment, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.Open(policy.root)
	if err != nil {
		t.Fatal(err)
	}
	return root, snapshot
}

func admissionEvidenceFixture(t *testing.T) *os.File {
	return admissionEvidenceFixtureWithContent(t, "ca42-admission-evidence-v1\n")
}

func admissionEvidenceFixtureWithContent(t *testing.T, content string) *os.File {
	t.Helper()
	file, err := os.CreateTemp("/root", "pandora-ca42-evidence.")
	if err != nil {
		t.Fatal(err)
	}
	name := file.Name()
	t.Cleanup(func() { _ = os.Remove(name) })
	if _, err := file.Write([]byte(content)); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Chmod(0400); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}
