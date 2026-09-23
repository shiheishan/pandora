//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
	"golang.org/x/sys/unix"
)

var (
	errV3ControlBundleInvalid     = errors.New("CA42 V3 control bundle invalid")
	errV3ControlBundleUnavailable = errors.New("CA42 V3 control bundle unavailable")
	errV3ControlBundleBusy        = errors.New("CA42 V3 control bundle busy")
	errV3ControlRoleReaderExpired = errors.New("CA42 V3 control role reader expired")
)

type retainedV3ControlBundle struct {
	state *retainedV3ControlBundleState
}

type retainedV3ControlBundleState struct {
	mu                                sync.Mutex
	cond                              *sync.Cond
	root                              *os.File
	attempt                           *os.File
	rootID                            retainedIdentity
	attemptID                         retainedIdentity
	rootMountID, attemptMountID       uint64
	mountNSFD, userNSFD               int
	mountNSStat, userNSStat           unix.Stat_t
	attemptKey                        string
	entries                           [7]*retainedV3ControlEntry
	ops                               v3ControlBundleOps
	active, closing, poisoned, closed bool
	roleActive                        bool
	productionOrigin                  bool
	coreBinding                       *v3AttestationCoreBinding
	boundSet                          ca42artifactsv2.Set
	boundTime                         time.Time
	handoffClaimed                    bool
	cancel                            context.CancelFunc
}

type retainedV3ControlEntry struct {
	spec     ca42controlv3.Entry
	file     *os.File
	identity retainedIdentity
	mountID  uint64
	digest   [sha256.Size]byte
}

type v3ControlBundleOps struct {
	openRoot        func() (*os.File, error)
	beforeEntryHash func(context.Context, int) error
	afterAcquire    func(context.Context, string, int) error
	beforePublish   func(context.Context) error
}

type v3ControlRoleOperation func(context.Context, io.ReaderAt, uint64, [sha256.Size]byte) error

type v3ControlRoleReader struct {
	mu     sync.Mutex
	file   *os.File
	active bool
}

func (reader *v3ControlRoleReader) ReadAt(output []byte, offset int64) (int, error) {
	if reader == nil || offset < 0 {
		return 0, errV3ControlRoleReaderExpired
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if !reader.active || reader.file == nil {
		return 0, errV3ControlRoleReaderExpired
	}
	return reader.file.ReadAt(output, offset)
}

func (reader *v3ControlRoleReader) revoke() {
	if reader == nil {
		return
	}
	reader.mu.Lock()
	reader.active = false
	reader.file = nil
	reader.mu.Unlock()
}

func readableV3ControlRole(role string) bool {
	switch role {
	case ca42controlv3.ReleaseManifestRole,
		ca42controlv3.ExecutionPlanRole,
		ca42controlv3.TrustCapsuleRole,
		ca42controlv3.AttestationRole,
		ca42controlv3.ExpectedRole,
		ca42controlv3.ArtifactStorageDescriptorRole:
		return true
	default:
		return false
	}
}

type productionV3ControlBundle struct {
	bundle *retainedV3ControlBundle
}

func openProductionV3ControlBundle(ctx context.Context, attemptID string) (*productionV3ControlBundle, error) {
	if unix.Geteuid() != 0 {
		return nil, errV3ControlBundleUnavailable
	}
	bundle, err := openV3ControlBundleWithOps(ctx, attemptID, v3ControlBundleOps{
		openRoot: func() (*os.File, error) { return openFixedTrustedDirectory(ca42controlv3.RootPath) },
	})
	if err != nil {
		return nil, err
	}
	bundle.state.mu.Lock()
	bundle.state.productionOrigin = true
	bundle.state.mu.Unlock()
	return &productionV3ControlBundle{bundle: bundle}, nil
}

func openV3ControlBundleWithOps(ctx context.Context, attemptID string, ops v3ControlBundleOps) (_ *retainedV3ControlBundle, resultErr error) {
	if ctx == nil || ops.openRoot == nil || ca42controlv3.ValidateAttemptID(attemptID) != nil {
		return nil, errV3ControlBundleInvalid
	}
	if unix.Geteuid() != 0 {
		return nil, errV3ControlBundleUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := &retainedV3ControlBundleState{attemptKey: attemptID, ops: ops, mountNSFD: -1, userNSFD: -1}
	state.cond = sync.NewCond(&state.mu)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	success := false
	defer func() {
		if !success {
			resultErr = errors.Join(resultErr, closeV3ControlBundleOwned(state))
		}
	}()

	mountNSFD, mountNSStat, userNSFD, userNSStat, err := openLedgerSupervisorNamespaces()
	if err != nil {
		return nil, errors.Join(errV3ControlBundleUnavailable, err)
	}
	state.mountNSFD, state.mountNSStat = mountNSFD, mountNSStat
	state.userNSFD, state.userNSStat = userNSFD, userNSStat
	if err := runV3ControlAcquireHook(ctx, ops, "namespaces", 0); err != nil {
		return nil, err
	}

	root, err := ops.openRoot()
	if err != nil || root == nil {
		return nil, errors.Join(errV3ControlBundleUnavailable, err)
	}
	root, err = promoteRetainedFile(root)
	if err != nil {
		return nil, err
	}
	state.root = root
	state.rootID, state.rootMountID, err = secureV3ControlDirectory(root)
	if err != nil {
		return nil, err
	}
	if err := runV3ControlAcquireHook(ctx, ops, "root", 0); err != nil {
		return nil, err
	}
	attempt, err := openTrustedChildDirectory(int(root.Fd()), attemptID)
	if err != nil {
		return nil, err
	}
	attempt, err = promoteRetainedFile(attempt)
	if err != nil {
		return nil, err
	}
	state.attempt = attempt
	state.attemptID, state.attemptMountID, err = secureV3ControlDirectory(attempt)
	if err != nil {
		return nil, err
	}
	if state.attemptMountID != state.rootMountID {
		return nil, errV3ControlBundleUnavailable
	}
	if err := runV3ControlAcquireHook(ctx, ops, "attempt", 0); err != nil {
		return nil, err
	}
	if err := listExactV3ControlEntries(ctx, attempt, state.attemptID, state.attemptMountID); err != nil {
		return nil, err
	}
	for index, spec := range ca42controlv3.Entries() {
		entry, openErr := openRetainedV3ControlEntry(ctx, attempt, spec)
		if openErr != nil {
			return nil, openErr
		}
		if entry.mountID != state.attemptMountID {
			_ = entry.file.Close()
			return nil, errV3ControlBundleUnavailable
		}
		state.entries[index] = entry
		if err := runV3ControlAcquireHook(ctx, ops, "entry", index+1); err != nil {
			return nil, err
		}
	}
	if err := revalidateV3ControlBundle(ctx, state); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ops.beforePublish != nil {
		if err := ops.beforePublish(ctx); err != nil {
			return nil, err
		}
	}
	if err := revalidateV3ControlBundle(ctx, state); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	success = true
	return &retainedV3ControlBundle{state: state}, nil
}

func runV3ControlAcquireHook(ctx context.Context, ops v3ControlBundleOps, stage string, ordinal int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ops.afterAcquire != nil {
		if err := ops.afterAcquire(ctx, stage, ordinal); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (bundle *retainedV3ControlBundle) Revalidate(ctx context.Context) error {
	if bundle == nil || bundle.state == nil || ctx == nil {
		return errV3ControlBundleInvalid
	}
	state := bundle.state
	state.mu.Lock()
	if state.closed || state.poisoned || state.closing {
		state.mu.Unlock()
		return errV3ControlBundleUnavailable
	}
	if state.active {
		state.mu.Unlock()
		return errV3ControlBundleBusy
	}
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		return err
	}
	operationContext, cancel := context.WithCancel(ctx)
	state.active, state.cancel = true, cancel
	state.mu.Unlock()

	runtime.LockOSThread()
	result := revalidateV3ControlBundle(operationContext, state)
	runtime.UnlockOSThread()
	cancel()
	state.mu.Lock()
	state.active, state.cancel = false, nil
	state.cond.Broadcast()
	if state.closing && result == nil {
		result = context.Canceled
	}
	if result != nil {
		state.poisoned, state.closed = true, true
		result = errors.Join(result, closeV3ControlBundleOwned(state))
	}
	state.mu.Unlock()
	return result
}

// withRoleReaderAt exposes one retained data role only for the dynamic callback
// scope. It deliberately denies the executable attestation core. Complete
// bundle revalidation brackets the callback, and the reader is revoked on
// return, panic, or runtime.Goexit.
func (bundle *retainedV3ControlBundle) withRoleReaderAt(ctx context.Context, role string, operation v3ControlRoleOperation) error {
	if bundle == nil || bundle.state == nil || ctx == nil || operation == nil || !readableV3ControlRole(role) {
		return errV3ControlBundleInvalid
	}
	spec, err := ca42controlv3.EntryForRole(role)
	if err != nil || spec.Ordinal < 1 || spec.Ordinal > len(ca42controlv3.Entries()) {
		return errV3ControlBundleInvalid
	}
	state := bundle.state
	state.mu.Lock()
	if state.closed || state.poisoned || state.closing {
		state.mu.Unlock()
		return errV3ControlBundleUnavailable
	}
	if state.active {
		state.mu.Unlock()
		return errV3ControlBundleBusy
	}
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		return err
	}
	operationContext, cancel := context.WithCancel(ctx)
	state.active, state.roleActive, state.cancel = true, true, cancel
	state.mu.Unlock()

	finalized := false
	defer func() {
		if finalized {
			return
		}
		cancel()
		state.mu.Lock()
		state.active, state.roleActive, state.cancel = false, false, nil
		state.poisoned, state.closed = true, true
		state.cond.Broadcast()
		_ = closeV3ControlBundleOwned(state)
		state.mu.Unlock()
	}()

	runtime.LockOSThread()
	result := func() error {
		defer runtime.UnlockOSThread()
		if err := revalidateV3ControlBundle(operationContext, state); err != nil {
			return err
		}
		selected := state.entries[spec.Ordinal-1]
		if selected == nil || selected.spec != spec || selected.file == nil || selected.identity.size <= 0 {
			return errV3ControlBundleUnavailable
		}
		reader := &v3ControlRoleReader{file: selected.file, active: true}
		operationErr := func() error {
			defer reader.revoke()
			return operation(operationContext, reader, uint64(selected.identity.size), selected.digest)
		}()
		if operationErr != nil {
			return operationErr
		}
		if err := operationContext.Err(); err != nil {
			return err
		}
		return revalidateV3ControlBundle(operationContext, state)
	}()
	cancel()
	state.mu.Lock()
	state.active, state.roleActive, state.cancel = false, false, nil
	state.cond.Broadcast()
	if state.closing && result == nil {
		result = context.Canceled
	}
	if result != nil {
		state.poisoned, state.closed = true, true
		result = errors.Join(result, closeV3ControlBundleOwned(state))
	}
	state.mu.Unlock()
	finalized = true
	return result
}

func (bundle *retainedV3ControlBundle) Close() error {
	if bundle == nil || bundle.state == nil {
		return nil
	}
	state := bundle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	state.closing = true
	if state.cancel != nil {
		state.cancel()
	}
	if state.active && state.roleActive {
		return errV3ControlBundleBusy
	}
	for state.active {
		state.cond.Wait()
	}
	state.closed, state.closing = true, false
	return closeV3ControlBundleOwned(state)
}

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
