//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 control_bundle_v3_linux.go 的 retainedV3ControlBundleState 与 retainedV3ControlEntry，依赖 control_bundle_v3_entries_linux.go 的目录与文件核验原语，依赖 clientauth/ca42controlv3 的条目清单
// [OUTPUT]: 包内提供 revalidateV3ControlBundle、validateV3ControlNamespaces、rebindV3ControlDirectories、openRetainedV3ControlEntry / revalidateV3ControlEntry、finalCanonicalV3ControlBinding
// [POS]: ca42runner v3 控制包的整包复核：从 control_bundle_v3_linux.go 拆出。命名空间、目录重绑定与每个条目的身份和哈希都按保留时的样子逐项复核，finalCanonicalV3ControlBinding 在最终规范绑定前再过一遍
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca42runner

import (
	"context"
	"crypto/subtle"
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
)

func revalidateV3ControlBundle(ctx context.Context, state *retainedV3ControlBundleState) error {
	if ctx == nil || state == nil || state.root == nil || state.attempt == nil || state.attemptKey == "" || state.ops.openRoot == nil {
		return errV3ControlBundleInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateV3ControlNamespaces(state); err != nil {
		return err
	}
	if err := rebindV3ControlDirectories(ctx, state); err != nil {
		return err
	}
	for index, entry := range state.entries {
		if entry == nil {
			return errV3ControlBundleInvalid
		}
		if state.ops.beforeEntryHash != nil {
			if err := state.ops.beforeEntryHash(ctx, index); err != nil {
				return err
			}
		}
		if err := revalidateV3ControlEntry(ctx, state.attempt, entry); err != nil {
			return err
		}
	}
	if err := rebindV3ControlDirectories(ctx, state); err != nil {
		return err
	}
	if err := validateV3ControlNamespaces(state); err != nil {
		return err
	}
	if err := finalCanonicalV3ControlBinding(ctx, state); err != nil {
		return err
	}
	return ctx.Err()
}

func validateV3ControlNamespaces(state *retainedV3ControlBundleState) error {
	if state == nil || state.mountNSFD < 3 || state.userNSFD < 3 {
		return errV3ControlBundleUnavailable
	}
	var mountNow, userNow unix.Stat_t
	if err := unix.Fstat(state.mountNSFD, &mountNow); err != nil || !sameLedgerNamespace(mountNow, state.mountNSStat) {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	if err := unix.Fstat(state.userNSFD, &userNow); err != nil || !sameLedgerNamespace(userNow, state.userNSStat) {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	mountFD, mountStat, userFD, userStat, err := openLedgerSupervisorNamespaces()
	if mountFD >= 0 {
		_ = unix.Close(mountFD)
	}
	if userFD >= 0 {
		_ = unix.Close(userFD)
	}
	if err != nil || !sameLedgerNamespace(mountStat, state.mountNSStat) || !sameLedgerNamespace(userStat, state.userNSStat) {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	return nil
}

func rebindV3ControlDirectories(ctx context.Context, state *retainedV3ControlBundleState) error {
	if err := matchV3ControlRootDirectory(state.root, state.rootID, state.rootMountID); err != nil {
		return err
	}
	if err := matchV3ControlDirectory(state.attempt, state.attemptID, state.attemptMountID); err != nil {
		return err
	}
	canonicalRoot, err := state.ops.openRoot()
	if err != nil || canonicalRoot == nil {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	canonicalRoot, err = promoteRetainedFile(canonicalRoot)
	if err != nil {
		return err
	}
	defer canonicalRoot.Close()
	if err := matchV3ControlRootDirectory(canonicalRoot, state.rootID, state.rootMountID); err != nil {
		return err
	}
	canonicalAttempt, err := openTrustedChildDirectory(int(canonicalRoot.Fd()), state.attemptKey)
	if err != nil {
		return err
	}
	canonicalAttempt, err = promoteRetainedFile(canonicalAttempt)
	if err != nil {
		return err
	}
	defer canonicalAttempt.Close()
	if err := matchV3ControlDirectory(canonicalAttempt, state.attemptID, state.attemptMountID); err != nil {
		return err
	}
	retainedRebind, err := openTrustedChildDirectory(int(state.root.Fd()), state.attemptKey)
	if err != nil {
		return err
	}
	retainedRebind, err = promoteRetainedFile(retainedRebind)
	if err != nil {
		return err
	}
	defer retainedRebind.Close()
	if err := matchV3ControlDirectory(retainedRebind, state.attemptID, state.attemptMountID); err != nil {
		return err
	}
	if err := listExactV3ControlEntries(ctx, canonicalAttempt, state.attemptID, state.attemptMountID); err != nil {
		return err
	}
	return listExactV3ControlEntries(ctx, state.attempt, state.attemptID, state.attemptMountID)
}

func openRetainedV3ControlEntry(ctx context.Context, attempt *os.File, spec ca42controlv3.Entry) (*retainedV3ControlEntry, error) {
	if ctx == nil || attempt == nil || spec.Ordinal < 1 || spec.Ordinal > len(ca42controlv3.Entries()) ||
		spec.Name == "" || spec.MaxBytes == 0 || spec.MaxBytes > uint64(^uint64(0)>>1) {
		return nil, errV3ControlBundleInvalid
	}
	fd, err := unix.Openat2(int(attempt.Fd()), spec.Name, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), spec.Name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errV3ControlBundleInvalid
	}
	file, err = promoteRetainedFile(file)
	if err != nil {
		return nil, err
	}
	owned := true
	defer func() {
		if owned {
			_ = file.Close()
		}
	}()
	identity, mountID, err := secureV3ControlFile(file, spec)
	if err != nil {
		return nil, err
	}
	digest, err := hashRetainedV3ControlFile(ctx, file, identity, int64(spec.MaxBytes))
	if err != nil {
		return nil, err
	}
	owned = false
	return &retainedV3ControlEntry{spec: spec, file: file, identity: identity, mountID: mountID, digest: digest}, nil
}

func revalidateV3ControlEntry(ctx context.Context, attempt *os.File, entry *retainedV3ControlEntry) error {
	if ctx == nil || attempt == nil || entry == nil || entry.file == nil {
		return errV3ControlBundleInvalid
	}
	if err := matchV3ControlFile(entry.file, entry.spec, entry.identity, entry.mountID); err != nil {
		return err
	}
	reopened, err := openRetainedV3ControlEntry(ctx, attempt, entry.spec)
	if err != nil {
		return err
	}
	if !sameRetainedV3ControlEntry(reopened, entry) {
		_ = reopened.file.Close()
		return errV3ControlBundleUnavailable
	}
	if err := reopened.file.Close(); err != nil {
		return err
	}
	digest, err := hashRetainedV3ControlFile(ctx, entry.file, entry.identity, int64(entry.spec.MaxBytes))
	if err != nil || subtle.ConstantTimeCompare(digest[:], entry.digest[:]) != 1 {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	if err := matchV3ControlFile(entry.file, entry.spec, entry.identity, entry.mountID); err != nil {
		return err
	}
	finalReopen, err := openRetainedV3ControlEntry(ctx, attempt, entry.spec)
	if err != nil {
		return err
	}
	if !sameRetainedV3ControlEntry(finalReopen, entry) {
		_ = finalReopen.file.Close()
		return errV3ControlBundleUnavailable
	}
	if err := finalReopen.file.Close(); err != nil {
		return err
	}
	return matchV3ControlFile(entry.file, entry.spec, entry.identity, entry.mountID)
}

func sameRetainedV3ControlEntry(actual, expected *retainedV3ControlEntry) bool {
	return actual != nil && expected != nil && actual.spec == expected.spec && actual.identity == expected.identity &&
		actual.mountID == expected.mountID && subtle.ConstantTimeCompare(actual.digest[:], expected.digest[:]) == 1
}

func finalCanonicalV3ControlBinding(ctx context.Context, state *retainedV3ControlBundleState) error {
	root, err := state.ops.openRoot()
	if err != nil || root == nil {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	root, err = promoteRetainedFile(root)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := matchV3ControlRootDirectory(root, state.rootID, state.rootMountID); err != nil {
		return err
	}
	attempt, err := openTrustedChildDirectory(int(root.Fd()), state.attemptKey)
	if err != nil {
		return err
	}
	attempt, err = promoteRetainedFile(attempt)
	if err != nil {
		return err
	}
	defer attempt.Close()
	if err := matchV3ControlDirectory(attempt, state.attemptID, state.attemptMountID); err != nil {
		return err
	}
	if err := listExactV3ControlEntries(ctx, attempt, state.attemptID, state.attemptMountID); err != nil {
		return err
	}
	for _, expected := range state.entries {
		if expected == nil {
			return errV3ControlBundleInvalid
		}
		actual, openErr := openRetainedV3ControlEntry(ctx, attempt, expected.spec)
		if openErr != nil {
			return openErr
		}
		matches := sameRetainedV3ControlEntry(actual, expected)
		closeErr := actual.file.Close()
		if !matches || closeErr != nil {
			return errors.Join(errV3ControlBundleUnavailable, closeErr)
		}
	}
	finalRoot, err := state.ops.openRoot()
	if err != nil || finalRoot == nil {
		return errors.Join(errV3ControlBundleUnavailable, err)
	}
	finalRoot, err = promoteRetainedFile(finalRoot)
	if err != nil {
		return err
	}
	defer finalRoot.Close()
	if err := matchV3ControlRootDirectory(finalRoot, state.rootID, state.rootMountID); err != nil {
		return err
	}
	finalAttempt, err := openTrustedChildDirectory(int(finalRoot.Fd()), state.attemptKey)
	if err != nil {
		return err
	}
	finalAttempt, err = promoteRetainedFile(finalAttempt)
	if err != nil {
		return err
	}
	defer finalAttempt.Close()
	if err := matchV3ControlDirectory(finalAttempt, state.attemptID, state.attemptMountID); err != nil {
		return err
	}
	return ctx.Err()
}
