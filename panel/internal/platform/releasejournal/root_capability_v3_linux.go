//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	productionV3ParentPath = "/var/lib/pandora"
	productionV3RootName   = "release-journal-v3"
)

// v3RootCapability retains the supervisor's canonical parent and the exact
// dedicated Journal v3 root. It grants read/session authority only; mutation
// remains package-private until nonce and authority-ledger capabilities can be
// consumed under the global coordinator lock order.
type v3RootCapability struct {
	mu            sync.Mutex
	parentFD      int
	rootFD        int
	parentStat    syscall.Stat_t
	rootStat      syscall.Stat_t
	mountNSStat   syscall.Stat_t
	userNSStat    syscall.Stat_t
	parentMountID uint64
	rootMountID   uint64
	mountNSFD     int
	userNSFD      int
	parentPath    string
	rootName      string
	allowed       map[uint64]struct{}
	closed        bool
}

type v3RootCapabilityHooks struct {
	afterParentOpen func() error
	afterRootOpen   func() error
	beforeReturn    func() error
}

// OpenProductionV3Session is the only exported production opener. It accepts
// no ambient path, basename, device list, or caller descriptor. The returned
// session owns the private canonical parent/root capability until Close.
func OpenProductionV3Session(attemptID string) (*V3Session, error) {
	capability, err := openV3RootCapabilityWithHooks(productionV3ParentPath, productionV3RootName, v3RootCapabilityHooks{})
	if err != nil {
		return nil, err
	}
	session, err := capability.openOwnedSession(attemptID)
	if err != nil {
		_ = capability.close()
		return nil, err
	}
	return session, nil
}

func openV3RootCapability(parentPath, rootName string) (*v3RootCapability, error) {
	return openV3RootCapabilityWithHooks(parentPath, rootName, v3RootCapabilityHooks{})
}

func openV3RootCapabilityWithHooks(parentPath, rootName string, hooks v3RootCapabilityHooks) (*v3RootCapability, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if os.Geteuid() != 0 {
		return nil, deny("journal_v3_root_capability_euid_zero_required")
	}
	if !filepath.IsAbs(parentPath) || filepath.Clean(parentPath) != parentPath || parentPath == "/" ||
		strings.Contains(parentPath, "//") || !safePathComponent(rootName) {
		return nil, deny("journal_v3_root_capability_path_invalid")
	}
	parentFD, parentStat, err := openCanonicalV3Parent(parentPath)
	if err != nil {
		return nil, err
	}
	closeParent := true
	defer func() {
		if closeParent {
			_ = syscall.Close(parentFD)
		}
	}()
	if hooks.afterParentOpen != nil {
		if err := hooks.afterParentOpen(); err != nil {
			return nil, denyErr("journal_v3_root_capability_after_parent_open_failed", err)
		}
	}
	allowed := map[uint64]struct{}{uint64(parentStat.Dev): {}}
	rootFD, rootStat, err := openJournalDirectory(parentFD, rootName, uint64(parentStat.Dev), allowed)
	if err != nil {
		return nil, denyErr("journal_v3_root_capability_root_open_failed", err)
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = syscall.Close(rootFD)
		}
	}()
	if hooks.afterRootOpen != nil {
		if err := hooks.afterRootOpen(); err != nil {
			return nil, denyErr("journal_v3_root_capability_after_root_open_failed", err)
		}
	}
	mountNSFD, mountNSStat, userNSFD, userNSStat, err := openAndVerifyV3SupervisorNamespaces()
	if err != nil {
		return nil, err
	}
	closeNamespaces := true
	defer func() {
		if closeNamespaces {
			_ = syscall.Close(mountNSFD)
			_ = syscall.Close(userNSFD)
		}
	}()
	parentMountID, err := v3MountID(parentFD)
	if err != nil {
		return nil, err
	}
	rootMountID, err := v3MountID(rootFD)
	if err != nil {
		return nil, err
	}
	if parentMountID != rootMountID {
		return nil, deny("journal_v3_root_capability_root_mount_mismatch")
	}
	capability := &v3RootCapability{parentFD: parentFD, rootFD: rootFD, parentStat: parentStat, rootStat: rootStat,
		mountNSFD: mountNSFD, mountNSStat: mountNSStat, parentMountID: parentMountID, rootMountID: rootMountID,
		userNSFD: userNSFD, userNSStat: userNSStat,
		parentPath: parentPath, rootName: rootName, allowed: allowed}
	if hooks.beforeReturn != nil {
		if err := hooks.beforeReturn(); err != nil {
			return nil, denyErr("journal_v3_root_capability_before_return_failed", err)
		}
	}
	if err := capability.validateLocked(); err != nil {
		return nil, err
	}
	closeParent, closeRoot, closeNamespaces = false, false, false
	return capability, nil
}

func openV3Namespace(path, reason string) (int, syscall.Stat_t, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, denyErr(reason+"_open_failed", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = syscall.Close(fd)
		return -1, stat, denyErr(reason+"_fstat_failed", err)
	}
	return fd, stat, nil
}

func openAndVerifyV3SupervisorNamespaces() (int, syscall.Stat_t, int, syscall.Stat_t, error) {
	threadMountFD, threadMountStat, err := openV3Namespace("/proc/thread-self/ns/mnt", "journal_v3_root_capability_mount_namespace")
	if err != nil {
		return -1, syscall.Stat_t{}, -1, syscall.Stat_t{}, err
	}
	closeThreadMount := true
	defer func() {
		if closeThreadMount {
			_ = syscall.Close(threadMountFD)
		}
	}()
	supervisorMountFD, supervisorMountStat, err := openV3Namespace("/proc/1/ns/mnt", "journal_v3_root_capability_supervisor_mount_namespace")
	if err != nil {
		return -1, syscall.Stat_t{}, -1, syscall.Stat_t{}, err
	}
	_ = syscall.Close(supervisorMountFD)
	if !sameFileIdentity(threadMountStat, supervisorMountStat) {
		return -1, syscall.Stat_t{}, -1, syscall.Stat_t{}, deny("journal_v3_root_capability_supervisor_mount_namespace_mismatch")
	}
	threadUserFD, threadUserStat, err := openV3Namespace("/proc/thread-self/ns/user", "journal_v3_root_capability_user_namespace")
	if err != nil {
		return -1, syscall.Stat_t{}, -1, syscall.Stat_t{}, err
	}
	closeThreadUser := true
	defer func() {
		if closeThreadUser {
			_ = syscall.Close(threadUserFD)
		}
	}()
	supervisorUserFD, supervisorUserStat, err := openV3Namespace("/proc/1/ns/user", "journal_v3_root_capability_supervisor_user_namespace")
	if err != nil {
		return -1, syscall.Stat_t{}, -1, syscall.Stat_t{}, err
	}
	_ = syscall.Close(supervisorUserFD)
	if !sameFileIdentity(threadUserStat, supervisorUserStat) {
		return -1, syscall.Stat_t{}, -1, syscall.Stat_t{}, deny("journal_v3_root_capability_supervisor_user_namespace_mismatch")
	}
	closeThreadMount, closeThreadUser = false, false
	return threadMountFD, threadMountStat, threadUserFD, threadUserStat, nil
}

func v3MountID(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, denyErr("journal_v3_root_capability_mount_identity_failed", err)
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return 0, deny("journal_v3_root_capability_mount_identity_missing")
	}
	return stat.Mnt_id, nil
}

func openCanonicalV3Parent(path string) (int, syscall.Stat_t, error) {
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current, err := syscall.Open("/", syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, denyErr("journal_v3_root_capability_anchor_open_failed", err)
	}
	var stat syscall.Stat_t
	if err := validateCanonicalV3Ancestor(current, false, &stat); err != nil {
		_ = syscall.Close(current)
		return -1, stat, err
	}
	for index, component := range components {
		if !safePathComponent(component) {
			_ = syscall.Close(current)
			return -1, stat, deny("journal_v3_root_capability_parent_component_invalid")
		}
		next, openErr := openAt2(current, component, syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
		_ = syscall.Close(current)
		if openErr != nil {
			return -1, stat, denyErr("journal_v3_root_capability_parent_open_failed", openErr)
		}
		current = next
		if err := validateCanonicalV3Ancestor(current, index == len(components)-1, &stat); err != nil {
			_ = syscall.Close(current)
			return -1, stat, err
		}
	}
	return current, stat, nil
}

func validateCanonicalV3Ancestor(fd int, final bool, stat *syscall.Stat_t) error {
	if err := syscall.Fstat(fd, stat); err != nil {
		return denyErr("journal_v3_root_capability_parent_fstat_failed", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink < 1 || stat.Mode&0022 != 0 {
		return deny("journal_v3_root_capability_parent_trust_invalid")
	}
	if final && stat.Mode&07777 != 0700 {
		return deny("journal_v3_root_capability_parent_mode_not_0700")
	}
	return nil
}

// Validate proves both retained identities, the canonical absolute parent
// binding, and the root basename binding without exposing either descriptor.
func (c *v3RootCapability) validate() error {
	if c == nil {
		return errors.New("release journal v3 root capability closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.validateLocked()
}

func (c *v3RootCapability) validateLocked() error {
	if c == nil || c.closed || c.parentFD < 3 || c.rootFD < 3 || c.mountNSFD < 3 || c.userNSFD < 3 {
		return errors.New("release journal v3 root capability closed")
	}
	var parentNow, rootNow syscall.Stat_t
	var mountNSNow syscall.Stat_t
	if err := syscall.Fstat(c.mountNSFD, &mountNSNow); err != nil || !sameFileIdentity(mountNSNow, c.mountNSStat) {
		return denyErr("journal_v3_root_capability_mount_namespace_changed", err)
	}
	var userNSNow syscall.Stat_t
	if err := syscall.Fstat(c.userNSFD, &userNSNow); err != nil || !sameFileIdentity(userNSNow, c.userNSStat) {
		return denyErr("journal_v3_root_capability_user_namespace_changed", err)
	}
	currentMountNSFD, currentMountNSStat, currentUserNSFD, currentUserNSStat, err := openAndVerifyV3SupervisorNamespaces()
	if err != nil {
		return err
	}
	_ = syscall.Close(currentMountNSFD)
	_ = syscall.Close(currentUserNSFD)
	if !sameFileIdentity(currentMountNSStat, c.mountNSStat) {
		return deny("journal_v3_root_capability_mount_namespace_binding_changed")
	}
	if !sameFileIdentity(currentUserNSStat, c.userNSStat) {
		return deny("journal_v3_root_capability_user_namespace_binding_changed")
	}
	if err := syscall.Fstat(c.parentFD, &parentNow); err != nil || !sameV3DirectoryAuthority(parentNow, c.parentStat) {
		return denyErr("journal_v3_root_capability_parent_identity_changed", err)
	}
	if err := syscall.Fstat(c.rootFD, &rootNow); err != nil || !sameV3DirectoryAuthority(rootNow, c.rootStat) {
		return denyErr("journal_v3_root_capability_root_identity_changed", err)
	}
	parentMountID, err := v3MountID(c.parentFD)
	if err != nil || parentMountID != c.parentMountID {
		return denyErr("journal_v3_root_capability_parent_mount_changed", err)
	}
	rootMountID, err := v3MountID(c.rootFD)
	if err != nil || rootMountID != c.rootMountID || rootMountID != parentMountID {
		return denyErr("journal_v3_root_capability_root_mount_changed", err)
	}
	reboundParentFD, reboundParentStat, err := openCanonicalV3Parent(c.parentPath)
	if err != nil {
		return denyErr("journal_v3_root_capability_parent_binding_missing", err)
	}
	defer syscall.Close(reboundParentFD)
	if !sameV3DirectoryAuthority(reboundParentStat, c.parentStat) {
		return deny("journal_v3_root_capability_parent_binding_changed")
	}
	reboundParentMountID, err := v3MountID(reboundParentFD)
	if err != nil || reboundParentMountID != c.parentMountID {
		return denyErr("journal_v3_root_capability_parent_mount_binding_changed", err)
	}
	retainedRootFD, retainedRootStat, err := openJournalDirectory(c.parentFD, c.rootName, uint64(c.parentStat.Dev), c.allowed)
	if err != nil {
		return denyErr("journal_v3_root_capability_retained_root_binding_missing", err)
	}
	_ = syscall.Close(retainedRootFD)
	if !sameV3DirectoryAuthority(retainedRootStat, c.rootStat) {
		return deny("journal_v3_root_capability_retained_root_binding_changed")
	}
	reboundRootFD, reboundRootStat, err := openJournalDirectory(reboundParentFD, c.rootName, uint64(c.parentStat.Dev), c.allowed)
	if err != nil {
		return denyErr("journal_v3_root_capability_canonical_root_binding_missing", err)
	}
	defer syscall.Close(reboundRootFD)
	if !sameV3DirectoryAuthority(reboundRootStat, c.rootStat) {
		return deny("journal_v3_root_capability_canonical_root_binding_changed")
	}
	reboundRootMountID, err := v3MountID(reboundRootFD)
	if err != nil || reboundRootMountID != c.rootMountID {
		return denyErr("journal_v3_root_capability_root_mount_binding_changed", err)
	}
	return nil
}

// OpenSession returns a separately reopened and locked Journal v3 session;
// closing it cannot unlock or close this capability's retained descriptors.
func (c *v3RootCapability) openSession(attemptID string) (*V3Session, error) {
	if c == nil {
		return nil, errors.New("release journal v3 root capability closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.validateLocked(); err != nil {
		return nil, err
	}
	borrowedFD, err := reopenV3DirectoryFD(c.rootFD)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(borrowedFD), c.rootName)
	if root == nil {
		_ = syscall.Close(borrowedFD)
		return nil, deny("journal_v3_root_capability_descriptor_conversion_failed")
	}
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, attemptID)
	if err != nil {
		return nil, err
	}
	if err := c.validateLocked(); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func (c *v3RootCapability) openOwnedSession(attemptID string) (*V3Session, error) {
	session, err := c.openSession(attemptID)
	if err != nil {
		return nil, err
	}
	session.lease.productionRoot = c
	return session, nil
}

func (c *v3RootCapability) bootstrapPrepared(intent v3PreparedIntent) (V3Snapshot, bool, error) {
	if c == nil {
		return V3Snapshot{}, false, errors.New("release journal v3 root capability closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.validateLocked(); err != nil {
		return V3Snapshot{}, false, err
	}
	borrowedFD, err := reopenV3DirectoryFD(c.rootFD)
	if err != nil {
		return V3Snapshot{}, false, err
	}
	root := os.NewFile(uintptr(borrowedFD), c.rootName)
	if root == nil {
		_ = syscall.Close(borrowedFD)
		return V3Snapshot{}, false, deny("journal_v3_root_capability_descriptor_conversion_failed")
	}
	defer root.Close()
	snapshot, recovered, err := bootstrapPreparedV3(root, intent)
	if err != nil {
		return V3Snapshot{}, false, err
	}
	if err := c.validateLocked(); err != nil {
		return V3Snapshot{}, false, denyErr("journal_v3_root_capability_bootstrap_binding_ambiguous", err)
	}
	return snapshot, recovered, nil
}

func sameV3DirectoryAuthority(actual, expected syscall.Stat_t) bool {
	return actual.Mode&syscall.S_IFMT == syscall.S_IFDIR && actual.Nlink >= 1 &&
		actual.Dev == expected.Dev && actual.Ino == expected.Ino && actual.Uid == expected.Uid &&
		actual.Gid == expected.Gid && actual.Mode == expected.Mode
}

func (c *v3RootCapability) close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var result error
	if err := syscall.Close(c.rootFD); err != nil {
		result = errors.Join(result, err)
	}
	if err := syscall.Close(c.parentFD); err != nil {
		result = errors.Join(result, err)
	}
	if err := syscall.Close(c.mountNSFD); err != nil {
		result = errors.Join(result, err)
	}
	if err := syscall.Close(c.userNSFD); err != nil {
		result = errors.Join(result, err)
	}
	c.rootFD, c.parentFD, c.mountNSFD, c.userNSFD = -1, -1, -1, -1
	return result
}
