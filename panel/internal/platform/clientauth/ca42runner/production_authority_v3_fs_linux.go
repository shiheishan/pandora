//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 production_authority_v3_linux.go 的 productionAuthorityV3LeaseState，依赖同包的 retainedIdentity，依赖 golang.org/x/sys/unix
// [OUTPUT]: 包内提供授权命名空间的取得与复核、secureAuthorityV3File / matchAuthorityV3File、listExactAuthorityV3Entries、closeProductionAuthorityV3Owned、zeroDescriptor
// [POS]: ca42runner v3 生产授权租约的命名空间与文件原语：从 production_authority_v3_linux.go 拆出。命名空间须与 supervisor 的一致，文件按大小上限与权限位核验，目录列表须与预期完全一致；关闭时释放描述符并清零描述符里的密钥材料
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca42runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
)

func acquireAuthorityV3Namespaces(state *productionAuthorityV3LeaseState) error {
	mountFD, mountStat, userFD, userStat, err := openLedgerSupervisorNamespaces()
	if err != nil {
		return err
	}
	state.mountNSFD, state.mountNSStat, state.userNSFD, state.userNSStat = mountFD, mountStat, userFD, userStat
	pidFD, pidStat, err := openMatchedAuthorityV3Namespace("/proc/thread-self/ns/pid", "/proc/1/ns/pid")
	if err != nil {
		return err
	}
	state.pidNSFD, state.pidNSStat = pidFD, pidStat
	return nil
}

func openMatchedAuthorityV3Namespace(current, supervisor string) (int, unix.Stat_t, error) {
	fd, stat, err := openLedgerNamespace(current)
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	otherFD, other, err := openLedgerNamespace(supervisor)
	if err != nil {
		_ = unix.Close(fd)
		return -1, unix.Stat_t{}, err
	}
	_ = unix.Close(otherFD)
	if !sameLedgerNamespace(stat, other) {
		_ = unix.Close(fd)
		return -1, unix.Stat_t{}, errProductionAuthorityV3Unavailable
	}
	return fd, stat, nil
}

func revalidateAuthorityV3Namespaces(state *productionAuthorityV3LeaseState) error {
	if state == nil || state.mountNSFD < 0 || state.userNSFD < 0 || state.pidNSFD < 0 {
		return errProductionAuthorityV3Unavailable
	}
	checks := []struct {
		fd       int
		expected unix.Stat_t
		current  string
		pid1     string
	}{
		{state.mountNSFD, state.mountNSStat, "/proc/thread-self/ns/mnt", "/proc/1/ns/mnt"},
		{state.userNSFD, state.userNSStat, "/proc/thread-self/ns/user", "/proc/1/ns/user"},
		{state.pidNSFD, state.pidNSStat, "/proc/thread-self/ns/pid", "/proc/1/ns/pid"},
	}
	for _, check := range checks {
		var retained unix.Stat_t
		if err := unix.Fstat(check.fd, &retained); err != nil || !sameLedgerNamespace(retained, check.expected) {
			return errors.Join(errProductionAuthorityV3Unavailable, err)
		}
		currentFD, currentStat, err := openMatchedAuthorityV3Namespace(check.current, check.pid1)
		if err != nil {
			return err
		}
		_ = unix.Close(currentFD)
		if !sameLedgerNamespace(currentStat, check.expected) {
			return errProductionAuthorityV3Unavailable
		}
	}
	return nil
}

func secureAuthorityV3File(file *os.File, maxBytes int64, mode uint32) (retainedIdentity, uint64, error) {
	if file == nil || file.Fd() < 3 || maxBytes <= 0 || (mode != 0o400 && mode != 0o500) {
		return retainedIdentity{}, 0, errProductionAuthorityV3Invalid
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return retainedIdentity{}, 0, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	fdFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		return retainedIdentity{}, 0, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	identity, err := retainedStat(int(file.Fd()))
	if err != nil || identity.mode&unix.S_IFMT != unix.S_IFREG || identity.uid != 0 || identity.gid != 0 || identity.nlink != 1 ||
		identity.mode&0o7777 != mode || identity.size <= 0 || identity.size > maxBytes {
		return retainedIdentity{}, 0, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	mountID, err := v3ControlMountID(file)
	return identity, mountID, err
}

func matchAuthorityV3File(file *os.File, expected retainedIdentity, mountID uint64, maxBytes int64, mode uint32) error {
	identity, actualMount, err := secureAuthorityV3File(file, maxBytes, mode)
	if err != nil || identity != expected || actualMount != mountID {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	return nil
}

func listExactAuthorityV3Entries(ctx context.Context, root *os.File, expected retainedIdentity, mountID uint64) error {
	if ctx == nil || root == nil {
		return errProductionAuthorityV3Invalid
	}
	fd, err := unix.Openat2(int(root.Fd()), ".", &unix.OpenHow{Flags: uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	duplicate := os.NewFile(uintptr(fd), "authority-v3-enumeration")
	if duplicate == nil {
		_ = unix.Close(fd)
		return errProductionAuthorityV3Invalid
	}
	defer duplicate.Close()
	if err := matchV3ControlDirectory(duplicate, expected, mountID); err != nil {
		return err
	}
	entries, err := duplicate.ReadDir(-1)
	if err != nil || len(entries) != 2 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	names := []string{entries[0].Name(), entries[1].Name()}
	sort.Strings(names)
	if names[0] != AuthorityPath || names[1] != HostIdentityPath {
		return fmt.Errorf("%w: unexpected trust-root inventory", errProductionAuthorityV3Unavailable)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return matchV3ControlDirectory(root, expected, mountID)
}

func closeProductionAuthorityV3Owned(state *productionAuthorityV3LeaseState) error {
	if state == nil {
		return nil
	}
	var result error
	if state.ledgerReservation != nil {
		result = errors.Join(result, state.ledgerReservation.close())
		state.ledgerReservation = nil
	}
	if state.ledgerRoot != nil {
		result = errors.Join(result, state.ledgerRoot.close())
		state.ledgerRoot = nil
	}
	if state.runner != nil {
		result = errors.Join(result, state.runner.Close())
		state.runner = nil
	}
	if state.hostIdentity != nil {
		result = errors.Join(result, state.hostIdentity.Close())
		state.hostIdentity = nil
	}
	if state.authority != nil {
		result = errors.Join(result, state.authority.Close())
		state.authority = nil
	}
	if state.trustRoot != nil {
		result = errors.Join(result, state.trustRoot.Close())
		state.trustRoot = nil
	}
	if state.roots != nil {
		result = errors.Join(result, state.roots.Close())
		state.roots = nil
	}
	for _, fd := range []int{state.pidNSFD, state.userNSFD, state.mountNSFD} {
		if fd >= 0 {
			result = errors.Join(result, unix.Close(fd))
		}
	}
	state.pidNSFD, state.userNSFD, state.mountNSFD = -1, -1, -1
	state.authoritySHA, state.hostSHA, state.runnerSHA = [sha256.Size]byte{}, [sha256.Size]byte{}, [sha256.Size]byte{}
	state.authorityChainSHA, state.hostChainSHA = [sha256.Size]byte{}, [sha256.Size]byte{}
	state.descriptorSHA, state.bindingSHA, state.signerSHA = [sha256.Size]byte{}, [sha256.Size]byte{}, [sha256.Size]byte{}
	state.manifestSHA = [sha256.Size]byte{}
	zeroBytes(state.releaseSignerKey)
	state.releaseSignerKey = nil
	state.releaseSignerKeyID = ""
	state.authorityEpoch, state.authoritySequence = 0, 0
	state.trustRootID, state.authorityID, state.hostID, state.runnerID = retainedIdentity{}, retainedIdentity{}, retainedIdentity{}, retainedIdentity{}
	state.mountNSStat, state.userNSStat, state.pidNSStat = unix.Stat_t{}, unix.Stat_t{}, unix.Stat_t{}
	state.attemptID, state.architecture = "", ""
	state.lastRawTime, state.lastWallTime, state.lastBoottime = time.Time{}, time.Time{}, 0
	state.ledgerBinding = authorityV3LedgerBinding{}
	state.ops = authorityV3Ops{}
	state.productionOrigin = false
	return result
}

func zeroDescriptor(descriptor *ca42authority.Descriptor) {
	if descriptor == nil {
		return
	}
	zeroBytes(descriptor.ReleaseSignerKey)
	*descriptor = ca42authority.Descriptor{}
}
