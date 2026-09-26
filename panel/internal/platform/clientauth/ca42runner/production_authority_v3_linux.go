//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 clientauth/ca42authority 的描述符与账本，依赖 production_authority_v3_revalidate_linux.go 的复核与 production_authority_v3_fs_linux.go 的命名空间与文件原语，依赖 golang.org/x/sys/unix
// [OUTPUT]: 包内提供 productionAuthorityV3Lease（生产组合唯一接受的授权能力）与 retainedAuthorityV3Lease（测试缝返回的另一类型）、openProductionAuthorityV3Lease / openAuthorityV3LeaseWithOps，以及 Revalidate、releaseAuthorityBinding、Close
// [POS]: ca42runner v3 生产授权租约的入口：测试缝拿不到生产来源；Revalidate 不暴露描述符、密钥、绑定、路径、FD、根集合或可信时间，releaseAuthorityBinding 只在完整复核后交出发布验证能力
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca42runner

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
)

const maxRootRunnerBytes = 256 << 20

var (
	errProductionAuthorityV3Invalid     = errors.New("CA42 V3 production authority invalid")
	errProductionAuthorityV3Unavailable = errors.New("CA42 V3 production authority unavailable")
	errProductionAuthorityV3Busy        = errors.New("CA42 V3 production authority busy")
)

// productionAuthorityV3Lease is the only authority capability accepted by
// production composition. Test seams return the deliberately distinct raw
// retained type and therefore cannot manufacture production provenance.
type productionAuthorityV3Lease struct {
	retained *retainedAuthorityV3Lease
}

type retainedAuthorityV3Lease struct {
	state *productionAuthorityV3LeaseState
}

type authorityV3Ops struct {
	now            func() time.Time
	boottime       func() (time.Duration, error)
	openRoots      func(context.Context) (*productionRootLease, error)
	openTrustRoot  func() (*os.File, error)
	openRunner     func(context.Context) (*os.File, [sha256.Size]byte, error)
	openLedgerRoot func() (*ledgerRootCapability, error)
}

type authorityV3LedgerBinding struct {
	recordSHA, descriptorSHA, previousSHA [sha256.Size]byte
	manifestSHA                           [sha256.Size]byte
	epoch, sequence                       uint64
	trustedEpoch, clockFloorEpoch         int64
	exactRetry                            bool
}

type productionAuthorityV3LeaseState struct {
	mu   sync.Mutex
	cond *sync.Cond

	roots                   *productionRootLease
	ledgerRoot              *ledgerRootCapability
	ledgerReservation       *boundLedgerReservation
	ledgerBinding           authorityV3LedgerBinding
	trustRoot, authority    *os.File
	hostIdentity, runner    *os.File
	trustRootID             retainedIdentity
	authorityID, hostID     retainedIdentity
	runnerID                retainedIdentity
	trustRootMountID        uint64
	authorityMountID        uint64
	hostMountID             uint64
	runnerMountID           uint64
	authoritySHA, hostSHA   [sha256.Size]byte
	runnerSHA               [sha256.Size]byte
	authorityChainSHA       [sha256.Size]byte
	hostChainSHA            [sha256.Size]byte
	descriptorSHA           [sha256.Size]byte
	bindingSHA              [sha256.Size]byte
	signerSHA               [sha256.Size]byte
	manifestSHA             [sha256.Size]byte
	releaseSignerKey        ed25519.PublicKey
	releaseSignerKeyID      string
	authorityEpoch          uint64
	authoritySequence       uint64
	attemptID               string
	architecture            string
	ops                     authorityV3Ops
	lastRawTime             time.Time
	lastWallTime            time.Time
	lastBoottime            time.Duration
	mountNSFD, userNSFD     int
	pidNSFD                 int
	mountNSStat, userNSStat unix.Stat_t
	pidNSStat               unix.Stat_t
	active, closing         bool
	poisoned, closed        bool
	productionOrigin        bool
	cancel                  context.CancelFunc
	closeErr                error
}

func productionAuthorityV3Ops() authorityV3Ops {
	return authorityV3Ops{
		now: time.Now,
		boottime: func() (time.Duration, error) {
			var value unix.Timespec
			if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &value); err != nil {
				return 0, err
			}
			return time.Duration(value.Sec)*time.Second + time.Duration(value.Nsec), nil
		},
		openRoots:      openProductionRootLease,
		openTrustRoot:  func() (*os.File, error) { return openFixedTrustedDirectory(TrustRootPath) },
		openRunner:     openRunningExecutableContext,
		openLedgerRoot: openProductionLedgerRootCapability,
	}
}

func openProductionAuthorityV3Lease(ctx context.Context, attemptID string) (*productionAuthorityV3Lease, error) {
	if unix.Geteuid() != 0 {
		return nil, errProductionAuthorityV3Unavailable
	}
	retained, err := openAuthorityV3LeaseWithOps(ctx, attemptID, productionAuthorityV3Ops())
	if err != nil {
		return nil, err
	}
	retained.state.mu.Lock()
	retained.state.productionOrigin = true
	retained.state.mu.Unlock()
	return &productionAuthorityV3Lease{retained: retained}, nil
}

func openAuthorityV3LeaseWithOps(ctx context.Context, attemptID string, ops authorityV3Ops) (_ *retainedAuthorityV3Lease, resultErr error) {
	if ctx == nil || ca42controlv3.ValidateAttemptID(attemptID) != nil || ops.now == nil || ops.boottime == nil ||
		ops.openRoots == nil || ops.openTrustRoot == nil || ops.openRunner == nil || ops.openLedgerRoot == nil {
		return nil, errProductionAuthorityV3Invalid
	}
	if unix.Geteuid() != 0 {
		return nil, errProductionAuthorityV3Unavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := &productionAuthorityV3LeaseState{attemptID: attemptID, architecture: runtime.GOARCH, ops: ops, mountNSFD: -1, userNSFD: -1, pidNSFD: -1}
	state.cond = sync.NewCond(&state.mu)
	lease := &retainedAuthorityV3Lease{state: state}
	success := false
	defer func() {
		if !success {
			resultErr = errors.Join(resultErr, closeProductionAuthorityV3Owned(state))
		}
	}()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := acquireAuthorityV3Namespaces(state); err != nil {
		return nil, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var err error
	state.roots, err = ops.openRoots(ctx)
	if err != nil || state.roots == nil {
		return nil, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	state.trustRoot, err = ops.openTrustRoot()
	if err != nil || state.trustRoot == nil {
		return nil, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	state.trustRoot, err = promoteRetainedFile(state.trustRoot)
	if err != nil {
		return nil, err
	}
	state.trustRootID, state.trustRootMountID, err = secureV3ControlDirectory(state.trustRoot)
	if err != nil {
		return nil, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	if err := listExactAuthorityV3Entries(ctx, state.trustRoot, state.trustRootID, state.trustRootMountID); err != nil {
		return nil, err
	}
	var authorityBytes []byte
	state.authority, authorityBytes, state.authoritySHA, err = openRootOwnedArtifactAt(ctx, int(state.trustRoot.Fd()), AuthorityPath, ca42authority.MaxDescriptorBytes, 0o400, nil)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(authorityBytes)
	state.authorityID, state.authorityMountID, err = secureAuthorityV3File(state.authority, ca42authority.MaxDescriptorBytes, 0o400)
	if err != nil || state.authorityMountID != state.trustRootMountID {
		return nil, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	var hostBytes []byte
	state.hostIdentity, hostBytes, state.hostSHA, err = openRootOwnedArtifactAt(ctx, int(state.trustRoot.Fd()), HostIdentityPath, maxHostIdentityBytes, 0o400, nil)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(hostBytes)
	state.hostID, state.hostMountID, err = secureAuthorityV3File(state.hostIdentity, maxHostIdentityBytes, 0o400)
	if err != nil || state.hostMountID != state.trustRootMountID {
		return nil, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	state.runner, state.runnerSHA, err = ops.openRunner(ctx)
	if err != nil || state.runner == nil {
		return nil, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	state.runner, err = promoteRetainedFile(state.runner)
	if err != nil {
		return nil, err
	}
	state.runnerID, state.runnerMountID, err = secureAuthorityV3File(state.runner, maxRootRunnerBytes, 0o500)
	if err != nil {
		return nil, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	state.ledgerRoot, err = ops.openLedgerRoot()
	if err != nil || state.ledgerRoot == nil {
		return nil, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	state.authorityChainSHA, err = absoluteArtifactChainDigest(ctx, state.authority, state.authorityID, TrustRootPath+"/"+AuthorityPath)
	if err != nil {
		return nil, err
	}
	state.hostChainSHA, err = absoluteArtifactChainDigest(ctx, state.hostIdentity, state.hostID, TrustRootPath+"/"+HostIdentityPath)
	if err != nil {
		return nil, err
	}
	now, err := sampleAuthorityV3Clock(state)
	if err != nil {
		return nil, err
	}
	descriptor, err := state.roots.verifyAuthorityAt(ctx, authorityBytes, state.architecture, state.hostSHA, now)
	if err != nil {
		return nil, err
	}
	if err := bindAuthorityV3Descriptor(state, descriptor); err != nil {
		zeroDescriptor(&descriptor)
		return nil, err
	}
	state.ledgerReservation, err = state.ledgerRoot.beginCurrentReservation(descriptor, now)
	if err != nil {
		zeroDescriptor(&descriptor)
		return nil, err
	}
	planned, exact, err := state.ledgerReservation.plannedLedger()
	if err != nil {
		zeroDescriptor(&descriptor)
		return nil, err
	}
	committed, _, err := state.ledgerReservation.commit()
	if err != nil || planned.RecordSHA256 != committed.RecordSHA256 {
		zeroDescriptor(&descriptor)
		return nil, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	if err := bindAuthorityV3Ledger(state, descriptor, committed, exact); err != nil {
		zeroDescriptor(&descriptor)
		return nil, err
	}
	zeroDescriptor(&descriptor)
	if err := revalidateAuthorityV3(ctx, state); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	success = true
	return lease, nil
}

// Revalidate rechecks every retained and canonical production capability. It
// exposes no descriptor, key, binding, path, FD, root set, or trusted time.
func (lease *productionAuthorityV3Lease) Revalidate(ctx context.Context) error {
	if lease == nil || lease.retained == nil || lease.retained.state == nil {
		return errProductionAuthorityV3Invalid
	}
	state := lease.retained.state
	state.mu.Lock()
	if !state.productionOrigin {
		state.mu.Unlock()
		return errProductionAuthorityV3Unavailable
	}
	state.mu.Unlock()
	return lease.retained.revalidate(ctx)
}

// releaseAuthorityBinding returns only the release verifier capability after
// a complete production-authority revalidation. The fixed composer consumes
// it immediately; it exposes no root keys, filesystem descriptors or ledger
// mutation authority.
func (lease *productionAuthorityV3Lease) releaseAuthorityBinding(ctx context.Context) (ca42releasev3.AuthorityBinding, error) {
	if err := lease.Revalidate(ctx); err != nil {
		return ca42releasev3.AuthorityBinding{}, err
	}
	if lease == nil || lease.retained == nil || lease.retained.state == nil {
		return ca42releasev3.AuthorityBinding{}, errProductionAuthorityV3Invalid
	}
	state := lease.retained.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.productionOrigin || state.closed || state.poisoned || state.closing || state.active ||
		len(state.releaseSignerKey) != ed25519.PublicKeySize || state.releaseSignerKeyID == "" ||
		state.manifestSHA == ([sha256.Size]byte{}) || state.bindingSHA == ([sha256.Size]byte{}) ||
		state.signerSHA == ([sha256.Size]byte{}) || state.authorityEpoch == 0 || state.authoritySequence == 0 {
		return ca42releasev3.AuthorityBinding{}, errProductionAuthorityV3Unavailable
	}
	return ca42releasev3.AuthorityBinding{
		PublicKey: append(ed25519.PublicKey(nil), state.releaseSignerKey...), ManifestSHA256: state.manifestSHA,
		SignerSHA256: state.signerSHA, Epoch: state.authorityEpoch, Sequence: state.authoritySequence,
		BindingSHA256: state.bindingSHA, ReleaseSignerKeyID: state.releaseSignerKeyID,
	}, nil
}

func (lease *retainedAuthorityV3Lease) revalidate(ctx context.Context) (result error) {
	if lease == nil || lease.state == nil || ctx == nil {
		return errProductionAuthorityV3Invalid
	}
	state := lease.state
	state.mu.Lock()
	if state.closed || state.poisoned || state.closing || state.active || state.cond == nil {
		state.mu.Unlock()
		return errProductionAuthorityV3Busy
	}
	if err := ctx.Err(); err != nil {
		state.poisoned, state.closed = true, true
		state.closeErr = errors.Join(state.closeErr, closeProductionAuthorityV3Owned(state))
		state.mu.Unlock()
		return errors.Join(err, state.closeErr)
	}
	opCtx, cancel := context.WithCancel(ctx)
	state.active, state.cancel = true, cancel
	state.mu.Unlock()
	committed := false
	defer func() {
		cancel()
		state.mu.Lock()
		state.active, state.cancel = false, nil
		state.cond.Broadcast()
		if !committed || state.closing || result != nil {
			state.poisoned, state.closed = true, true
			closeErr := closeProductionAuthorityV3Owned(state)
			state.closeErr = errors.Join(state.closeErr, closeErr)
			result = errors.Join(result, closeErr)
		}
		state.mu.Unlock()
	}()
	func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		result = revalidateAuthorityV3(opCtx, state)
	}()
	committed = result == nil
	return result
}

func (lease *productionAuthorityV3Lease) Close() error {
	if lease == nil || lease.retained == nil {
		return nil
	}
	return lease.retained.Close()
}

func (lease *retainedAuthorityV3Lease) Close() error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return state.closeErr
	}
	if state.active {
		state.closing = true
		if state.cancel != nil {
			state.cancel()
		}
		for state.active {
			state.cond.Wait()
		}
		if state.closed {
			return state.closeErr
		}
	}
	state.closed = true
	state.closeErr = errors.Join(state.closeErr, closeProductionAuthorityV3Owned(state))
	return state.closeErr
}
