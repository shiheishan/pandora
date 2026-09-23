//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"golang.org/x/sys/unix"
)

// ledgerRootCapability binds the authority ledger to its canonical production
// path, mount and supervisor namespaces. Its mutex remains held for the entire
// lifetime of a bound reservation so Close and a second in-process operation
// cannot invalidate the root while plan/commit owns the ledger lock.
type ledgerRootCapability struct {
	state *ledgerRootCapabilityState
}

type ledgerRootCapabilityState struct {
	mu                  sync.Mutex
	rootFD              int
	mountNSFD, userNSFD int
	rootStat            unix.Stat_t
	mountNSStat         unix.Stat_t
	userNSStat          unix.Stat_t
	rootMountID         uint64
	absolutePath        string
	closed              bool
}

type boundLedgerReservation struct {
	state *boundLedgerReservationState
}

type boundLedgerReservationState struct {
	mu         sync.Mutex
	capability *ledgerRootCapability
	lease      *ledgerReservationLease
	closed     bool
}

func openProductionLedgerRootCapability() (*ledgerRootCapability, error) {
	return openLedgerRootCapability(LedgerRootPath)
}

func openLedgerRootCapability(absolutePath string) (*ledgerRootCapability, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if os.Geteuid() != 0 {
		return nil, errors.New("authority ledger production root requires effective UID 0")
	}
	root, err := openFixedTrustedDirectory(absolutePath)
	if err != nil {
		return nil, err
	}
	rootFD := int(root.Fd())
	defer root.Close()
	var rootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil {
		return nil, err
	}
	retainedFD, err := unix.Openat(rootFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	closeRetained := true
	defer func() {
		if closeRetained {
			_ = unix.Close(retainedFD)
		}
	}()
	var retainedStat unix.Stat_t
	if err := unix.Fstat(retainedFD, &retainedStat); err != nil || !sameLedgerRoot(retainedStat, rootStat) {
		return nil, errors.Join(errors.New("authority ledger retained root reopen changed"), err)
	}
	rootFD, rootStat = retainedFD, retainedStat
	mountNSFD, mountNSStat, userNSFD, userNSStat, err := openLedgerSupervisorNamespaces()
	if err != nil {
		return nil, err
	}
	closeNamespaces := true
	defer func() {
		if closeNamespaces {
			_ = unix.Close(mountNSFD)
			_ = unix.Close(userNSFD)
		}
	}()
	mountID, err := ledgerMountID(rootFD)
	if err != nil {
		return nil, err
	}
	capability := &ledgerRootCapability{state: &ledgerRootCapabilityState{rootFD: rootFD, mountNSFD: mountNSFD, userNSFD: userNSFD,
		rootStat: rootStat, mountNSStat: mountNSStat, userNSStat: userNSStat,
		rootMountID: mountID, absolutePath: absolutePath}}
	if err := capability.validateLocked(); err != nil {
		return nil, err
	}
	closeRetained, closeNamespaces = false, false
	return capability, nil
}

func (c *ledgerRootCapability) beginReservation(expected *ca42authority.Ledger, descriptor ca42authority.Descriptor, now time.Time) (*boundLedgerReservation, error) {
	bound, err := c.acquireReservation(expected)
	if err != nil {
		return nil, err
	}
	if _, _, err := bound.plan(descriptor, now); err != nil {
		_ = bound.close()
		return nil, err
	}
	return bound, nil
}

// beginCurrentReservation derives the CAS expectation only from the retained
// canonical ledger root. No production caller can inject a ledger snapshot.
func (c *ledgerRootCapability) beginCurrentReservation(descriptor ca42authority.Descriptor, now time.Time) (*boundLedgerReservation, error) {
	return c.beginCurrentReservationWithHook(descriptor, now, nil)
}

func (c *ledgerRootCapability) beginCurrentReservationWithHook(descriptor ca42authority.Descriptor, now time.Time, afterSnapshot func() error) (*boundLedgerReservation, error) {
	if c == nil || c.state == nil {
		return nil, errors.New("authority ledger production root closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	state := c.state
	if !state.mu.TryLock() {
		return nil, ErrLedgerBusy
	}
	unlock := true
	defer func() {
		if unlock {
			state.mu.Unlock()
		}
	}()
	if err := c.validateLocked(); err != nil {
		return nil, err
	}
	expected, err := readCurrentLedger(state.rootFD)
	if err != nil {
		return nil, err
	}
	if afterSnapshot != nil {
		if err := afterSnapshot(); err != nil {
			return nil, err
		}
	}
	lease, err := acquireLedgerReservation(state.rootFD, expected)
	if err != nil {
		return nil, err
	}
	if err := c.validateLocked(); err != nil {
		_ = lease.close()
		return nil, err
	}
	bound := &boundLedgerReservation{state: &boundLedgerReservationState{capability: c, lease: lease}}
	unlock = false
	if _, _, err := bound.plan(descriptor, now); err != nil {
		_ = bound.close()
		return nil, err
	}
	return bound, nil
}

func (c *ledgerRootCapability) acquireReservation(expected *ca42authority.Ledger) (*boundLedgerReservation, error) {
	if c == nil || c.state == nil {
		return nil, errors.New("authority ledger production root closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	state := c.state
	if !state.mu.TryLock() {
		return nil, ErrLedgerBusy
	}
	unlock := true
	defer func() {
		if unlock {
			state.mu.Unlock()
		}
	}()
	if err := c.validateLocked(); err != nil {
		return nil, err
	}
	lease, err := acquireLedgerReservation(state.rootFD, expected)
	if err != nil {
		return nil, err
	}
	if err := c.validateLocked(); err != nil {
		_ = lease.close()
		return nil, err
	}
	boundState := &boundLedgerReservationState{capability: c, lease: lease}
	unlock = false
	return &boundLedgerReservation{state: boundState}, nil
}

func (b *boundLedgerReservation) plan(descriptor ca42authority.Descriptor, reservedAt time.Time) (ca42authority.Ledger, bool, error) {
	if b == nil || b.state == nil {
		return ca42authority.Ledger{}, false, errors.New("bound authority ledger reservation closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	state := b.state
	if state.closed || state.capability == nil || state.lease == nil {
		return ca42authority.Ledger{}, false, errors.New("bound authority ledger reservation closed")
	}
	if err := state.capability.validateLocked(); err != nil {
		return ca42authority.Ledger{}, false, err
	}
	planned, exact, err := state.lease.plan(descriptor, reservedAt)
	if err != nil {
		return ca42authority.Ledger{}, false, err
	}
	if err := state.capability.validateLocked(); err != nil {
		return ca42authority.Ledger{}, false, err
	}
	return planned, exact, nil
}

func (b *boundLedgerReservation) plannedLedger() (ca42authority.Ledger, bool, error) {
	if b == nil || b.state == nil {
		return ca42authority.Ledger{}, false, errors.New("bound authority ledger reservation closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	state := b.state
	if state.closed || state.capability == nil || state.lease == nil {
		return ca42authority.Ledger{}, false, errors.New("bound authority ledger reservation closed")
	}
	if err := state.capability.validateLocked(); err != nil {
		return ca42authority.Ledger{}, false, err
	}
	planned, exact, err := state.lease.plannedLedger()
	if err != nil {
		return ca42authority.Ledger{}, false, err
	}
	if err := state.capability.validateLocked(); err != nil {
		return ca42authority.Ledger{}, false, err
	}
	return planned, exact, nil
}

func (b *boundLedgerReservation) commit() (ca42authority.Ledger, bool, error) {
	if b == nil || b.state == nil {
		return ca42authority.Ledger{}, false, errors.New("bound authority ledger reservation closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	state := b.state
	if state.closed || state.capability == nil || state.lease == nil {
		return ca42authority.Ledger{}, false, errors.New("bound authority ledger reservation closed")
	}
	if err := state.capability.validateLocked(); err != nil {
		return ca42authority.Ledger{}, false, err
	}
	committed, recovered, err := state.lease.commit()
	if err != nil {
		return ca42authority.Ledger{}, false, err
	}
	if err := state.capability.validateLocked(); err != nil {
		return ca42authority.Ledger{}, false, errors.Join(ErrLedgerCommitAmbiguous, err)
	}
	return committed, recovered, nil
}

func (b *boundLedgerReservation) close() error {
	if b == nil || b.state == nil {
		return nil
	}
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	state := b.state
	if state.closed {
		return nil
	}
	state.closed = true
	var result error
	if state.lease != nil {
		result = errors.Join(result, state.lease.close())
		state.lease = nil
	}
	capability := state.capability
	state.capability = nil
	if capability != nil {
		capability.state.mu.Unlock()
	}
	return result
}

func (c *ledgerRootCapability) validate() error {
	if c == nil || c.state == nil {
		return errors.New("authority ledger production root closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	state := c.state
	if !state.mu.TryLock() {
		return ErrLedgerBusy
	}
	defer state.mu.Unlock()
	return c.validateLocked()
}

func (c *ledgerRootCapability) validateLocked() error {
	if c == nil || c.state == nil {
		return errors.New("authority ledger production root closed")
	}
	state := c.state
	if state.closed || state.rootFD < 3 || state.mountNSFD < 3 || state.userNSFD < 3 {
		return errors.New("authority ledger production root closed")
	}
	var rootNow, mountNSNow, userNSNow unix.Stat_t
	if err := unix.Fstat(state.mountNSFD, &mountNSNow); err != nil || !sameLedgerNamespace(mountNSNow, state.mountNSStat) {
		return errors.Join(errors.New("authority ledger mount namespace changed"), err)
	}
	if err := unix.Fstat(state.userNSFD, &userNSNow); err != nil || !sameLedgerNamespace(userNSNow, state.userNSStat) {
		return errors.Join(errors.New("authority ledger user namespace changed"), err)
	}
	currentMountFD, currentMountStat, currentUserFD, currentUserStat, err := openLedgerSupervisorNamespaces()
	if err != nil {
		return err
	}
	_ = unix.Close(currentMountFD)
	_ = unix.Close(currentUserFD)
	if !sameLedgerNamespace(currentMountStat, state.mountNSStat) || !sameLedgerNamespace(currentUserStat, state.userNSStat) {
		return errors.New("authority ledger supervisor namespace binding changed")
	}
	if err := unix.Fstat(state.rootFD, &rootNow); err != nil || !sameLedgerRoot(rootNow, state.rootStat) {
		return errors.Join(errors.New("authority ledger retained root changed"), err)
	}
	mountID, err := ledgerMountID(state.rootFD)
	if err != nil || mountID != state.rootMountID {
		return errors.Join(errors.New("authority ledger retained mount changed"), err)
	}
	rebound, err := openFixedTrustedDirectory(state.absolutePath)
	if err != nil {
		return err
	}
	defer rebound.Close()
	var reboundStat unix.Stat_t
	if err := unix.Fstat(int(rebound.Fd()), &reboundStat); err != nil || !sameLedgerRoot(reboundStat, state.rootStat) {
		return errors.Join(errors.New("authority ledger canonical path binding changed"), err)
	}
	reboundMountID, err := ledgerMountID(int(rebound.Fd()))
	if err != nil || reboundMountID != state.rootMountID {
		return errors.Join(errors.New("authority ledger canonical mount binding changed"), err)
	}
	return nil
}

func openLedgerSupervisorNamespaces() (int, unix.Stat_t, int, unix.Stat_t, error) {
	mountFD, mountStat, err := openLedgerNamespace("/proc/thread-self/ns/mnt")
	if err != nil {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, err
	}
	closeMount := true
	defer func() {
		if closeMount {
			_ = unix.Close(mountFD)
		}
	}()
	pidMountFD, pidMountStat, err := openLedgerNamespace("/proc/1/ns/mnt")
	if err != nil {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, err
	}
	_ = unix.Close(pidMountFD)
	if !sameLedgerNamespace(mountStat, pidMountStat) {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, errors.New("authority ledger supervisor mount namespace mismatch")
	}
	userFD, userStat, err := openLedgerNamespace("/proc/thread-self/ns/user")
	if err != nil {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, err
	}
	closeUser := true
	defer func() {
		if closeUser {
			_ = unix.Close(userFD)
		}
	}()
	pidUserFD, pidUserStat, err := openLedgerNamespace("/proc/1/ns/user")
	if err != nil {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, err
	}
	_ = unix.Close(pidUserFD)
	if !sameLedgerNamespace(userStat, pidUserStat) {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, errors.New("authority ledger supervisor user namespace mismatch")
	}
	closeMount, closeUser = false, false
	return mountFD, mountStat, userFD, userStat, nil
}

func openLedgerNamespace(path string) (int, unix.Stat_t, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, stat, err
	}
	return fd, stat, nil
}

func ledgerMountID(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return 0, errors.New("authority ledger mount identity missing")
	}
	return stat.Mnt_id, nil
}

func sameLedgerNamespace(actual, expected unix.Stat_t) bool {
	return actual.Dev == expected.Dev && actual.Ino == expected.Ino
}

func sameLedgerRoot(actual, expected unix.Stat_t) bool {
	return actual.Mode&unix.S_IFMT == unix.S_IFDIR && actual.Nlink >= 1 && actual.Dev == expected.Dev && actual.Ino == expected.Ino &&
		actual.Uid == 0 && actual.Gid == 0 && actual.Mode == expected.Mode && actual.Mode&0o7777 == 0o700
}

func (c *ledgerRootCapability) close() error {
	if c == nil || c.state == nil {
		return nil
	}
	state := c.state
	if !state.mu.TryLock() {
		return ErrLedgerBusy
	}
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	state.closed = true
	var result error
	for _, fd := range []int{state.rootFD, state.mountNSFD, state.userNSFD} {
		if fd >= 0 {
			result = errors.Join(result, unix.Close(fd))
		}
	}
	state.rootFD, state.mountNSFD, state.userNSFD = -1, -1, -1
	return result
}
