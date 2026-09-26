//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖同包的 retainedIdentity 与挂载 ID 读取，依赖 clientauth/ca42controlv3 的条目规格，依赖 golang.org/x/sys/unix
// [OUTPUT]: 包内提供 v3 控制包目录与文件的 secure* / match* 核验、hashRetainedV3ControlFile、listExactV3ControlEntries、promoteRetainedFile、v3ControlMountID、closeV3ControlBundleOwned
// [POS]: ca42runner v3 控制包的目录与文件原语：从 control_bundle_v3_linux.go 拆出。目录与文件按保留时的身份与挂载 ID 精确匹配，目录列表必须与清单完全一致，关闭时释放包持有的全部描述符
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca42runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
)

func secureV3ControlDirectory(file *os.File) (retainedIdentity, uint64, error) {
	if file == nil || file.Fd() < 3 {
		return retainedIdentity{}, 0, errV3ControlBundleInvalid
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return retainedIdentity{}, 0, errors.Join(errV3ControlBundleUnavailable, err)
	}
	fdFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		return retainedIdentity{}, 0, errors.Join(errV3ControlBundleUnavailable, err)
	}
	identity, err := retainedStat(int(file.Fd()))
	if err != nil || identity.mode&unix.S_IFMT != unix.S_IFDIR || identity.uid != 0 || identity.gid != 0 ||
		identity.nlink < 1 || identity.mode&0o7777 != ca42controlv3.DirectoryMode {
		return retainedIdentity{}, 0, errors.Join(errV3ControlBundleUnavailable, err)
	}
	mountID, err := v3ControlMountID(file)
	if err != nil {
		return retainedIdentity{}, 0, err
	}
	return identity, mountID, nil
}

func matchV3ControlDirectory(file *os.File, expected retainedIdentity, expectedMountID uint64) error {
	identity, mountID, err := secureV3ControlDirectory(file)
	if err != nil || identity != expected || mountID != expectedMountID {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	return nil
}

func matchV3ControlRootDirectory(file *os.File, expected retainedIdentity, expectedMountID uint64) error {
	identity, mountID, err := secureV3ControlDirectory(file)
	if err != nil || !sameV3ControlRootIdentity(identity, expected) || mountID != expectedMountID {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	return nil
}

func sameV3ControlRootIdentity(actual, expected retainedIdentity) bool {
	return actual.dev == expected.dev && actual.ino == expected.ino && actual.mode == expected.mode &&
		actual.uid == expected.uid && actual.gid == expected.gid
}

func secureV3ControlFile(file *os.File, spec ca42controlv3.Entry) (retainedIdentity, uint64, error) {
	if file == nil || file.Fd() < 3 || spec.MaxBytes == 0 {
		return retainedIdentity{}, 0, errV3ControlBundleInvalid
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return retainedIdentity{}, 0, errV3ControlBundleUnavailable
	}
	fdFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		return retainedIdentity{}, 0, errV3ControlBundleUnavailable
	}
	identity, err := retainedStat(int(file.Fd()))
	if err != nil || identity.mode&unix.S_IFMT != unix.S_IFREG || identity.uid != 0 || identity.gid != 0 ||
		identity.nlink != 1 || identity.mode&0o7777 != spec.Mode || identity.size <= 0 || uint64(identity.size) > spec.MaxBytes {
		return retainedIdentity{}, 0, errors.Join(errV3ControlBundleUnavailable, err)
	}
	mountID, err := v3ControlMountID(file)
	if err != nil {
		return retainedIdentity{}, 0, err
	}
	return identity, mountID, nil
}

func matchV3ControlFile(file *os.File, spec ca42controlv3.Entry, expected retainedIdentity, expectedMountID uint64) error {
	identity, mountID, err := secureV3ControlFile(file, spec)
	if err != nil || identity != expected || mountID != expectedMountID {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	return nil
}

func hashRetainedV3ControlFile(ctx context.Context, file *os.File, expected retainedIdentity, maxBytes int64) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if ctx == nil || file == nil || expected.size <= 0 || expected.size > maxBytes {
		return digest, errV3ControlBundleInvalid
	}
	hash := sha256.New()
	buffer := make([]byte, 256<<10)
	var offset int64
	for offset < expected.size {
		if err := ctx.Err(); err != nil {
			return digest, err
		}
		want := expected.size - offset
		if want > int64(len(buffer)) {
			want = int64(len(buffer))
		}
		read, err := file.ReadAt(buffer[:want], offset)
		if read > 0 {
			_, _ = hash.Write(buffer[:read])
			offset += int64(read)
		}
		if err != nil && !(errors.Is(err, io.EOF) && offset == expected.size) {
			return digest, errV3ControlBundleUnavailable
		}
		if read == 0 {
			return digest, errV3ControlBundleUnavailable
		}
	}
	if err := ctx.Err(); err != nil {
		return digest, err
	}
	after, err := retainedStat(int(file.Fd()))
	if err != nil || after != expected {
		return digest, errors.Join(errV3ControlBundleUnavailable, err)
	}
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func listExactV3ControlEntries(ctx context.Context, attempt *os.File, expected retainedIdentity, expectedMountID uint64) error {
	if ctx == nil || attempt == nil {
		return errV3ControlBundleInvalid
	}
	fd, err := unix.Openat2(int(attempt.Fd()), ".", &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return err
	}
	duplicate := os.NewFile(uintptr(fd), "control-attempt-enumeration")
	if duplicate == nil {
		_ = unix.Close(fd)
		return errV3ControlBundleInvalid
	}
	duplicate, err = promoteRetainedFile(duplicate)
	if err != nil {
		return err
	}
	defer duplicate.Close()
	if err := matchV3ControlDirectory(duplicate, expected, expectedMountID); err != nil {
		return err
	}
	entries, err := duplicate.ReadDir(-1)
	if err != nil || len(entries) != len(ca42controlv3.Entries()) {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	wanted := make(map[string]bool, len(entries))
	for _, spec := range ca42controlv3.Entries() {
		wanted[spec.Name] = true
	}
	for _, entry := range entries {
		if !wanted[entry.Name()] {
			return fmt.Errorf("%w: unexpected entry", errV3ControlBundleUnavailable)
		}
		delete(wanted, entry.Name())
	}
	if len(wanted) != 0 {
		return errV3ControlBundleUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return matchV3ControlDirectory(attempt, expected, expectedMountID)
}

func promoteRetainedFile(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, errV3ControlBundleInvalid
	}
	if file.Fd() >= 3 {
		return file, nil
	}
	promoted, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	name := file.Name()
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		_ = unix.Close(promoted)
		return nil, closeErr
	}
	result := os.NewFile(uintptr(promoted), name)
	if result == nil {
		_ = unix.Close(promoted)
		return nil, errV3ControlBundleInvalid
	}
	return result, nil
}

func v3ControlMountID(file *os.File) (uint64, error) {
	if file == nil {
		return 0, errV3ControlBundleInvalid
	}
	var stat unix.Statx_t
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat); err != nil ||
		stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return 0, errors.Join(errV3ControlBundleUnavailable, err)
	}
	return stat.Mnt_id, nil
}

func closeV3ControlBundleOwned(state *retainedV3ControlBundleState) error {
	if state == nil {
		return nil
	}
	var result error
	for index := len(state.entries) - 1; index >= 0; index-- {
		if state.entries[index] != nil && state.entries[index].file != nil {
			result = errors.Join(result, state.entries[index].file.Close())
			state.entries[index].file = nil
		}
		state.entries[index] = nil
	}
	if state.attempt != nil {
		result = errors.Join(result, state.attempt.Close())
		state.attempt = nil
	}
	if state.root != nil {
		result = errors.Join(result, state.root.Close())
		state.root = nil
	}
	if state.userNSFD >= 0 {
		fd := state.userNSFD
		state.userNSFD = -1
		result = errors.Join(result, unix.Close(fd))
	}
	if state.mountNSFD >= 0 {
		fd := state.mountNSFD
		state.mountNSFD = -1
		result = errors.Join(result, unix.Close(fd))
	}
	state.rootID, state.attemptID = retainedIdentity{}, retainedIdentity{}
	state.rootMountID, state.attemptMountID = 0, 0
	state.mountNSStat, state.userNSStat = unix.Stat_t{}, unix.Stat_t{}
	state.roleActive = false
	state.productionOrigin = false
	state.coreBinding = nil
	state.boundSet = ca42artifactsv2.Set{}
	state.boundTime = time.Time{}
	state.handoffClaimed = false
	state.attemptKey = ""
	state.ops = v3ControlBundleOps{}
	return result
}
