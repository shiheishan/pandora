//go:build linux && (amd64 || arm64)

package ca42nonce

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const nonceDirectorySuffix = ".global-nonce-v1"

var nonceDirectoryRE = regexp.MustCompile(`^([0-9a-f]{64})\.([0-9a-f]{64})\.global-nonce-v1$`)

// ErrStoreBusy is a stable lock-contention classification. Coordinators must
// release all earlier-ranked capabilities before retrying from the beginning.
var ErrStoreBusy = errors.New("CA42 nonce store busy")

// retainedStore is deliberately package-private. The admission coordinator
// will own the only production opener after the journal and authority-ledger
// lock order is frozen. Callers cannot turn paths, record bytes, or Snapshot
// values into mutation authority.
type retainedStore struct {
	lease    *retainedStoreLease
	rootFD   int
	rootStat syscall.Stat_t
	hooks    nonceStoreHooks
}

type retainedStoreLease struct {
	mu             sync.Mutex
	closed         bool
	activeSessions uint64
}

type nonceStoreHooks struct {
	afterDirectoryCreate func(string) error
	afterRecordLink      func(string) error
	beforeDirectorySync  func(string) error
	afterDirectorySync   func(string) error
}

type nonceSession struct {
	store   *retainedStore
	lease   *nonceSessionLease
	dirFD   int
	dirStat syscall.Stat_t
	dirName string
	nonceID string
	txID    string
}

type nonceSessionLease struct {
	closed bool
}

// openRetainedStore accepts only an already-retained root-owned 0700
// directory. It reopens an independent description and holds an exclusive
// flock for the lifetime of the store.
func openRetainedStore(root *os.File, hooks nonceStoreHooks) (*retainedStore, error) {
	if os.Geteuid() != 0 || root == nil || int(root.Fd()) < 3 {
		return nil, errors.New("CA42 nonce retained root authority invalid")
	}
	var original syscall.Stat_t
	if err := syscall.Fstat(int(root.Fd()), &original); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	closeFD := true
	defer func() {
		if closeFD {
			_ = syscall.Close(fd)
		}
	}()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if !sameNonceDirectory(stat, original) || stat.Mode&07777 != 0700 {
		return nil, errors.New("CA42 nonce retained root identity invalid")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(ErrStoreBusy, err)
		}
		return nil, err
	}
	store := &retainedStore{lease: &retainedStoreLease{}, rootFD: fd, rootStat: stat, hooks: hooks}
	if _, err := store.scanRootLocked("", ""); err != nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		return nil, err
	}
	closeFD = false
	return store, nil
}

func (s *retainedStore) reserve(fields reservationFields) (*nonceSession, bool, error) {
	if s == nil || s.lease == nil {
		return nil, false, errors.New("CA42 nonce store closed")
	}
	data, _, err := reservationRecordBytes(fields)
	if err != nil {
		return nil, false, err
	}
	s.lease.mu.Lock()
	defer s.lease.mu.Unlock()
	if err := s.validateLocked(); err != nil {
		return nil, false, err
	}
	name := nonceDirectoryName(fields.NonceID, fields.TransactionID)
	if existing, err := s.scanRootLocked(fields.NonceID, fields.TransactionID); err != nil {
		return nil, false, err
	} else if existing != "" && existing != name {
		return nil, false, errors.New("CA42 nonce replay transaction conflict")
	}
	created := false
	if err := unix.Mkdirat(s.rootFD, name, 0700); err != nil {
		if !errors.Is(err, syscall.EEXIST) {
			return nil, false, err
		}
	} else {
		created = true
	}
	session, err := s.openSessionLocked(name, fields.NonceID, fields.TransactionID)
	if err != nil {
		return nil, false, err
	}
	if created {
		// Persist the empty basename before exposing the directory-created crash
		// boundary. A restart (including after sudden power loss on a filesystem
		// honoring fsync) must retain the fence even when no record was linked.
		if err := fsyncNonceDirectory(session.dirFD, "allocation-child", s.hooks); err != nil {
			_ = session.closeLocked()
			return nil, false, err
		}
		if err := fsyncNonceDirectory(s.rootFD, "allocation-root", s.hooks); err != nil {
			_ = session.closeLocked()
			return nil, false, err
		}
		if s.hooks.afterDirectoryCreate != nil {
			if err := s.hooks.afterDirectoryCreate(name); err != nil {
				_ = session.closeLocked()
				return nil, false, err
			}
		}
		// The create hook models the only package-private integration boundary.
		// Recheck exact emptiness so even an injected unknown entry fails before
		// the canonical RESERVED record is published.
		empty, inventoryErr := nonceDirectoryEmpty(session.dirFD)
		if inventoryErr != nil {
			_ = session.closeLocked()
			return nil, false, inventoryErr
		}
		if !empty {
			_ = session.closeLocked()
			return nil, false, errors.New("CA42 nonce newly allocated directory inventory changed")
		}
	} else {
		empty, inventoryErr := nonceDirectoryEmpty(session.dirFD)
		if inventoryErr != nil {
			_ = session.closeLocked()
			return nil, false, inventoryErr
		}
		if empty {
			_ = session.closeLocked()
			return nil, false, ErrReservationIncomplete
		}
		// Prove the existing directory is already a complete, canonical
		// reservation before any exact-retry durability repair. Unknown or
		// partial inventory must never be mutated into a caller-selected tuple.
		if _, inventoryErr := session.inspectLocked(); inventoryErr != nil {
			_ = session.closeLocked()
			return nil, false, inventoryErr
		}
	}
	recovered, err := publishNonceRecord(session.dirFD, ReservedRecordName, data, uint64(session.dirStat.Dev), s.hooks)
	if err != nil {
		_ = session.closeLocked()
		return nil, false, err
	}
	if err := fsyncNonceDirectory(s.rootFD, "root", s.hooks); err != nil {
		_ = session.closeLocked()
		return nil, false, err
	}
	snapshot, err := session.inspectLocked()
	if err != nil {
		_ = session.closeLocked()
		return nil, false, err
	}
	if snapshot.nonceID != fields.NonceID || snapshot.txID != fields.TransactionID {
		_ = session.closeLocked()
		return nil, false, errors.New("CA42 nonce reservation binding changed")
	}
	return session, recovered || !created, nil
}

func (s *retainedStore) openExisting(nonceID, transactionID string) (*nonceSession, error) {
	if s == nil || s.lease == nil || !validSHA(nonceID, false) || !validSHA(transactionID, false) {
		return nil, errors.New("CA42 nonce session identity invalid")
	}
	s.lease.mu.Lock()
	defer s.lease.mu.Unlock()
	if err := s.validateLocked(); err != nil {
		return nil, err
	}
	name := nonceDirectoryName(nonceID, transactionID)
	existing, err := s.scanRootLocked(nonceID, transactionID)
	if err != nil {
		return nil, err
	}
	if existing != name {
		return nil, errors.New("CA42 nonce reservation not found")
	}
	session, err := s.openSessionLocked(name, nonceID, transactionID)
	if err != nil {
		return nil, err
	}
	empty, err := nonceDirectoryEmpty(session.dirFD)
	if err != nil {
		_ = session.closeLocked()
		return nil, err
	}
	if empty {
		_ = session.closeLocked()
		return nil, ErrReservationIncomplete
	}
	if _, err := session.inspectLocked(); err != nil {
		_ = session.closeLocked()
		return nil, err
	}
	return session, nil
}

func (s *retainedStore) openSessionLocked(name, nonceID, txID string) (*nonceSession, error) {
	fd, stat, err := openNonceDirectoryAt(s.rootFD, name, uint64(s.rootStat.Dev))
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(ErrStoreBusy, err)
		}
		return nil, err
	}
	s.lease.activeSessions++
	return &nonceSession{store: s, lease: &nonceSessionLease{}, dirFD: fd, dirStat: stat, dirName: name, nonceID: nonceID, txID: txID}, nil
}

func (s *retainedStore) scanRootLocked(wantNonce, wantTx string) (string, error) {
	fd, err := unix.Openat(s.rootFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	directory := os.NewFile(uintptr(fd), "ca42-nonce-root")
	if directory == nil {
		_ = syscall.Close(fd)
		return "", errors.New("CA42 nonce directory descriptor invalid")
	}
	names, err := directory.Readdirnames(-1)
	_ = directory.Close()
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	found := ""
	for _, name := range names {
		parts := nonceDirectoryRE.FindStringSubmatch(name)
		if parts == nil {
			return "", fmt.Errorf("CA42 nonce root unknown entry: %s", name)
		}
		childFD, _, err := openNonceDirectoryAt(s.rootFD, name, uint64(s.rootStat.Dev))
		if err != nil {
			return "", err
		}
		_ = syscall.Close(childFD)
		if wantNonce != "" && parts[1] == wantNonce {
			if found != "" || parts[2] != wantTx {
				return "", errors.New("CA42 nonce replay transaction conflict")
			}
			found = name
		}
	}
	return found, nil
}

func (s *retainedStore) validateLocked() error {
	if s.lease == nil || s.lease.closed || s.rootFD < 3 {
		return errors.New("CA42 nonce store closed")
	}
	var now syscall.Stat_t
	if err := syscall.Fstat(s.rootFD, &now); err != nil {
		return err
	}
	if !sameNonceDirectory(now, s.rootStat) || now.Mode&07777 != 0700 {
		return errors.New("CA42 nonce retained root identity changed")
	}
	return nil
}

func (s *retainedStore) close() error {
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
	if s.lease.activeSessions != 0 {
		return ErrStoreBusy
	}
	s.lease.closed = true
	var result error
	if err := syscall.Flock(s.rootFD, syscall.LOCK_UN); err != nil {
		result = errors.Join(result, err)
	}
	if err := syscall.Close(s.rootFD); err != nil {
		result = errors.Join(result, err)
	}
	s.rootFD = -1
	return result
}

func (s *nonceSession) inspect() (Snapshot, error) {
	if s == nil || s.store == nil {
		return Snapshot{}, errors.New("CA42 nonce session closed")
	}
	if s.store.lease == nil {
		return Snapshot{}, errors.New("CA42 nonce session closed")
	}
	s.store.lease.mu.Lock()
	defer s.store.lease.mu.Unlock()
	return s.inspectLocked()
}

func (s *nonceSession) inspectLocked() (Snapshot, error) {
	if s.lease == nil || s.lease.closed {
		return Snapshot{}, errors.New("CA42 nonce session closed")
	}
	if err := s.store.validateLocked(); err != nil {
		return Snapshot{}, err
	}
	if err := s.validateDirectoryLocked(); err != nil {
		return Snapshot{}, err
	}
	records, err := loadNonceRecords(s.dirFD, uint64(s.dirStat.Dev))
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := Parse(records)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.nonceID != s.nonceID || snapshot.txID != s.txID {
		return Snapshot{}, errors.New("CA42 nonce directory record binding changed")
	}
	return snapshot, nil
}

func (s *nonceSession) commit(fields committedFields) (Snapshot, bool, error) {
	return s.publishTerminal(fields, recoveryFields{}, true)
}

func (s *nonceSession) recover(fields recoveryFields) (Snapshot, bool, error) {
	return s.publishTerminal(committedFields{}, fields, false)
}

func (s *nonceSession) publishTerminal(committed committedFields, recovery recoveryFields, commit bool) (Snapshot, bool, error) {
	if s == nil || s.store == nil {
		return Snapshot{}, false, errors.New("CA42 nonce session closed")
	}
	if s.store.lease == nil {
		return Snapshot{}, false, errors.New("CA42 nonce session closed")
	}
	s.store.lease.mu.Lock()
	defer s.store.lease.mu.Unlock()
	current, err := s.inspectLocked()
	if err != nil {
		return Snapshot{}, false, err
	}
	reserved, err := parseRecord(ReservedRecordName, current.records[ReservedRecordName], reservedFieldNames[:])
	if err != nil {
		return Snapshot{}, false, errors.New("CA42 nonce retained reservation invalid")
	}
	var name string
	var data []byte
	if commit {
		committed.NonceID, committed.TransactionID, committed.ReservationSHA256 = s.nonceID, s.txID, reserved.sha256
		name = CommittedRecordName
		data, _, err = committedRecordBytes(committed)
	} else {
		recovery.NonceID, recovery.TransactionID, recovery.ReservationSHA256 = s.nonceID, s.txID, reserved.sha256
		name = RecoveryRecordName
		data, _, err = recoveryRecordBytes(recovery)
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	if current.state != Reserved {
		expectedState := Committed
		if !commit {
			expectedState = RecoveryRequired
		}
		if current.state != expectedState || !bytes.Equal(current.records[name], data) {
			return current, false, errors.New("CA42 nonce terminal fork or divergent retry denied")
		}
		if err := fsyncNonceDirectory(s.dirFD, name, s.store.hooks); err != nil {
			if _, reconcileErr := reconcileNoncePublication(s.dirFD, name, data, uint64(s.dirStat.Dev), s.store.hooks); reconcileErr != nil {
				return Snapshot{}, false, reconcileErr
			}
		}
		return current, true, nil
	}
	recovered, err := publishNonceRecord(s.dirFD, name, data, uint64(s.dirStat.Dev), s.store.hooks)
	if err != nil {
		return Snapshot{}, false, err
	}
	result, err := s.inspectLocked()
	return result, recovered, err
}

func (s *nonceSession) validateDirectoryLocked() error {
	var now syscall.Stat_t
	if err := syscall.Fstat(s.dirFD, &now); err != nil {
		return err
	}
	if !sameNonceDirectory(now, s.dirStat) || now.Mode&07777 != 0700 {
		return errors.New("CA42 nonce retained directory identity changed")
	}
	rebound, stat, err := openNonceDirectoryAt(s.store.rootFD, s.dirName, uint64(s.store.rootStat.Dev))
	if err != nil {
		return err
	}
	_ = syscall.Close(rebound)
	if !sameNonceDirectory(stat, s.dirStat) {
		return errors.New("CA42 nonce retained directory binding changed")
	}
	return nil
}

func (s *nonceSession) closeLocked() error {
	if s.lease == nil || s.lease.closed {
		return nil
	}
	s.lease.closed = true
	var result error
	if err := syscall.Flock(s.dirFD, syscall.LOCK_UN); err != nil {
		result = errors.Join(result, err)
	}
	if err := syscall.Close(s.dirFD); err != nil {
		result = errors.Join(result, err)
	}
	s.dirFD = -1
	if s.store != nil && s.store.lease != nil && s.store.lease.activeSessions > 0 {
		s.store.lease.activeSessions--
	}
	return result
}

func (s *nonceSession) close() error {
	if s == nil || s.store == nil {
		return nil
	}
	if s.store.lease == nil {
		return nil
	}
	s.store.lease.mu.Lock()
	defer s.store.lease.mu.Unlock()
	return s.closeLocked()
}

func nonceDirectoryName(nonceID, txID string) string {
	return nonceID + "." + txID + nonceDirectorySuffix
}

func openNonceDirectoryAt(rootFD int, name string, device uint64) (int, syscall.Stat_t, error) {
	if nonceDirectoryRE.FindStringSubmatch(name) == nil {
		return -1, syscall.Stat_t{}, errors.New("CA42 nonce directory name invalid")
	}
	fd, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, err
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = syscall.Close(fd)
		return -1, stat, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&07777 != 0700 || stat.Nlink < 1 || uint64(stat.Dev) != device {
		_ = syscall.Close(fd)
		return -1, stat, errors.New("CA42 nonce directory authority invalid")
	}
	return fd, stat, nil
}

func loadNonceRecords(dirFD int, device uint64) (map[string][]byte, error) {
	fd, err := unix.Openat(dirFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "ca42-nonce")
	names, err := directory.Readdirnames(-1)
	_ = directory.Close()
	if err != nil {
		return nil, err
	}
	if len(names) < 1 || len(names) > 2 {
		return nil, errors.New("CA42 nonce retained inventory invalid")
	}
	records := make(map[string][]byte, len(names))
	for _, name := range names {
		if name != ReservedRecordName && name != CommittedRecordName && name != RecoveryRecordName {
			return nil, errors.New("CA42 nonce retained record name invalid")
		}
		data, err := readNonceRecordAt(dirFD, name, device)
		if err != nil {
			return nil, err
		}
		records[name] = data
	}
	return records, nil
}

func nonceDirectoryEmpty(dirFD int) (bool, error) {
	fd, err := unix.Openat(dirFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	directory := os.NewFile(uintptr(fd), "ca42-nonce-inventory")
	if directory == nil {
		_ = syscall.Close(fd)
		return false, errors.New("CA42 nonce inventory descriptor invalid")
	}
	names, err := directory.Readdirnames(-1)
	_ = directory.Close()
	if err != nil {
		return false, err
	}
	return len(names) == 0, nil
}

func readNonceRecordAt(dirFD int, name string, device uint64) ([]byte, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Uid != 0 || before.Gid != 0 || before.Mode&07777 != 0400 || before.Nlink != 1 || uint64(before.Dev) != device || before.Size <= 0 || before.Size > MaxRecordBytes {
		return nil, errors.New("CA42 nonce retained record authority invalid")
	}
	data := make([]byte, before.Size)
	if _, err := file.ReadAt(data, 0); err != nil {
		return nil, err
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim || before.Ino != after.Ino {
		return nil, errors.New("CA42 nonce retained record changed during read")
	}
	return data, nil
}

func publishNonceRecord(dirFD int, name string, data []byte, device uint64, hooks nonceStoreHooks) (bool, error) {
	if name != ReservedRecordName && name != CommittedRecordName && name != RecoveryRecordName {
		return false, errors.New("CA42 nonce publication name invalid")
	}
	if existing, err := readNonceRecordAt(dirFD, name, device); err == nil {
		if !bytes.Equal(existing, data) {
			return false, errors.New("CA42 nonce divergent retry denied")
		}
		if err := fsyncNonceDirectory(dirFD, name, hooks); err != nil {
			return reconcileNoncePublication(dirFD, name, data, device, hooks)
		}
		return true, nil
	} else if !errors.Is(err, syscall.ENOENT) {
		return false, err
	}
	tmpFD, err := unix.Openat(dirFD, ".", unix.O_WRONLY|unix.O_TMPFILE|unix.O_CLOEXEC, 0600)
	if err != nil {
		return false, err
	}
	tmp := os.NewFile(uintptr(tmpFD), "ca42-nonce-tmpfile")
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		return false, err
	}
	if err := tmp.Chmod(0400); err != nil {
		return false, err
	}
	// Persist the sealed mode before publishing the anonymous inode. A sync
	// before chmod is insufficient for power-loss durability of the metadata.
	if err := tmp.Sync(); err != nil {
		return false, err
	}
	if err := unix.Linkat(tmpFD, "", dirFD, name, unix.AT_EMPTY_PATH); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			existing, readErr := readNonceRecordAt(dirFD, name, device)
			if readErr == nil && bytes.Equal(existing, data) {
				if syncErr := fsyncNonceDirectory(dirFD, name, hooks); syncErr != nil {
					return reconcileNoncePublication(dirFD, name, data, device, hooks)
				}
				return true, nil
			}
			return false, errors.New("CA42 nonce concurrent divergent publication")
		}
		return false, err
	}
	if hooks.afterRecordLink != nil {
		if err := hooks.afterRecordLink(name); err != nil {
			return reconcileNoncePublication(dirFD, name, data, device, hooks)
		}
	}
	if err := fsyncNonceDirectory(dirFD, name, hooks); err != nil {
		return reconcileNoncePublication(dirFD, name, data, device, hooks)
	}
	return false, nil
}

func reconcileNoncePublication(dirFD int, name string, data []byte, device uint64, hooks nonceStoreHooks) (bool, error) {
	existing, err := readNonceRecordAt(dirFD, name, device)
	if err != nil || !bytes.Equal(existing, data) {
		return false, errors.New("CA42 nonce durability outcome ambiguous")
	}
	if err := syscall.Fsync(dirFD); err != nil {
		return false, errors.New("CA42 nonce durability outcome ambiguous")
	}
	return true, nil
}

func fsyncNonceDirectory(fd int, operation string, hooks nonceStoreHooks) error {
	if hooks.beforeDirectorySync != nil {
		if err := hooks.beforeDirectorySync(operation); err != nil {
			return err
		}
	}
	if err := syscall.Fsync(fd); err != nil {
		return err
	}
	if hooks.afterDirectorySync != nil {
		return hooks.afterDirectorySync(operation)
	}
	return nil
}

func sameNonceDirectory(actual, expected syscall.Stat_t) bool {
	return actual.Mode&syscall.S_IFMT == syscall.S_IFDIR && actual.Nlink >= 1 && actual.Dev == expected.Dev && actual.Ino == expected.Ino && actual.Uid == 0 && actual.Gid == 0 && actual.Mode == expected.Mode
}
