//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"crypto/subtle"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const admissionEventDomainV2 = "PANDORA\x00CA42-JOURNAL-EVENT\x00V2\x00"

// Session retains the already-trusted journal root and exact journal directory
// descriptors, together with their advisory locks, across inspection and the
// admission CAS. It never resolves a caller-provided path.
type Session struct {
	mu          sync.Mutex
	rootFD      int
	journalFD   int
	rootStat    syscall.Stat_t
	journalStat syscall.Stat_t
	journalName string
	policy      pathPolicy
	closed      bool
}

// AdmissionEvidence is a descriptor-derived immutable projection. Its fields
// are private so a caller cannot assert a digest without presenting the file.
type AdmissionEvidence struct {
	sha256 string
	size   int64
}

func (e AdmissionEvidence) SHA256() string { return e.sha256 }
func (e AdmissionEvidence) Size() int64    { return e.size }

// OpenV2Session duplicates a trusted, root-owned 0700 journal-root descriptor
// and opens the sole v2 journal bound to attemptID. The caller remains owner of
// retainedRoot; Session owns only its duplicate and the journal descriptor.
func OpenV2Session(retainedRoot *os.File, attemptID string) (*Session, error) {
	if os.Geteuid() != 0 {
		return nil, deny("euid_zero_required")
	}
	if retainedRoot == nil || !safeTokenRE.MatchString(attemptID) {
		return nil, deny("session_identity_invalid")
	}
	originalFD := int(retainedRoot.Fd())
	if originalFD < 3 {
		return nil, deny("journal_root_fd_must_be_retained")
	}
	var original syscall.Stat_t
	if err := syscall.Fstat(originalFD, &original); err != nil {
		return nil, denyErr("journal_root_fstat_failed", err)
	}
	allowed := map[uint64]struct{}{uint64(original.Dev): {}}
	rootFD, err := syscall.Dup(originalFD)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(rootFD)
	closeRoot := true
	defer func() {
		if closeRoot {
			syscall.Close(rootFD)
		}
	}()
	rootStat, err := validateDirectoryFD(rootFD, allowed)
	if err != nil {
		return nil, err
	}
	if !sameFileIdentity(original, rootStat) || rootStat.Mode&07777 != 0700 {
		return nil, deny("journal_root_retained_identity_invalid")
	}
	if err := syscall.Flock(rootFD, syscall.LOCK_SH); err != nil {
		return nil, err
	}
	rootLocked := true
	defer func() {
		if rootLocked {
			_ = syscall.Flock(rootFD, syscall.LOCK_UN)
		}
	}()
	name, err := discoverJournal(rootFD, attemptID, allowed)
	if err != nil {
		return nil, err
	}
	journalFD, journalStat, err := openJournalDirectory(rootFD, name, uint64(rootStat.Dev), allowed)
	if err != nil {
		return nil, err
	}
	closeJournal := true
	defer func() {
		if closeJournal {
			syscall.Close(journalFD)
		}
	}()
	if err := syscall.Flock(journalFD, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, err
	}
	journalLocked := true
	defer func() {
		if journalLocked {
			_ = syscall.Flock(journalFD, syscall.LOCK_UN)
		}
	}()
	s := &Session{
		rootFD: rootFD, journalFD: journalFD, rootStat: rootStat,
		journalStat: journalStat, journalName: name,
		policy: pathPolicy{expectedDevice: uint64(rootStat.Dev), allowedDevices: allowed},
	}
	if err := s.validateRetainedBinding(); err != nil {
		return nil, err
	}
	snapshot, err := loadSnapshot(journalFD, uint64(journalStat.Dev))
	if err != nil {
		return nil, err
	}
	if snapshot.JournalFormat != releaseJournalFormatV2 || snapshot.Prepared.ReleaseAttemptID != attemptID {
		return nil, deny("journal_v2_required")
	}
	if _, err := receiptFromSnapshot(snapshot); err != nil {
		return nil, denyErr("journal_v2_receipt_invalid", err)
	}
	closeRoot, rootLocked, closeJournal, journalLocked = false, false, false, false
	return s, nil
}

// Inspect returns a projection derived from a fresh replay through the retained
// journal directory. It also proves the basename still names the retained inode.
func (s *Session) Inspect() (Receipt, error) {
	if s == nil {
		return Receipt{}, errors.New("release journal session closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inspectLocked()
}

func (s *Session) inspectLocked() (Receipt, error) {
	_, receipt, err := s.inspectSnapshotLocked()
	return receipt, err
}

func (s *Session) inspectSnapshotLocked() (releaseJournalSnapshot, Receipt, error) {
	if s == nil || s.closed {
		return releaseJournalSnapshot{}, Receipt{}, errors.New("release journal session closed")
	}
	if err := s.validateRetainedBinding(); err != nil {
		return releaseJournalSnapshot{}, Receipt{}, err
	}
	snapshot, err := loadSnapshot(s.journalFD, uint64(s.journalStat.Dev))
	if err != nil {
		return releaseJournalSnapshot{}, Receipt{}, err
	}
	if err := validateJournalName(s.journalName, snapshot); err != nil {
		return releaseJournalSnapshot{}, Receipt{}, err
	}
	receipt, err := receiptFromSnapshot(snapshot)
	if err != nil {
		return releaseJournalSnapshot{}, Receipt{}, err
	}
	if err := s.validateRetainedBinding(); err != nil {
		return releaseJournalSnapshot{}, Receipt{}, err
	}
	return snapshot, receipt, nil
}

// InspectLayoutSwitched is the fresh CA42 admission boundary. Recovery of an
// already-published admission record is deliberately a separate future API.
func (s *Session) InspectLayoutSwitched() (Receipt, error) {
	receipt, err := s.Inspect()
	if err != nil {
		return Receipt{}, err
	}
	if receipt.currentState != stateLayoutSwitched || receipt.currentSequence != layoutSwitchedSegmentCount-1 ||
		receipt.currentHeadSHA256 != receipt.boundaryHeadSHA256 || receipt.currentJournalSHA256 != receipt.boundaryJournalSHA256 {
		return Receipt{}, deny("layout_switched_boundary_invalid")
	}
	return receipt, nil
}

// InspectAdmissionBoundary accepts only the fresh sequence-6 boundary or its
// one-record ADMISSION_ATTEMPTED successor. The latter exists solely so an
// exact retry can repair durability after a post-rename failure.
func (s *Session) InspectAdmissionBoundary() (Receipt, error) {
	receipt, err := s.Inspect()
	if err != nil {
		return Receipt{}, err
	}
	if receipt.currentState == stateLayoutSwitched && receipt.currentSequence == layoutSwitchedSegmentCount-1 &&
		receipt.currentHeadSHA256 == receipt.boundaryHeadSHA256 && receipt.currentJournalSHA256 == receipt.boundaryJournalSHA256 {
		return receipt, nil
	}
	if receipt.currentState == stateAdmissionAttempted && receipt.currentSequence == layoutSwitchedSegmentCount {
		return receipt, nil
	}
	return Receipt{}, deny("admission_boundary_invalid")
}

// AdvanceAdmissionAttempted replays and compares the exact receipt after the
// external authority ledger has been durably reserved, then publishes only the
// fixed v2 LAYOUT_SWITCHED -> ADMISSION_ATTEMPTED transition.
func (s *Session) AdvanceAdmissionAttempted(expected Receipt, retainedEvidence *os.File, baseline AdmissionEvidence, now time.Time) (Receipt, bool, error) {
	if s == nil {
		return Receipt{}, false, errors.New("release journal session closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, current, err := s.inspectSnapshotLocked()
	if err != nil {
		return Receipt{}, false, err
	}
	if expected.currentState != stateLayoutSwitched || expected.currentSequence != layoutSwitchedSegmentCount-1 ||
		expected.currentHeadSHA256 != expected.boundaryHeadSHA256 || expected.currentJournalSHA256 != expected.boundaryJournalSHA256 ||
		current.boundaryHeadSHA256 != expected.currentHeadSHA256 || current.boundaryJournalSHA256 != expected.currentJournalSHA256 ||
		current.releaseAttemptID != expected.releaseAttemptID || current.journalID != expected.journalID {
		return Receipt{}, false, deny("journal_cas_or_state_conflict")
	}
	if retainedEvidence == nil || int(retainedEvidence.Fd()) < 3 {
		return Receipt{}, false, deny("evidence_fd_must_be_retained")
	}
	evidence, err := readAdmissionEvidenceFD(int(retainedEvidence.Fd()))
	if err != nil {
		return Receipt{}, false, err
	}
	if baseline.sha256 == "" || baseline.size <= 0 || baseline.size != evidence.size ||
		subtle.ConstantTimeCompare([]byte(baseline.sha256), []byte(evidence.sha256)) != 1 {
		return Receipt{}, false, deny("evidence_changed_after_admission_precheck")
	}
	eventID := sha256Bytes([]byte(admissionEventDomainV2 + expected.releaseAttemptID + "\x00" + expected.currentHeadSHA256 + "\x00ADMISSION_ATTEMPTED\x00" + baseline.sha256))
	if current.currentState == stateAdmissionAttempted && current.currentSequence == layoutSwitchedSegmentCount {
		last := snapshot.Transitions[len(snapshot.Transitions)-1]
		if last.JournalFormat != releaseJournalFormatV2 || last.ReleaseAttemptID != expected.releaseAttemptID ||
			last.JournalID != expected.journalID || last.Sequence != strconv.Itoa(layoutSwitchedSegmentCount) ||
			last.PreviousState != stateLayoutSwitched || last.State != stateAdmissionAttempted ||
			last.PreviousRecordSHA256 != expected.currentHeadSHA256 || last.EventID != eventID ||
			last.EvidenceSHA256 != baseline.sha256 || last.EvidenceSize != strconv.FormatInt(baseline.size, 10) {
			return Receipt{}, false, deny("exact_retry_record_mismatch")
		}
		if err := syncSnapshot(s.journalFD, uint64(s.journalStat.Dev), snapshot); err != nil {
			return Receipt{}, false, err
		}
		if err := releaseFsync(s.rootFD); err != nil {
			return Receipt{}, false, err
		}
		_, reloaded, err := s.inspectSnapshotLocked()
		if err != nil {
			return Receipt{}, false, err
		}
		if reloaded != current {
			return Receipt{}, false, deny("recovered_journal_changed")
		}
		finalEvidence, finalErr := readAdmissionEvidenceFD(int(retainedEvidence.Fd()))
		if finalErr != nil || finalEvidence.size != baseline.size ||
			subtle.ConstantTimeCompare([]byte(finalEvidence.sha256), []byte(baseline.sha256)) != 1 {
			return Receipt{}, false, denyErr("admission_recovery_evidence_ambiguous", finalErr)
		}
		return reloaded, true, nil
	}
	if current != expected || current.currentState != stateLayoutSwitched || current.currentSequence != layoutSwitchedSegmentCount-1 {
		return Receipt{}, false, deny("journal_cas_or_state_conflict")
	}
	occurred := strconv.FormatInt(now.Unix(), 10)
	if !canonicalPositive(occurred) {
		return Receipt{}, false, deny("transition_time_invalid")
	}
	updated, recovered, err := advanceOpenJournal(
		s.rootFD, s.journalFD, s.rootStat, s.journalStat, s.journalName, s.policy,
		current.releaseAttemptID, current.currentJournalSHA256, stateLayoutSwitched,
		stateAdmissionAttempted, eventID, occurred, evidenceDigest{sha256: baseline.sha256, size: baseline.size},
	)
	if err != nil {
		return Receipt{}, false, err
	}
	receipt, err := receiptFromSnapshot(updated)
	if err != nil {
		return Receipt{}, false, err
	}
	if receipt.currentState != stateAdmissionAttempted || receipt.currentSequence != layoutSwitchedSegmentCount {
		return Receipt{}, false, deny("admission_transition_result_invalid")
	}
	if err := s.validateRetainedBinding(); err != nil {
		return Receipt{}, false, denyErr("admission_commit_binding_ambiguous", err)
	}
	finalEvidence, err := readAdmissionEvidenceFD(int(retainedEvidence.Fd()))
	if err != nil || finalEvidence.size != baseline.size ||
		subtle.ConstantTimeCompare([]byte(finalEvidence.sha256), []byte(baseline.sha256)) != 1 {
		return Receipt{}, false, denyErr("admission_commit_evidence_ambiguous", err)
	}
	return receipt, recovered, nil
}

// InspectAdmissionEvidence validates and hashes a retained, immutable root-owned
// 0400 regular file before the authority ledger is touched.
func InspectAdmissionEvidence(retained *os.File) (AdmissionEvidence, error) {
	if retained == nil || int(retained.Fd()) < 3 {
		return AdmissionEvidence{}, deny("evidence_fd_must_be_retained")
	}
	digest, err := readAdmissionEvidenceFD(int(retained.Fd()))
	if err != nil {
		return AdmissionEvidence{}, err
	}
	return AdmissionEvidence{sha256: digest.sha256, size: digest.size}, nil
}

func readAdmissionEvidenceFD(fd int) (evidenceDigest, error) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return evidenceDigest{}, denyErr("evidence_fd_flags_failed", err)
	}
	if flags&unix.O_ACCMODE != unix.O_RDONLY {
		return evidenceDigest{}, deny("evidence_fd_must_be_read_only")
	}
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return evidenceDigest{}, denyErr("evidence_fstat_failed", err)
	}
	if before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Uid != 0 || before.Mode&07777 != 0400 || before.Nlink != 1 {
		return evidenceDigest{}, deny("evidence_identity_or_mode_invalid")
	}
	if before.Size <= 0 || before.Size > maxRecordBytes {
		return evidenceDigest{}, deny("evidence_size_invalid")
	}
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return evidenceDigest{}, err
	}
	syscall.CloseOnExec(duplicate)
	file := os.NewFile(uintptr(duplicate), "ca42-admission-evidence")
	if file == nil {
		syscall.Close(duplicate)
		return evidenceDigest{}, errors.New("wrap admission evidence fd")
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return evidenceDigest{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return evidenceDigest{}, err
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil {
		return evidenceDigest{}, err
	}
	if !sameFileSnapshot(before, after) || int64(len(data)) != after.Size {
		return evidenceDigest{}, deny("evidence_changed_or_short_read")
	}
	return evidenceDigest{sha256: sha256Bytes(data), size: after.Size}, nil
}

func (s *Session) validateRetainedBinding() error {
	if err := validateDirectoryIdentity(s.rootFD, s.rootStat, s.policy.allowedDevices); err != nil {
		return err
	}
	if err := validateDirectoryIdentity(s.journalFD, s.journalStat, s.policy.allowedDevices); err != nil {
		return err
	}
	reboundFD, reboundStat, err := openJournalDirectory(s.rootFD, s.journalName, uint64(s.rootStat.Dev), s.policy.allowedDevices)
	if err != nil {
		return denyErr("journal_retained_binding_missing", err)
	}
	syscall.Close(reboundFD)
	if !sameFileIdentity(reboundStat, s.journalStat) {
		return deny("journal_retained_binding_changed")
	}
	return nil
}

// Close releases locks and descriptors. It is idempotent.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var result error
	if err := syscall.Flock(s.journalFD, syscall.LOCK_UN); err != nil {
		result = errors.Join(result, err)
	}
	if err := syscall.Close(s.journalFD); err != nil {
		result = errors.Join(result, err)
	}
	if err := syscall.Flock(s.rootFD, syscall.LOCK_UN); err != nil {
		result = errors.Join(result, err)
	}
	if err := syscall.Close(s.rootFD); err != nil {
		result = errors.Join(result, err)
	}
	s.journalFD, s.rootFD = -1, -1
	return result
}
