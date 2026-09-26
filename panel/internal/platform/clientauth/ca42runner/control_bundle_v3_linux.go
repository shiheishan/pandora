//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 clientauth/ca42controlv3 的条目清单，依赖 control_bundle_v3_revalidate_linux.go 的整包复核与 control_bundle_v3_entries_linux.go 的目录与文件原语，依赖 golang.org/x/sys/unix
// [OUTPUT]: 包内提供 retainedV3ControlBundle 与 productionV3ControlBundle、openProductionV3ControlBundle / openV3ControlBundleWithOps（测试注入 v3ControlBundleOps），以及 Revalidate、withRoleReaderAt、Close
// [POS]: ca42runner v3 控制包的保留与读取入口：打开时保留每个条目的描述符与身份，withRoleReaderAt 只在回调作用域里暴露一个数据角色（拒绝可执行的证明核心），前后各做一次整包复核，返回、panic 或 Goexit 时吊销读取器
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca42runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
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
