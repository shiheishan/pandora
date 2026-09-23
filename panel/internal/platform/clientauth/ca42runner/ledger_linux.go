//go:build linux

package ca42runner

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"golang.org/x/sys/unix"
)

const ledgerLockName = "authority-ledger.lock"

var ErrLedgerCommitAmbiguous = errors.New("authority ledger commit ambiguous")

// ErrLedgerBusy is the stable nonblocking lock-contention classification used
// by the future cross-store coordinator.
var ErrLedgerBusy = errors.New("authority ledger busy")

// ledgerReservationLease keeps the exact ledger directory and exclusive lock
// across plan and commit. It is package-private until the admission
// coordinator freezes and proves one global cross-store lock order.
type ledgerReservationLease struct {
	state *ledgerReservationState
}

type ledgerReservationState struct {
	mu            sync.Mutex
	directoryFD   int
	lockFD        int
	directoryStat unix.Stat_t
	lockStat      unix.Stat_t
	expected      *ca42authority.Ledger
	before        *ca42authority.Ledger
	planned       ca42authority.Ledger
	plannedReady  bool
	exactPlan     bool
	committed     bool
	closed        bool
}

func beginLedgerReservation(directoryFD int, expected *ca42authority.Ledger, descriptor ca42authority.Descriptor, now time.Time) (*ledgerReservationLease, error) {
	lease, err := acquireLedgerReservation(directoryFD, expected)
	if err != nil {
		return nil, err
	}
	if _, _, err := lease.plan(descriptor, now); err != nil {
		_ = lease.close()
		return nil, err
	}
	return lease, nil
}

// acquireLedgerReservation freezes the current ledger under its exclusive
// lock without deriving a time-dependent planned record. The cross-store
// coordinator can then inspect an already-persisted nonce reservation and use
// its original reserved_at value when calling plan after a process restart.
func acquireLedgerReservation(directoryFD int, expected *ca42authority.Ledger) (*ledgerReservationLease, error) {
	if directoryFD < 3 {
		return nil, errors.New("authority ledger retained directory invalid")
	}
	var original unix.Stat_t
	if err := unix.Fstat(directoryFD, &original); err != nil {
		return nil, err
	}
	reopened, err := unix.Openat(directoryFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	closeDirectory := true
	defer func() {
		if closeDirectory {
			_ = unix.Close(reopened)
		}
	}()
	var directoryStat unix.Stat_t
	if err := unix.Fstat(reopened, &directoryStat); err != nil {
		return nil, err
	}
	if !sameLedgerDirectory(original, directoryStat) || directoryStat.Mode&0o777 != 0o700 {
		return nil, errors.New("authority ledger retained directory identity denied")
	}
	lockFD, err := unix.Openat(reopened, ledgerLockName, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = unix.Close(lockFD)
		}
	}()
	if err := requireRootOwnedLock(lockFD); err != nil {
		return nil, err
	}
	var lockStat unix.Stat_t
	if err := unix.Fstat(lockFD, &lockStat); err != nil {
		return nil, err
	}
	if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errors.Join(ErrLedgerBusy, err)
		}
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			_ = unix.Flock(lockFD, unix.LOCK_UN)
		}
	}()
	current, err := readCurrentLedger(reopened)
	if err != nil {
		return nil, err
	}
	state := &ledgerReservationState{directoryFD: reopened, lockFD: lockFD, directoryStat: directoryStat,
		lockStat: lockStat, expected: cloneLedger(expected), before: cloneLedger(current)}
	lease := &ledgerReservationLease{state: state}
	if err := state.validateLocked(); err != nil {
		return nil, err
	}
	closeDirectory, closeLock, locked = false, false, false
	return lease, nil
}

func (l *ledgerReservationLease) plan(descriptor ca42authority.Descriptor, reservedAt time.Time) (ca42authority.Ledger, bool, error) {
	if l == nil || l.state == nil {
		return ca42authority.Ledger{}, false, errors.New("authority ledger reservation lease closed")
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	state := l.state
	if err := state.validateLocked(); err != nil {
		return ca42authority.Ledger{}, false, err
	}
	planned, exact, err := ca42authority.ReserveTransition(state.expected, state.before, descriptor, reservedAt)
	if err != nil {
		return ca42authority.Ledger{}, false, err
	}
	if state.plannedReady {
		if state.planned.RecordSHA256 != planned.RecordSHA256 || state.exactPlan != exact {
			return ca42authority.Ledger{}, false, errors.New("authority ledger reservation plan changed")
		}
		return state.planned, state.exactPlan, nil
	}
	state.planned, state.exactPlan, state.plannedReady = planned, exact, true
	return planned, exact, nil
}

func (l *ledgerReservationLease) plannedLedger() (ca42authority.Ledger, bool, error) {
	if l == nil || l.state == nil {
		return ca42authority.Ledger{}, false, errors.New("authority ledger reservation lease closed")
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if err := l.state.validateLocked(); err != nil {
		return ca42authority.Ledger{}, false, err
	}
	if !l.state.plannedReady {
		return ca42authority.Ledger{}, false, errors.New("authority ledger reservation not planned")
	}
	return l.state.planned, l.state.exactPlan, nil
}

func (l *ledgerReservationLease) commit() (ca42authority.Ledger, bool, error) {
	if l == nil || l.state == nil {
		return ca42authority.Ledger{}, false, errors.New("authority ledger reservation lease closed")
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	state := l.state
	if err := state.validateLocked(); err != nil {
		return ca42authority.Ledger{}, false, err
	}
	if !state.plannedReady {
		return ca42authority.Ledger{}, false, errors.New("authority ledger reservation not planned")
	}
	current, err := readCurrentLedger(state.directoryFD)
	if err != nil {
		return ca42authority.Ledger{}, false, err
	}
	if sameLedgerRecord(current, &state.planned) {
		if err := unix.Fsync(state.directoryFD); err != nil {
			return ca42authority.Ledger{}, false, fmt.Errorf("authority ledger exact-retry fsync failed: %w", err)
		}
		state.committed = true
		return state.planned, true, nil
	}
	if state.committed || !sameLedgerRecord(current, state.before) {
		return ca42authority.Ledger{}, false, errors.New("authority ledger reservation compare-and-swap conflict")
	}
	encoded, err := state.planned.CanonicalBytes()
	if err != nil {
		return ca42authority.Ledger{}, false, err
	}
	if err := atomicReplaceLedger(state.directoryFD, encoded); err != nil {
		return ca42authority.Ledger{}, false, err
	}
	current, err = readCurrentLedger(state.directoryFD)
	if err != nil || !sameLedgerRecord(current, &state.planned) {
		return ca42authority.Ledger{}, false, errors.Join(ErrLedgerCommitAmbiguous, err)
	}
	state.committed = true
	return state.planned, false, nil
}

func (l *ledgerReservationLease) close() error {
	if l == nil || l.state == nil {
		return nil
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	state := l.state
	if state.closed {
		return nil
	}
	state.closed = true
	var result error
	if err := unix.Flock(state.lockFD, unix.LOCK_UN); err != nil {
		result = errors.Join(result, err)
	}
	if err := unix.Close(state.lockFD); err != nil {
		result = errors.Join(result, err)
	}
	if err := unix.Close(state.directoryFD); err != nil {
		result = errors.Join(result, err)
	}
	state.lockFD, state.directoryFD = -1, -1
	return result
}

func (s *ledgerReservationState) validateLocked() error {
	if s == nil || s.closed || s.directoryFD < 3 || s.lockFD < 3 {
		return errors.New("authority ledger reservation lease closed")
	}
	var directoryNow, lockNow unix.Stat_t
	if err := unix.Fstat(s.directoryFD, &directoryNow); err != nil || !sameLedgerDirectory(directoryNow, s.directoryStat) {
		return errors.Join(errors.New("authority ledger retained directory changed"), err)
	}
	if err := unix.Fstat(s.lockFD, &lockNow); err != nil || !sameLedgerLock(lockNow, s.lockStat) {
		return errors.Join(errors.New("authority ledger retained lock changed"), err)
	}
	rebound, err := unix.Openat(s.directoryFD, ledgerLockName, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	var reboundStat unix.Stat_t
	err = unix.Fstat(rebound, &reboundStat)
	_ = unix.Close(rebound)
	if err != nil || !sameLedgerLock(reboundStat, s.lockStat) {
		return errors.Join(errors.New("authority ledger lock basename binding changed"), err)
	}
	return nil
}

func sameLedgerDirectory(first, second unix.Stat_t) bool {
	return first.Mode&unix.S_IFMT == unix.S_IFDIR && first.Nlink >= 1 && first.Dev == second.Dev && first.Ino == second.Ino &&
		first.Uid == 0 && first.Gid == 0 && first.Mode == second.Mode
}

func sameLedgerLock(first, second unix.Stat_t) bool {
	return first.Mode&unix.S_IFMT == unix.S_IFREG && first.Nlink == 1 && first.Dev == second.Dev && first.Ino == second.Ino &&
		first.Uid == 0 && first.Gid == 0 && first.Mode == second.Mode && first.Mode&0o777 == 0o600
}

func sameLedgerRecord(first, second *ca42authority.Ledger) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.Validate() == nil && second.Validate() == nil && first.RecordSHA256 == second.RecordSHA256
}

func cloneLedger(ledger *ca42authority.Ledger) *ca42authority.Ledger {
	if ledger == nil {
		return nil
	}
	copy := *ledger
	return &copy
}

// reserveLedgerAt performs the durable compare-and-swap boundary. It is kept
// separate from read-only verification so no state is changed before all
// immutable inputs have passed their trust checks.
func reserveLedgerAt(directoryFD int, expected *ca42authority.Ledger, descriptor ca42authority.Descriptor, now time.Time) (ca42authority.Ledger, bool, error) {
	lease, err := beginLedgerReservation(directoryFD, expected, descriptor, now)
	if err != nil {
		return ca42authority.Ledger{}, false, err
	}
	defer lease.close()
	return lease.commit()
}

func readCurrentLedger(directoryFD int) (*ca42authority.Ledger, error) {
	data, _, err := readRootOwnedRegularAt(directoryFD, LedgerPath, ca42authority.MaxLedgerBytes, nil)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ledger, err := ca42authority.ParseLedger(data)
	if err != nil {
		return nil, err
	}
	return &ledger, nil
}

func requireRootOwnedLock(fd int) error {
	stat, err := retainedStat(fd)
	if err != nil {
		return err
	}
	if stat.mode&unix.S_IFMT != unix.S_IFREG || stat.uid != 0 || stat.nlink != 1 || stat.mode&0o777 != 0o600 {
		return errors.New("authority ledger lock ownership or mode denied")
	}
	return nil
}

func atomicReplaceLedger(directoryFD int, data []byte) error {
	recordSHA256 := nextRecordSHA256(data)
	if recordSHA256 == ([32]byte{}) {
		return errors.New("authority ledger replacement candidate invalid")
	}
	candidateName := fmt.Sprintf("authority-ledger.candidate.%x", recordSHA256[:])
	if recovered, err := reconcileLedgerCandidate(directoryFD, candidateName, data, recordSHA256); err != nil || recovered {
		return err
	}
	tempFD, err := unix.Openat(directoryFD, ".", unix.O_RDWR|unix.O_TMPFILE|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("authority ledger anonymous candidate unavailable: %w", err)
	}
	defer unix.Close(tempFD)
	for remaining := data; len(remaining) > 0; {
		written, writeErr := unix.Write(tempFD, remaining)
		if writeErr != nil {
			return writeErr
		}
		if written <= 0 {
			return errors.New("authority ledger short write")
		}
		remaining = remaining[written:]
	}
	if err := unix.Fsync(tempFD); err != nil {
		return err
	}
	if err := unix.Fchmod(tempFD, 0o400); err != nil {
		return err
	}
	if err := unix.Fsync(tempFD); err != nil {
		return err
	}
	if err := unix.Linkat(tempFD, "", directoryFD, candidateName, unix.AT_EMPTY_PATH); err != nil {
		if errors.Is(err, unix.EEXIST) {
			if recovered, reconcileErr := reconcileLedgerCandidate(directoryFD, candidateName, data, recordSHA256); reconcileErr != nil || recovered {
				return reconcileErr
			}
		}
		return errors.Join(ErrLedgerCommitAmbiguous, fmt.Errorf("authority ledger candidate link failed: %w", err))
	}
	if err := verifyLedgerCandidate(directoryFD, candidateName, data); err != nil {
		return errors.Join(ErrLedgerCommitAmbiguous, err)
	}
	if err := unix.Renameat(directoryFD, candidateName, directoryFD, LedgerPath); err != nil {
		return errors.Join(ErrLedgerCommitAmbiguous, fmt.Errorf("authority ledger atomic rename failed: %w", err))
	}
	if err := unix.Fsync(directoryFD); err != nil {
		current, readErr := readCurrentLedger(directoryFD)
		if readErr == nil && current != nil && current.RecordSHA256 == recordSHA256 {
			if retryErr := unix.Fsync(directoryFD); retryErr == nil {
				return nil
			}
		}
		return errors.Join(ErrLedgerCommitAmbiguous, fmt.Errorf("authority ledger directory fsync failed: %w", err), readErr)
	}
	return nil
}

func reconcileLedgerCandidate(directoryFD int, candidateName string, data []byte, recordSHA256 [32]byte) (bool, error) {
	if err := verifyLedgerCandidate(directoryFD, candidateName, data); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, errors.Join(ErrLedgerCommitAmbiguous, err)
	}
	if err := unix.Renameat(directoryFD, candidateName, directoryFD, LedgerPath); err != nil {
		return false, errors.Join(ErrLedgerCommitAmbiguous, fmt.Errorf("authority ledger candidate recovery rename failed: %w", err))
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return false, errors.Join(ErrLedgerCommitAmbiguous, fmt.Errorf("authority ledger candidate recovery fsync failed: %w", err))
	}
	current, err := readCurrentLedger(directoryFD)
	if err != nil || current == nil || current.RecordSHA256 != recordSHA256 {
		return false, errors.Join(ErrLedgerCommitAmbiguous, errors.New("authority ledger candidate recovery replay mismatch"), err)
	}
	return true, nil
}

func verifyLedgerCandidate(directoryFD int, candidateName string, expected []byte) error {
	actual, _, err := readRootOwnedRegularAt(directoryFD, candidateName, ca42authority.MaxLedgerBytes, nil)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("authority ledger deterministic candidate content conflict")
	}
	return nil
}

func nextRecordSHA256(data []byte) [32]byte {
	ledger, err := ca42authority.ParseLedger(data)
	if err != nil {
		return [32]byte{}
	}
	return ledger.RecordSHA256
}
