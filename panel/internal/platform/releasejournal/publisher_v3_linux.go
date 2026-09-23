//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"bytes"
	"errors"
	"syscall"
	"unsafe"
)

const (
	linuxOTmpfile    = 0x410000
	linuxATEmptyPath = 0x1000
)

var (
	v3OpenAnonymous = openV3Anonymous
	v3LinkAnonymous = linkV3Anonymous
)

// v3MutationCheckpoint is an unforgeable-in-practice, package-private CAS
// checkpoint derived only from a fresh replay while the retained session is
// locked. Public V3Snapshot values never grant mutation authority.
type v3MutationCheckpoint struct {
	valid                      bool
	rootDev, rootIno           uint64
	journalDev, journalIno     uint64
	journalID, attemptID       string
	state                      V3State
	sequence                   uint64
	headSHA256, manifestSHA256 string
}

type v3GenericIntent struct {
	target          V3State
	eventID         string
	evidenceSHA256  string
	occurredAtEpoch int64
	evidenceSize    uint64
}

func (s *V3Session) mutationCheckpoint() (v3MutationCheckpoint, error) {
	if s == nil {
		return v3MutationCheckpoint{}, errors.New("release journal v3 session closed")
	}
	if s.lease == nil {
		return v3MutationCheckpoint{}, errors.New("release journal v3 session closed")
	}
	s.lease.mu.Lock()
	defer s.lease.mu.Unlock()
	snapshot, err := s.inspectLocked()
	if err != nil {
		return v3MutationCheckpoint{}, err
	}
	return checkpointV3(s, snapshot), nil
}

func checkpointV3(s *V3Session, snapshot V3Snapshot) v3MutationCheckpoint {
	return v3MutationCheckpoint{
		valid: true, rootDev: uint64(s.rootStat.Dev), rootIno: s.rootStat.Ino,
		journalDev: uint64(s.journalStat.Dev), journalIno: s.journalStat.Ino,
		journalID: snapshot.JournalID(), attemptID: snapshot.AttemptID(), state: snapshot.State(),
		sequence: snapshot.Sequence(), headSHA256: snapshot.HeadSHA256(), manifestSHA256: snapshot.ManifestSHA256(),
	}
}

// advanceGenericV3 is deliberately package-private. Its caller can choose only
// transition evidence; all chain identity and ordering fields come from the
// retained checkpoint and a fresh replay.
func (s *V3Session) advanceGenericV3(checkpoint v3MutationCheckpoint, intent v3GenericIntent) (V3Snapshot, bool, error) {
	if s == nil {
		return V3Snapshot{}, false, errors.New("release journal v3 session closed")
	}
	if s.lease == nil {
		return V3Snapshot{}, false, errors.New("release journal v3 session closed")
	}
	s.lease.mu.Lock()
	defer s.lease.mu.Unlock()
	if !checkpoint.valid || checkpoint.rootDev != uint64(s.rootStat.Dev) || checkpoint.rootIno != s.rootStat.Ino ||
		checkpoint.journalDev != uint64(s.journalStat.Dev) || checkpoint.journalIno != s.journalStat.Ino {
		return V3Snapshot{}, false, deny("journal_v3_mutation_checkpoint_identity_invalid")
	}
	current, err := s.inspectLocked()
	if err != nil {
		return V3Snapshot{}, false, err
	}
	data, _, err := v3GenericRecordBytes(v3GenericFields{
		JournalID: checkpoint.journalID, AttemptID: checkpoint.attemptID,
		EventID: intent.eventID, PreviousRecordSHA256: checkpoint.headSHA256,
		EvidenceSHA256: intent.evidenceSHA256, Sequence: checkpoint.sequence + 1,
		EvidenceSize: intent.evidenceSize, OccurredAtEpoch: intent.occurredAtEpoch,
		PreviousState: checkpoint.state, State: intent.target,
	})
	if err != nil {
		return V3Snapshot{}, false, denyErr("journal_v3_candidate_invalid", err)
	}
	segment, ok := v3Segments[intent.target]
	if !ok || checkpoint.sequence >= 6 || v3StateSequence(intent.target) != int(checkpoint.sequence+1) {
		return V3Snapshot{}, false, deny("journal_v3_generic_target_invalid")
	}
	candidate, err := candidateV3(checkpoint, current, segment, data)
	if err != nil {
		return V3Snapshot{}, false, err
	}
	if !snapshotMatchesCheckpoint(current, checkpoint) {
		if exactV3Advance(current, checkpoint, segment, data, candidate) {
			return s.recoverExactV3Locked(current, checkpoint, segment, data, candidate)
		}
		return V3Snapshot{}, false, deny("journal_v3_cas_or_exact_retry_mismatch")
	}
	return s.publishV3Locked(segment, data, candidate)
}

func candidateV3(checkpoint v3MutationCheckpoint, current V3Snapshot, segment string, data []byte) (V3Snapshot, error) {
	if !current.parsed || len(current.names) < int(checkpoint.sequence+1) {
		return V3Snapshot{}, deny("journal_v3_candidate_prefix_missing")
	}
	records := make(map[string][]byte, checkpoint.sequence+2)
	for _, name := range current.names[:checkpoint.sequence+1] {
		records[name] = append([]byte(nil), current.records[name]...)
	}
	prefix, err := ParseV3(records)
	if err != nil || !snapshotMatchesCheckpoint(prefix, checkpoint) {
		return V3Snapshot{}, denyErr("journal_v3_candidate_prefix_mismatch", err)
	}
	if _, exists := records[segment]; exists {
		return V3Snapshot{}, deny("journal_v3_candidate_segment_exists")
	}
	records[segment] = append([]byte(nil), data...)
	candidate, err := ParseV3(records)
	if err != nil {
		return V3Snapshot{}, denyErr("journal_v3_candidate_replay_failed", err)
	}
	return candidate, nil
}

func snapshotMatchesCheckpoint(snapshot V3Snapshot, checkpoint v3MutationCheckpoint) bool {
	return snapshot.parsed && snapshot.JournalID() == checkpoint.journalID && snapshot.AttemptID() == checkpoint.attemptID &&
		snapshot.State() == checkpoint.state && snapshot.Sequence() == checkpoint.sequence &&
		snapshot.HeadSHA256() == checkpoint.headSHA256 && snapshot.ManifestSHA256() == checkpoint.manifestSHA256
}

func exactV3Advance(current V3Snapshot, checkpoint v3MutationCheckpoint, segment string, data []byte, candidate V3Snapshot) bool {
	if !current.parsed || current.Sequence() != checkpoint.sequence+1 || current.State() != candidate.State() ||
		current.HeadSHA256() != candidate.HeadSHA256() || current.ManifestSHA256() != candidate.ManifestSHA256() {
		return false
	}
	actual, ok := current.records[segment]
	return ok && bytes.Equal(actual, data)
}

func (s *V3Session) publishV3Locked(segment string, data []byte, candidate V3Snapshot) (V3Snapshot, bool, error) {
	owned := append([]byte(nil), data...)
	if err := s.validateRetainedBinding(); err != nil {
		return V3Snapshot{}, false, err
	}
	tmpFD, tmpStat, err := v3OpenAnonymous(s.journalFD, uint64(s.journalStat.Dev))
	if err != nil {
		return V3Snapshot{}, false, err
	}
	defer syscall.Close(tmpFD)
	if err := writeFull(tmpFD, owned); err != nil {
		return V3Snapshot{}, false, err
	}
	if err := releaseFdatasync(tmpFD); err != nil {
		return V3Snapshot{}, false, err
	}
	if err := syscall.Fchmod(tmpFD, 0400); err != nil {
		return V3Snapshot{}, false, err
	}
	if err := releaseFsync(tmpFD); err != nil {
		return V3Snapshot{}, false, err
	}
	if err := validateV3Anonymous(tmpFD, tmpStat, uint64(len(owned)), owned); err != nil {
		return V3Snapshot{}, false, err
	}
	if err := s.validateRetainedBinding(); err != nil {
		return V3Snapshot{}, false, err
	}
	if err := v3LinkAnonymous(tmpFD, s.journalFD, segment); err != nil {
		prior := checkpointV3FromCandidate(candidate)
		current, inspectErr := s.inspectLocked()
		if inspectErr == nil && exactV3Advance(current, prior, segment, owned, candidate) {
			return s.recoverExactV3Locked(current, prior, segment, owned, candidate)
		}
		if errors.Is(err, syscall.EEXIST) && inspectErr == nil {
			return V3Snapshot{}, false, denyErr("journal_v3_final_segment_conflict", err)
		}
		// A link helper or storage layer may report an error after the directory
		// entry became visible. Without an exact replay proof the outcome is
		// ambiguous, never a safe invitation to repeat an external side effect.
		return V3Snapshot{}, false, denyErr("journal_v3_commit_ambiguous", errors.Join(err, inspectErr))
	}
	// From this point onward the commit may be durable. Never unlink or replace
	// the final segment on any error; callers may only perform exact recovery.
	if err := verifyV3Linked(tmpFD, s.journalFD, segment, uint64(s.journalStat.Dev), owned); err != nil {
		return V3Snapshot{}, false, denyErr("journal_v3_commit_ambiguous", err)
	}
	if err := releaseFsync(s.journalFD); err != nil {
		return V3Snapshot{}, false, denyErr("journal_v3_commit_ambiguous", err)
	}
	if err := s.validateRetainedBinding(); err != nil {
		return V3Snapshot{}, false, denyErr("journal_v3_commit_ambiguous", err)
	}
	reloaded, err := s.inspectLocked()
	if err != nil || reloaded.HeadSHA256() != candidate.HeadSHA256() || reloaded.ManifestSHA256() != candidate.ManifestSHA256() || reloaded.State() != candidate.State() {
		return V3Snapshot{}, false, denyErr("journal_v3_commit_ambiguous", err)
	}
	return reloaded, false, nil
}

func (s *V3Session) recoverExactV3Locked(current V3Snapshot, checkpoint v3MutationCheckpoint, segment string, data []byte, candidate V3Snapshot) (V3Snapshot, bool, error) {
	if !exactV3Advance(current, checkpoint, segment, data, candidate) {
		return V3Snapshot{}, false, deny("journal_v3_exact_recovery_mismatch")
	}
	if err := syncV3Snapshot(s.journalFD, uint64(s.journalStat.Dev), current); err != nil {
		return V3Snapshot{}, false, denyErr("journal_v3_exact_recovery_sync_failed", err)
	}
	if err := s.validateRetainedBinding(); err != nil {
		return V3Snapshot{}, false, err
	}
	reloaded, err := s.inspectLocked()
	if err != nil || !exactV3Advance(reloaded, checkpoint, segment, data, candidate) {
		return V3Snapshot{}, false, denyErr("journal_v3_exact_recovery_reload_mismatch", err)
	}
	return reloaded, true, nil
}

func checkpointV3FromCandidate(candidate V3Snapshot) v3MutationCheckpoint {
	if len(candidate.names) < 2 {
		return v3MutationCheckpoint{}
	}
	prefix := make(map[string][]byte, len(candidate.names)-1)
	for _, name := range candidate.names[:len(candidate.names)-1] {
		prefix[name] = candidate.records[name]
	}
	prior, err := ParseV3(prefix)
	if err != nil {
		return v3MutationCheckpoint{}
	}
	return v3MutationCheckpoint{valid: true, journalID: prior.JournalID(), attemptID: prior.AttemptID(), state: prior.State(), sequence: prior.Sequence(), headSHA256: prior.HeadSHA256(), manifestSHA256: prior.ManifestSHA256()}
}

func openV3Anonymous(dirFD int, expectedDevice uint64) (int, syscall.Stat_t, error) {
	pointer, err := syscall.BytePtrFromString(".")
	if err != nil {
		return -1, syscall.Stat_t{}, err
	}
	how := openHow{Flags: uint64(syscall.O_RDWR | linuxOTmpfile | linuxOCloExec), Mode: 0600,
		Resolve: resolveBeneath | resolveNoSymlinks | resolveNoMagicLinks}
	rawFD, _, errno := syscall.Syscall6(linuxSYSOpenat2, uintptr(dirFD), uintptr(unsafe.Pointer(pointer)),
		uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
	if errno != 0 {
		return -1, syscall.Stat_t{}, denyErr("journal_v3_otmpfile_unsupported", errno)
	}
	fd := int(rawFD)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = syscall.Close(fd)
		return -1, stat, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&07777 != 0600 || stat.Nlink != 0 || uint64(stat.Dev) != expectedDevice {
		_ = syscall.Close(fd)
		return -1, stat, deny("journal_v3_anonymous_identity_invalid")
	}
	return fd, stat, nil
}

func validateV3Anonymous(fd int, initial syscall.Stat_t, size uint64, expected []byte) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Dev != initial.Dev || stat.Ino != initial.Ino || stat.Uid != initial.Uid || stat.Gid != initial.Gid ||
		stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&07777 != 0400 || stat.Nlink != 0 || uint64(stat.Size) != size {
		return deny("journal_v3_anonymous_changed")
	}
	actual := make([]byte, len(expected))
	if err := preadV3Full(fd, actual); err != nil || !bytes.Equal(actual, expected) {
		return denyErr("journal_v3_anonymous_readback_mismatch", err)
	}
	return nil
}

func preadV3Full(fd int, data []byte) error {
	offset := int64(0)
	for len(data) > 0 {
		n, err := syscall.Pread(fd, data, offset)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			return syscall.EIO
		}
		data = data[n:]
		offset += int64(n)
	}
	return nil
}

func linkV3Anonymous(fd, dirFD int, name string) error {
	empty, _ := syscall.BytePtrFromString("")
	target, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(linuxSYSLinkat, uintptr(fd), uintptr(unsafe.Pointer(empty)), uintptr(dirFD), uintptr(unsafe.Pointer(target)), linuxATEmptyPath, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func verifyV3Linked(tmpFD, journalFD int, segment string, device uint64, expected []byte) error {
	var retained syscall.Stat_t
	if err := syscall.Fstat(tmpFD, &retained); err != nil {
		return err
	}
	if retained.Nlink != 1 || retained.Mode&07777 != 0400 {
		return deny("journal_v3_link_count_invalid")
	}
	fd, stat, err := openV3ImmutableAt(journalFD, segment, device, nil)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if !sameFileIdentity(retained, stat) {
		return deny("journal_v3_published_inode_mismatch")
	}
	actual, _, err := readStableFD(fd)
	if err != nil || !bytes.Equal(actual, expected) {
		return denyErr("journal_v3_published_bytes_mismatch", err)
	}
	return nil
}
