//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"errors"
	"os"
	"reflect"
	"regexp"
	"sync"
	"syscall"
	"unsafe"
)

var journalV3RE = regexp.MustCompile(`^([a-z0-9][a-z0-9_.-]{0,127})\.([0-9a-f]{64})\.release-journal-v3$`)

// ErrV3SessionBusy is a stable retry classification for lock contention. A
// coordinator must release every already-acquired lower-rank capability before
// retrying from the start of the global lock order.
var ErrV3SessionBusy = errors.New("release journal v3 session busy")

// V3Session is a retained, read/replay capability for one exact Journal v3
// directory. The retained root FD, rather than an ambient pathname, is its
// namespace authority. A future production opener must additionally prove the
// supervisor's canonical parent/basename binding before exposing mutation.
// Unlike V3Snapshot it cannot be constructed from caller bytes. Mutation APIs
// remain package-private until retained nonce, authority-ledger and canonical
// parent capabilities can be consumed under the frozen global lock order.
type V3Session struct {
	lease       *v3SessionLease
	rootFD      int
	journalFD   int
	rootStat    syscall.Stat_t
	journalStat syscall.Stat_t
	journalName string
	policy      pathPolicy
}

type v3SessionLease struct {
	mu             sync.Mutex
	closed         bool
	productionRoot *v3RootCapability
}

// openV3SessionFromRetainedRoot duplicates a trusted root-owned 0700
// journal-root descriptor for package-internal bootstrap and test use,
// retains a shared root lock and an exclusive lock on the exact v3 journal,
// and verifies every record as a root-owned 0400 single-link regular file.
func openV3SessionFromRetainedRoot(retainedRoot *os.File, attemptID string) (*V3Session, error) {
	if os.Geteuid() != 0 {
		return nil, deny("euid_zero_required")
	}
	if retainedRoot == nil || !v3TokenRE.MatchString(attemptID) {
		return nil, deny("v3_session_identity_invalid")
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
	rootFD, err := reopenV3DirectoryFD(originalFD)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(rootFD)
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = syscall.Close(rootFD)
		}
	}()
	rootStat, err := validateDirectoryFD(rootFD, allowed)
	if err != nil {
		return nil, err
	}
	if !sameFileIdentity(original, rootStat) || rootStat.Mode&07777 != 0700 {
		return nil, deny("journal_root_retained_identity_invalid")
	}
	if err := syscall.Flock(rootFD, syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(ErrV3SessionBusy, err)
		}
		return nil, err
	}
	rootLocked := true
	defer func() {
		if rootLocked {
			_ = syscall.Flock(rootFD, syscall.LOCK_UN)
		}
	}()
	name, err := discoverV3Journal(rootFD, attemptID, allowed)
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
			_ = syscall.Close(journalFD)
		}
	}()
	if err := syscall.Flock(journalFD, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(ErrV3SessionBusy, err)
		}
		return nil, err
	}
	journalLocked := true
	defer func() {
		if journalLocked {
			_ = syscall.Flock(journalFD, syscall.LOCK_UN)
		}
	}()
	s := &V3Session{lease: &v3SessionLease{}, rootFD: rootFD, journalFD: journalFD, rootStat: rootStat, journalStat: journalStat,
		journalName: name, policy: pathPolicy{expectedDevice: uint64(rootStat.Dev), allowedDevices: allowed}}
	if err := s.validateRetainedBinding(); err != nil {
		return nil, err
	}
	snapshot, err := loadV3Snapshot(journalFD, uint64(journalStat.Dev))
	if err != nil {
		return nil, err
	}
	if snapshot.AttemptID() != attemptID {
		return nil, deny("journal_v3_attempt_mismatch")
	}
	if err := validateV3JournalName(name, snapshot); err != nil {
		return nil, err
	}
	if err := s.validateRetainedBinding(); err != nil {
		return nil, err
	}
	closeRoot, rootLocked, closeJournal, journalLocked = false, false, false, false
	return s, nil
}

func (s *V3Session) Inspect() (V3Snapshot, error) {
	if s == nil {
		return V3Snapshot{}, errors.New("release journal v3 session closed")
	}
	if s.lease == nil {
		return V3Snapshot{}, errors.New("release journal v3 session closed")
	}
	s.lease.mu.Lock()
	defer s.lease.mu.Unlock()
	return s.inspectLocked()
}

func (s *V3Session) inspectLocked() (V3Snapshot, error) {
	if s == nil || s.lease == nil || s.lease.closed {
		return V3Snapshot{}, errors.New("release journal v3 session closed")
	}
	if err := s.validateRetainedBinding(); err != nil {
		return V3Snapshot{}, err
	}
	snapshot, err := loadV3Snapshot(s.journalFD, uint64(s.journalStat.Dev))
	if err != nil {
		return V3Snapshot{}, err
	}
	if snapshot.AttemptID() == "" || snapshot.AttemptID() != v3AttemptFromJournalName(s.journalName) {
		return V3Snapshot{}, deny("journal_v3_attempt_binding_changed")
	}
	if err := validateV3JournalName(s.journalName, snapshot); err != nil {
		return V3Snapshot{}, err
	}
	if err := s.validateRetainedBinding(); err != nil {
		return V3Snapshot{}, err
	}
	return snapshot, nil
}

func (s *V3Session) InspectLayoutSwitched() (V3Snapshot, error) {
	snapshot, err := s.Inspect()
	if err != nil {
		return V3Snapshot{}, err
	}
	if snapshot.State() != V3LayoutSwitched || snapshot.Sequence() != 6 {
		return V3Snapshot{}, deny("journal_v3_layout_boundary_invalid")
	}
	return snapshot, nil
}

// Sync replays exact record bytes before and after syncing the retained
// journal/root directories. Final records are read-only and were required to
// be data-synced before publication; this method repairs directory durability
// only and never creates, opens writable, or rewrites a record.
func (s *V3Session) Sync() (V3Snapshot, error) {
	if s == nil {
		return V3Snapshot{}, errors.New("release journal v3 session closed")
	}
	if s.lease == nil {
		return V3Snapshot{}, errors.New("release journal v3 session closed")
	}
	s.lease.mu.Lock()
	defer s.lease.mu.Unlock()
	snapshot, err := s.inspectLocked()
	if err != nil {
		return V3Snapshot{}, err
	}
	if err := syncV3Snapshot(s.journalFD, uint64(s.journalStat.Dev), snapshot); err != nil {
		return V3Snapshot{}, err
	}
	if err := releaseFsync(s.rootFD); err != nil {
		return V3Snapshot{}, err
	}
	reloaded, err := s.inspectLocked()
	if err != nil {
		return V3Snapshot{}, err
	}
	if reloaded.HeadSHA256() != snapshot.HeadSHA256() || reloaded.ManifestSHA256() != snapshot.ManifestSHA256() {
		return V3Snapshot{}, deny("journal_v3_changed_during_sync")
	}
	return reloaded, nil
}

func (s *V3Session) validateRetainedBinding() error {
	if s == nil || s.lease == nil || s.lease.closed {
		return errors.New("release journal v3 session closed")
	}
	if s.lease.productionRoot != nil {
		if err := s.lease.productionRoot.validate(); err != nil {
			return denyErr("journal_v3_production_root_binding_changed", err)
		}
	}
	if err := validateDirectoryIdentity(s.rootFD, s.rootStat, s.policy.allowedDevices); err != nil {
		return err
	}
	if err := validateDirectoryIdentity(s.journalFD, s.journalStat, s.policy.allowedDevices); err != nil {
		return err
	}
	reboundFD, reboundStat, err := openJournalDirectory(s.rootFD, s.journalName, uint64(s.rootStat.Dev), s.policy.allowedDevices)
	if err != nil {
		return denyErr("journal_v3_retained_binding_missing", err)
	}
	_ = syscall.Close(reboundFD)
	if !sameFileIdentity(reboundStat, s.journalStat) {
		return deny("journal_v3_retained_binding_changed")
	}
	return nil
}

func (s *V3Session) Close() error {
	if s == nil {
		return nil
	}
	if s.lease == nil {
		return nil
	}
	s.lease.mu.Lock()
	defer s.lease.mu.Unlock()
	if s.lease.closed {
		return nil
	}
	s.lease.closed = true
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
	if s.lease.productionRoot != nil {
		if err := s.lease.productionRoot.close(); err != nil {
			result = errors.Join(result, err)
		}
		s.lease.productionRoot = nil
	}
	s.journalFD, s.rootFD = -1, -1
	return result
}

func validateV3JournalName(name string, snapshot V3Snapshot) error {
	parts := journalV3RE.FindStringSubmatch(name)
	if parts == nil || parts[1] != snapshot.AttemptID() || parts[2] != snapshot.JournalID() {
		return deny("journal_v3_basename_record_binding_mismatch")
	}
	return nil
}

func v3AttemptFromJournalName(name string) string {
	parts := journalV3RE.FindStringSubmatch(name)
	if parts == nil {
		return ""
	}
	return parts[1]
}

// discoverV3Journal intentionally accepts only the dedicated v3 basename
// grammar. A v1/v2 journal, stage, temp, evidence file, or any other entry in
// this retained root is a fail-closed configuration error.
func discoverV3Journal(rootFD int, attempt string, allowed map[uint64]struct{}) (string, error) {
	names, err := listDirectoryNames(rootFD)
	if err != nil {
		return "", err
	}
	var rootStat syscall.Stat_t
	if err := syscall.Fstat(rootFD, &rootStat); err != nil {
		return "", err
	}
	matches := make([]string, 0, 1)
	for _, name := range names {
		parts := journalV3RE.FindStringSubmatch(name)
		if parts == nil {
			return "", deny("unknown_entry_in_journal_v3_root")
		}
		fd, _, openErr := openJournalDirectory(rootFD, name, uint64(rootStat.Dev), allowed)
		if openErr != nil {
			return "", openErr
		}
		_ = syscall.Close(fd)
		if parts[1] == attempt {
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return "", deny("release_journal_v3_not_found")
	}
	if len(matches) != 1 {
		return "", deny("multiple_release_journal_v3_for_attempt")
	}
	return matches[0], nil
}

func loadV3Snapshot(journalFD int, device uint64) (V3Snapshot, error) {
	names, err := listDirectoryNames(journalFD)
	if err != nil {
		return V3Snapshot{}, err
	}
	valid := V3StateSegments()
	validNames := make(map[string]struct{}, len(valid))
	for _, name := range valid {
		validNames[name] = struct{}{}
	}
	records := make(map[string][]byte, len(names))
	for _, name := range names {
		if _, ok := validNames[name]; !ok {
			return V3Snapshot{}, deny("unknown_journal_v3_segment")
		}
		fd, _, openErr := openV3ImmutableAt(journalFD, name, device, nil)
		if openErr != nil {
			return V3Snapshot{}, openErr
		}
		data, _, readErr := readStableFD(fd)
		_ = syscall.Close(fd)
		if readErr != nil {
			return V3Snapshot{}, readErr
		}
		records[name] = data
	}
	snapshot, err := ParseV3(records)
	if err != nil {
		return V3Snapshot{}, denyErr("journal_v3_parse_failed", err)
	}
	return snapshot, nil
}

func syncV3Snapshot(journalFD int, device uint64, snapshot V3Snapshot) error {
	for _, name := range snapshot.names {
		fd, _, err := openV3ImmutableAt(journalFD, name, device, nil)
		if err != nil {
			return err
		}
		actual, _, err := readStableFD(fd)
		if err != nil {
			_ = syscall.Close(fd)
			return err
		}
		if !reflect.DeepEqual(actual, snapshot.records[name]) {
			_ = syscall.Close(fd)
			return deny("journal_v3_record_changed")
		}
		_ = syscall.Close(fd)
	}
	if err := releaseFsync(journalFD); err != nil {
		return err
	}
	reloaded, err := loadV3Snapshot(journalFD, device)
	if err != nil {
		return err
	}
	if reloaded.HeadSHA256() != snapshot.HeadSHA256() || reloaded.ManifestSHA256() != snapshot.ManifestSHA256() {
		return deny("journal_v3_manifest_changed")
	}
	return nil
}

func openV3ImmutableAt(dirFD int, name string, expectedDevice uint64, allowed map[uint64]struct{}) (int, syscall.Stat_t, error) {
	fd, err := openAt2(dirFD, name, syscall.O_RDONLY|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, denyErr("journal_v3_record_open_failed", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = syscall.Close(fd)
		return -1, stat, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&07777 != 0400 || stat.Nlink != 1 {
		_ = syscall.Close(fd)
		return -1, stat, deny("journal_v3_record_identity_or_mode_invalid")
	}
	if expectedDevice != ^uint64(0) && uint64(stat.Dev) != expectedDevice {
		_ = syscall.Close(fd)
		return -1, stat, deny("journal_v3_record_device_mismatch")
	}
	if allowed != nil {
		if _, ok := allowed[uint64(stat.Dev)]; !ok {
			_ = syscall.Close(fd)
			return -1, stat, deny("journal_v3_record_device_not_allowed")
		}
	}
	return fd, stat, nil
}

// reopenV3DirectoryFD creates an independent open-file description. Dup is
// forbidden here because flock state is shared by duplicated descriptions and
// the caller could otherwise unlock the session through its original FD.
func reopenV3DirectoryFD(retainedFD int) (int, error) {
	pointer, err := syscall.BytePtrFromString(".")
	if err != nil {
		return -1, err
	}
	how := openHow{Flags: uint64(syscall.O_RDONLY | linuxODirectory | linuxONoFollow | linuxOCloExec),
		Resolve: resolveBeneath | resolveNoSymlinks | resolveNoMagicLinks}
	fd, _, errno := syscall.Syscall6(linuxSYSOpenat2, uintptr(retainedFD), uintptr(unsafe.Pointer(pointer)),
		uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
	if errno != 0 {
		return -1, denyErr("journal_v3_root_reopen_failed", errno)
	}
	return int(fd), nil
}
