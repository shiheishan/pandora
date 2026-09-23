//go:build linux && (amd64 || arm64)

package ca42nonce

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	productionNonceParentPath = "/var/lib/pandora"
	productionNonceRootName   = "ca42-global-nonce-v1"
)

// ProductionStore is an opaque production namespace and lock capability. It
// intentionally exposes no reservation mutation API yet; the future runner
// bridge must consume live opaque claim/journal/ledger capabilities rather
// than caller-provided paths, snapshots, hashes or record fields.
type ProductionStore struct {
	lease      *productionStoreLease
	capability *nonceRootCapability
	store      *retainedStore
}

type productionStoreLease struct {
	mu       sync.Mutex
	closed   bool
	poisoned error
}

type nonceRootCapability struct {
	state *nonceRootCapabilityState
}

type nonceRootCapabilityState struct {
	mu                         sync.Mutex
	parentFD, rootFD           int
	mountNSFD, userNSFD        int
	parentStat, rootStat       unix.Stat_t
	mountNSStat, userNSStat    unix.Stat_t
	parentMountID, rootMountID uint64
	parentPath, rootName       string
	closed                     bool
}

// OpenProductionStore is the only production nonce-root opener. The fixed
// path is not created by this function and cannot be overridden by a caller.
func OpenProductionStore() (*ProductionStore, error) {
	capability, err := openNonceRootCapability(productionNonceParentPath, productionNonceRootName)
	if err != nil {
		return nil, err
	}
	store, err := capability.openStore()
	if err != nil {
		_ = capability.close()
		return nil, err
	}
	return &ProductionStore{lease: &productionStoreLease{}, capability: capability, store: store}, nil
}

func openNonceRootCapability(parentPath, rootName string) (*nonceRootCapability, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if os.Geteuid() != 0 {
		return nil, errors.New("CA42 nonce production root requires effective UID 0")
	}
	if !filepath.IsAbs(parentPath) || filepath.Clean(parentPath) != parentPath || parentPath == "/" ||
		strings.Contains(parentPath, "//") || !safeNoncePathComponent(rootName) {
		return nil, errors.New("CA42 nonce production root path invalid")
	}
	parentFD, parentStat, err := openCanonicalNonceParent(parentPath)
	if err != nil {
		return nil, err
	}
	closeParent := true
	defer func() {
		if closeParent {
			_ = unix.Close(parentFD)
		}
	}()
	rootFD, rootStat, err := openNonceRootAt(parentFD, rootName, uint64(parentStat.Dev))
	if err != nil {
		return nil, err
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = unix.Close(rootFD)
		}
	}()
	mountNSFD, mountNSStat, userNSFD, userNSStat, err := openNonceSupervisorNamespaces()
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
	parentMountID, err := nonceMountID(parentFD)
	if err != nil {
		return nil, err
	}
	rootMountID, err := nonceMountID(rootFD)
	if err != nil || rootMountID != parentMountID {
		return nil, errors.Join(errors.New("CA42 nonce production root mount mismatch"), err)
	}
	capability := &nonceRootCapability{state: &nonceRootCapabilityState{
		parentFD: parentFD, rootFD: rootFD, mountNSFD: mountNSFD, userNSFD: userNSFD,
		parentStat: parentStat, rootStat: rootStat, mountNSStat: mountNSStat, userNSStat: userNSStat,
		parentMountID: parentMountID, rootMountID: rootMountID, parentPath: parentPath, rootName: rootName,
	}}
	if err := capability.validateLocked(); err != nil {
		return nil, err
	}
	closeParent, closeRoot, closeNamespaces = false, false, false
	return capability, nil
}

func (c *nonceRootCapability) openStore() (*retainedStore, error) {
	if c == nil || c.state == nil {
		return nil, errors.New("CA42 nonce production root closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	state := c.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := c.validateLocked(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(state.rootFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), state.rootName)
	if root == nil {
		_ = unix.Close(fd)
		return nil, errors.New("CA42 nonce production root descriptor conversion failed")
	}
	defer root.Close()
	store, err := openRetainedStore(root, nonceStoreHooks{})
	if err != nil {
		return nil, err
	}
	if err := c.validateLocked(); err != nil {
		_ = store.close()
		return nil, err
	}
	return store, nil
}

func (s *ProductionStore) validate() error {
	if s == nil || s.lease == nil {
		return errors.New("CA42 nonce production store closed")
	}
	if !s.lease.mu.TryLock() {
		return ErrStoreBusy
	}
	defer s.lease.mu.Unlock()
	if s.lease.closed || s.capability == nil || s.store == nil {
		return errors.New("CA42 nonce production store closed")
	}
	if s.lease.poisoned != nil {
		return s.lease.poisoned
	}
	if err := s.capability.validate(); err != nil {
		return err
	}
	s.store.lease.mu.Lock()
	defer s.store.lease.mu.Unlock()
	return s.store.validateLocked()
}

func (s *ProductionStore) Close() error {
	if s == nil || s.lease == nil {
		return nil
	}
	if !s.lease.mu.TryLock() {
		return ErrStoreBusy
	}
	defer s.lease.mu.Unlock()
	if s.lease.closed {
		return nil
	}
	if s.store != nil {
		if err := s.store.close(); err != nil {
			return err
		}
		s.store = nil
	}
	s.lease.closed = true
	var result error
	if s.capability != nil {
		result = errors.Join(result, s.capability.close())
		s.capability = nil
	}
	return result
}

func (c *nonceRootCapability) validate() error {
	if c == nil || c.state == nil {
		return errors.New("CA42 nonce production root closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	state := c.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return c.validateLocked()
}

func (c *nonceRootCapability) validateLocked() error {
	if c == nil || c.state == nil {
		return errors.New("CA42 nonce production root closed")
	}
	state := c.state
	if state.closed || state.parentFD < 3 || state.rootFD < 3 || state.mountNSFD < 3 || state.userNSFD < 3 {
		return errors.New("CA42 nonce production root closed")
	}
	var parentNow, rootNow, mountNSNow, userNSNow unix.Stat_t
	if err := unix.Fstat(state.mountNSFD, &mountNSNow); err != nil || !sameNonceIdentity(mountNSNow, state.mountNSStat) {
		return errors.Join(errors.New("CA42 nonce mount namespace changed"), err)
	}
	if err := unix.Fstat(state.userNSFD, &userNSNow); err != nil || !sameNonceIdentity(userNSNow, state.userNSStat) {
		return errors.Join(errors.New("CA42 nonce user namespace changed"), err)
	}
	currentMountFD, currentMountStat, currentUserFD, currentUserStat, err := openNonceSupervisorNamespaces()
	if err != nil {
		return err
	}
	_ = unix.Close(currentMountFD)
	_ = unix.Close(currentUserFD)
	if !sameNonceIdentity(currentMountStat, state.mountNSStat) || !sameNonceIdentity(currentUserStat, state.userNSStat) {
		return errors.New("CA42 nonce supervisor namespace binding changed")
	}
	if err := unix.Fstat(state.parentFD, &parentNow); err != nil || !sameNonceDirectoryUnix(parentNow, state.parentStat) {
		return errors.Join(errors.New("CA42 nonce canonical parent changed"), err)
	}
	if err := unix.Fstat(state.rootFD, &rootNow); err != nil || !sameNonceDirectoryUnix(rootNow, state.rootStat) {
		return errors.Join(errors.New("CA42 nonce canonical root changed"), err)
	}
	parentMountID, err := nonceMountID(state.parentFD)
	if err != nil || parentMountID != state.parentMountID {
		return errors.Join(errors.New("CA42 nonce parent mount changed"), err)
	}
	rootMountID, err := nonceMountID(state.rootFD)
	if err != nil || rootMountID != state.rootMountID || rootMountID != parentMountID {
		return errors.Join(errors.New("CA42 nonce root mount changed"), err)
	}
	reboundParentFD, reboundParentStat, err := openCanonicalNonceParent(state.parentPath)
	if err != nil {
		return err
	}
	defer unix.Close(reboundParentFD)
	if !sameNonceDirectoryUnix(reboundParentStat, state.parentStat) {
		return errors.New("CA42 nonce canonical parent binding changed")
	}
	reboundRootFD, reboundRootStat, err := openNonceRootAt(reboundParentFD, state.rootName, uint64(state.parentStat.Dev))
	if err != nil {
		return err
	}
	defer unix.Close(reboundRootFD)
	if !sameNonceDirectoryUnix(reboundRootStat, state.rootStat) {
		return errors.New("CA42 nonce canonical root basename changed")
	}
	reboundMountID, err := nonceMountID(reboundRootFD)
	if err != nil || reboundMountID != state.rootMountID {
		return errors.Join(errors.New("CA42 nonce canonical root mount binding changed"), err)
	}
	return nil
}

func openCanonicalNonceParent(path string) (int, unix.Stat_t, error) {
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := validateNonceAncestor(current, false, &stat); err != nil {
		_ = unix.Close(current)
		return -1, stat, err
	}
	for index, component := range components {
		if !safeNoncePathComponent(component) {
			_ = unix.Close(current)
			return -1, stat, errors.New("CA42 nonce canonical parent component invalid")
		}
		next, openErr := nonceOpenat2(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, stat, openErr
		}
		current = next
		if err := validateNonceAncestor(current, index == len(components)-1, &stat); err != nil {
			_ = unix.Close(current)
			return -1, stat, err
		}
	}
	return current, stat, nil
}

func validateNonceAncestor(fd int, final bool, stat *unix.Stat_t) error {
	if err := unix.Fstat(fd, stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink < 1 || stat.Mode&0022 != 0 {
		return errors.New("CA42 nonce canonical parent trust invalid")
	}
	if final && stat.Mode&07777 != 0700 {
		return errors.New("CA42 nonce canonical parent mode invalid")
	}
	return nil
}

func openNonceRootAt(parentFD int, name string, device uint64) (int, unix.Stat_t, error) {
	fd, err := nonceOpenat2(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 ||
		stat.Nlink < 1 || stat.Mode&07777 != 0700 || uint64(stat.Dev) != device {
		_ = unix.Close(fd)
		return -1, stat, errors.Join(errors.New("CA42 nonce production root identity invalid"), err)
	}
	return fd, stat, nil
}

func nonceOpenat2(parentFD int, name string, flags int, mode uint32) (int, error) {
	how := &unix.OpenHow{Flags: uint64(flags), Mode: uint64(mode), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS}
	return unix.Openat2(parentFD, name, how)
}

func openNonceSupervisorNamespaces() (int, unix.Stat_t, int, unix.Stat_t, error) {
	threadMountFD, threadMountStat, err := openNonceNamespace("/proc/thread-self/ns/mnt")
	if err != nil {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, err
	}
	closeMount := true
	defer func() {
		if closeMount {
			_ = unix.Close(threadMountFD)
		}
	}()
	pidMountFD, pidMountStat, err := openNonceNamespace("/proc/1/ns/mnt")
	if err != nil {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, err
	}
	_ = unix.Close(pidMountFD)
	if !sameNonceIdentity(threadMountStat, pidMountStat) {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, errors.New("CA42 nonce supervisor mount namespace mismatch")
	}
	threadUserFD, threadUserStat, err := openNonceNamespace("/proc/thread-self/ns/user")
	if err != nil {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, err
	}
	closeUser := true
	defer func() {
		if closeUser {
			_ = unix.Close(threadUserFD)
		}
	}()
	pidUserFD, pidUserStat, err := openNonceNamespace("/proc/1/ns/user")
	if err != nil {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, err
	}
	_ = unix.Close(pidUserFD)
	if !sameNonceIdentity(threadUserStat, pidUserStat) {
		return -1, unix.Stat_t{}, -1, unix.Stat_t{}, errors.New("CA42 nonce supervisor user namespace mismatch")
	}
	closeMount, closeUser = false, false
	return threadMountFD, threadMountStat, threadUserFD, threadUserStat, nil
}

func openNonceNamespace(path string) (int, unix.Stat_t, error) {
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

func nonceMountID(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return 0, errors.New("CA42 nonce mount identity missing")
	}
	return stat.Mnt_id, nil
}

func safeNoncePathComponent(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00\r\n")
}

func sameNonceIdentity(actual, expected unix.Stat_t) bool {
	return actual.Dev == expected.Dev && actual.Ino == expected.Ino
}

func sameNonceDirectoryUnix(actual, expected unix.Stat_t) bool {
	return actual.Mode&unix.S_IFMT == unix.S_IFDIR && actual.Nlink >= 1 && actual.Dev == expected.Dev && actual.Ino == expected.Ino &&
		actual.Uid == expected.Uid && actual.Gid == expected.Gid && actual.Mode == expected.Mode
}

func (c *nonceRootCapability) close() error {
	if c == nil || c.state == nil {
		return nil
	}
	state := c.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	state.closed = true
	var result error
	for _, fd := range []int{state.rootFD, state.parentFD, state.mountNSFD, state.userNSFD} {
		if fd >= 0 {
			result = errors.Join(result, unix.Close(fd))
		}
	}
	state.rootFD, state.parentFD, state.mountNSFD, state.userNSFD = -1, -1, -1, -1
	return result
}
