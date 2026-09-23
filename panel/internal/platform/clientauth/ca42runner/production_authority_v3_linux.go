//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
	"golang.org/x/sys/unix"
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

func revalidateAuthorityV3(ctx context.Context, state *productionAuthorityV3LeaseState) error {
	if ctx == nil || state == nil || state.roots == nil || state.trustRoot == nil || state.authority == nil ||
		state.hostIdentity == nil || state.runner == nil || state.ledgerRoot == nil || state.ledgerReservation == nil ||
		state.ops.openTrustRoot == nil || state.ops.openRunner == nil {
		return errProductionAuthorityV3Invalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := revalidateAuthorityV3Namespaces(state); err != nil {
		return err
	}
	if err := matchV3ControlDirectory(state.trustRoot, state.trustRootID, state.trustRootMountID); err != nil {
		return err
	}
	if err := matchAuthorityV3File(state.authority, state.authorityID, state.authorityMountID, ca42authority.MaxDescriptorBytes, 0o400); err != nil {
		return err
	}
	if err := matchAuthorityV3File(state.hostIdentity, state.hostID, state.hostMountID, maxHostIdentityBytes, 0o400); err != nil {
		return err
	}
	if err := matchAuthorityV3File(state.runner, state.runnerID, state.runnerMountID, maxRootRunnerBytes, 0o500); err != nil {
		return err
	}
	authoritySHA, err := hashStableRetainedArtifact(ctx, state.authority, state.authorityID, ca42authority.MaxDescriptorBytes)
	if err != nil || subtle.ConstantTimeCompare(authoritySHA[:], state.authoritySHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	hostSHA, err := hashStableRetainedArtifact(ctx, state.hostIdentity, state.hostID, maxHostIdentityBytes)
	if err != nil || subtle.ConstantTimeCompare(hostSHA[:], state.hostSHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	runnerSHA, err := hashStableRetainedArtifact(ctx, state.runner, state.runnerID, maxRootRunnerBytes)
	if err != nil || subtle.ConstantTimeCompare(runnerSHA[:], state.runnerSHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	root, err := state.ops.openTrustRoot()
	if err != nil || root == nil {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	root, err = promoteRetainedFile(root)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := matchV3ControlRootDirectory(root, state.trustRootID, state.trustRootMountID); err != nil {
		return err
	}
	if err := listExactAuthorityV3Entries(ctx, root, state.trustRootID, state.trustRootMountID); err != nil {
		return err
	}
	reboundAuthority, authorityBytes, reboundAuthoritySHA, err := openRootOwnedArtifactAt(ctx, int(root.Fd()), AuthorityPath, ca42authority.MaxDescriptorBytes, 0o400, &state.authoritySHA)
	if err != nil {
		return err
	}
	defer reboundAuthority.Close()
	defer zeroBytes(authorityBytes)
	if err := matchAuthorityV3File(reboundAuthority, state.authorityID, state.authorityMountID, ca42authority.MaxDescriptorBytes, 0o400); err != nil || subtle.ConstantTimeCompare(reboundAuthoritySHA[:], state.authoritySHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	reboundHost, hostBytes, reboundHostSHA, err := openRootOwnedArtifactAt(ctx, int(root.Fd()), HostIdentityPath, maxHostIdentityBytes, 0o400, &state.hostSHA)
	if err != nil {
		return err
	}
	defer reboundHost.Close()
	defer zeroBytes(hostBytes)
	if err := matchAuthorityV3File(reboundHost, state.hostID, state.hostMountID, maxHostIdentityBytes, 0o400); err != nil || subtle.ConstantTimeCompare(reboundHostSHA[:], state.hostSHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	reboundRunner, reboundRunnerSHA, err := state.ops.openRunner(ctx)
	if err != nil || reboundRunner == nil {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	reboundRunner, err = promoteRetainedFile(reboundRunner)
	if err != nil {
		return err
	}
	defer reboundRunner.Close()
	if err := matchAuthorityV3File(reboundRunner, state.runnerID, state.runnerMountID, maxRootRunnerBytes, 0o500); err != nil ||
		subtle.ConstantTimeCompare(reboundRunnerSHA[:], state.runnerSHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	authorityChain, err := absoluteArtifactChainDigest(ctx, reboundAuthority, state.authorityID, TrustRootPath+"/"+AuthorityPath)
	if err != nil || subtle.ConstantTimeCompare(authorityChain[:], state.authorityChainSHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	hostChain, err := absoluteArtifactChainDigest(ctx, reboundHost, state.hostID, TrustRootPath+"/"+HostIdentityPath)
	if err != nil || subtle.ConstantTimeCompare(hostChain[:], state.hostChainSHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	now, err := sampleAuthorityV3Clock(state)
	if err != nil {
		return err
	}
	descriptor, err := state.roots.verifyAuthorityAt(ctx, authorityBytes, state.architecture, state.hostSHA, now)
	if err != nil {
		return err
	}
	defer zeroDescriptor(&descriptor)
	if err := matchAuthorityV3Descriptor(state, descriptor); err != nil {
		return err
	}
	if err := finalCanonicalAuthorityV3Binding(ctx, state); err != nil {
		return err
	}
	finalNow, err := sampleAuthorityV3Clock(state)
	if err != nil {
		return err
	}
	finalDescriptor, err := state.roots.verifyAuthorityAt(ctx, authorityBytes, state.architecture, state.hostSHA, finalNow)
	if err != nil {
		return err
	}
	defer zeroDescriptor(&finalDescriptor)
	if err := matchAuthorityV3Descriptor(state, finalDescriptor); err != nil {
		return err
	}
	if err := revalidateAuthorityV3Ledger(state, finalDescriptor); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// finalCanonicalAuthorityV3Binding closes the parser/time TOCTOU window. The
// descriptor is not publishable until every retained and canonical capability
// has been rebound once more after parsing.
func finalCanonicalAuthorityV3Binding(ctx context.Context, state *productionAuthorityV3LeaseState) error {
	if ctx == nil || state == nil {
		return errProductionAuthorityV3Invalid
	}
	if err := revalidateAuthorityV3Namespaces(state); err != nil {
		return err
	}
	if err := matchV3ControlDirectory(state.trustRoot, state.trustRootID, state.trustRootMountID); err != nil {
		return err
	}
	if err := matchAuthorityV3File(state.authority, state.authorityID, state.authorityMountID, ca42authority.MaxDescriptorBytes, 0o400); err != nil {
		return err
	}
	if err := matchAuthorityV3File(state.hostIdentity, state.hostID, state.hostMountID, maxHostIdentityBytes, 0o400); err != nil {
		return err
	}
	if err := matchAuthorityV3File(state.runner, state.runnerID, state.runnerMountID, maxRootRunnerBytes, 0o500); err != nil {
		return err
	}
	root, err := state.ops.openTrustRoot()
	if err != nil || root == nil {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	root, err = promoteRetainedFile(root)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := matchV3ControlRootDirectory(root, state.trustRootID, state.trustRootMountID); err != nil {
		return err
	}
	if err := listExactAuthorityV3Entries(ctx, root, state.trustRootID, state.trustRootMountID); err != nil {
		return err
	}
	authority, authorityBytes, authoritySHA, err := openRootOwnedArtifactAt(ctx, int(root.Fd()), AuthorityPath, ca42authority.MaxDescriptorBytes, 0o400, &state.authoritySHA)
	if err != nil {
		return err
	}
	defer authority.Close()
	defer zeroBytes(authorityBytes)
	if err := matchAuthorityV3File(authority, state.authorityID, state.authorityMountID, ca42authority.MaxDescriptorBytes, 0o400); err != nil ||
		subtle.ConstantTimeCompare(authoritySHA[:], state.authoritySHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	host, hostBytes, hostSHA, err := openRootOwnedArtifactAt(ctx, int(root.Fd()), HostIdentityPath, maxHostIdentityBytes, 0o400, &state.hostSHA)
	if err != nil {
		return err
	}
	defer host.Close()
	defer zeroBytes(hostBytes)
	if err := matchAuthorityV3File(host, state.hostID, state.hostMountID, maxHostIdentityBytes, 0o400); err != nil ||
		subtle.ConstantTimeCompare(hostSHA[:], state.hostSHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	runner, runnerSHA, err := state.ops.openRunner(ctx)
	if err != nil || runner == nil {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	runner, err = promoteRetainedFile(runner)
	if err != nil {
		return err
	}
	defer runner.Close()
	if err := matchAuthorityV3File(runner, state.runnerID, state.runnerMountID, maxRootRunnerBytes, 0o500); err != nil ||
		subtle.ConstantTimeCompare(runnerSHA[:], state.runnerSHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	authorityChain, err := absoluteArtifactChainDigest(ctx, authority, state.authorityID, TrustRootPath+"/"+AuthorityPath)
	if err != nil || subtle.ConstantTimeCompare(authorityChain[:], state.authorityChainSHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	hostChain, err := absoluteArtifactChainDigest(ctx, host, state.hostID, TrustRootPath+"/"+HostIdentityPath)
	if err != nil || subtle.ConstantTimeCompare(hostChain[:], state.hostChainSHA[:]) != 1 {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func bindAuthorityV3Descriptor(state *productionAuthorityV3LeaseState, descriptor ca42authority.Descriptor) error {
	if state == nil || descriptor.AttemptID != state.attemptID || descriptor.Architecture != state.architecture ||
		subtle.ConstantTimeCompare(descriptor.HostIdentitySHA256[:], state.hostSHA[:]) != 1 ||
		subtle.ConstantTimeCompare(descriptor.RootRunnerSHA256[:], state.runnerSHA[:]) != 1 {
		return errProductionAuthorityV3Unavailable
	}
	state.descriptorSHA, state.bindingSHA, state.signerSHA = descriptor.SHA256, descriptor.BindingSHA256, descriptor.ReleaseSignerSHA256
	state.manifestSHA = descriptor.ReleaseManifestSHA256
	state.releaseSignerKey = append(ed25519.PublicKey(nil), descriptor.ReleaseSignerKey...)
	state.releaseSignerKeyID = descriptor.ReleaseSignerKeyID
	state.authorityEpoch, state.authoritySequence = descriptor.AuthorityEpoch, descriptor.AuthoritySequence
	return nil
}

func matchAuthorityV3Descriptor(state *productionAuthorityV3LeaseState, descriptor ca42authority.Descriptor) error {
	if state == nil || descriptor.AttemptID != state.attemptID || descriptor.Architecture != state.architecture ||
		subtle.ConstantTimeCompare(descriptor.HostIdentitySHA256[:], state.hostSHA[:]) != 1 ||
		subtle.ConstantTimeCompare(descriptor.RootRunnerSHA256[:], state.runnerSHA[:]) != 1 ||
		subtle.ConstantTimeCompare(descriptor.SHA256[:], state.descriptorSHA[:]) != 1 ||
		subtle.ConstantTimeCompare(descriptor.BindingSHA256[:], state.bindingSHA[:]) != 1 ||
		subtle.ConstantTimeCompare(descriptor.ReleaseSignerSHA256[:], state.signerSHA[:]) != 1 ||
		subtle.ConstantTimeCompare(descriptor.ReleaseManifestSHA256[:], state.manifestSHA[:]) != 1 ||
		subtle.ConstantTimeCompare(descriptor.ReleaseSignerKey, state.releaseSignerKey) != 1 ||
		descriptor.ReleaseSignerKeyID != state.releaseSignerKeyID || descriptor.AuthorityEpoch != state.authorityEpoch ||
		descriptor.AuthoritySequence != state.authoritySequence {
		return errProductionAuthorityV3Unavailable
	}
	return nil
}

func bindAuthorityV3Ledger(state *productionAuthorityV3LeaseState, descriptor ca42authority.Descriptor, ledger ca42authority.Ledger, exact bool) error {
	if state == nil || ledger.Validate() != nil || ledger.LedgerID != descriptor.LedgerID || ledger.RootKeysetID != descriptor.RootKeysetID ||
		ledger.AuthorityEpoch != descriptor.AuthorityEpoch || ledger.AuthoritySequence != descriptor.AuthoritySequence ||
		ledger.DescriptorSHA256 != descriptor.SHA256 || ledger.PreviousDescriptorSHA != descriptor.PreviousDescriptor ||
		ledger.LastAttemptID != descriptor.AttemptID || ledger.LastManifestSHA256 != descriptor.ReleaseManifestSHA256 ||
		ledger.LastTrustedEpoch < descriptor.ClockFloor.Unix() || ledger.RecordSHA256 == ([sha256.Size]byte{}) {
		return errProductionAuthorityV3Unavailable
	}
	state.ledgerBinding = authorityV3LedgerBinding{
		recordSHA: ledger.RecordSHA256, descriptorSHA: ledger.DescriptorSHA256, previousSHA: ledger.PreviousDescriptorSHA,
		manifestSHA: ledger.LastManifestSHA256, epoch: ledger.AuthorityEpoch, sequence: ledger.AuthoritySequence,
		trustedEpoch: ledger.LastTrustedEpoch, clockFloorEpoch: descriptor.ClockFloor.Unix(), exactRetry: exact,
	}
	return nil
}

func revalidateAuthorityV3Ledger(state *productionAuthorityV3LeaseState, descriptor ca42authority.Descriptor) error {
	if state == nil || state.ledgerRoot == nil || state.ledgerReservation == nil {
		return errProductionAuthorityV3Unavailable
	}
	planned, exact, err := state.ledgerReservation.plannedLedger()
	if err != nil {
		return err
	}
	committed, recovered, err := state.ledgerReservation.commit()
	if err != nil || planned.RecordSHA256 != committed.RecordSHA256 || !recovered {
		return errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	binding := state.ledgerBinding
	if committed.RecordSHA256 != binding.recordSHA || committed.DescriptorSHA256 != binding.descriptorSHA ||
		committed.PreviousDescriptorSHA != binding.previousSHA || committed.LastManifestSHA256 != binding.manifestSHA ||
		committed.AuthorityEpoch != binding.epoch || committed.AuthoritySequence != binding.sequence ||
		committed.LastTrustedEpoch != binding.trustedEpoch || descriptor.ClockFloor.Unix() != binding.clockFloorEpoch || exact != binding.exactRetry {
		return errProductionAuthorityV3Unavailable
	}
	return state.ledgerRoot.validateLocked()
}

func sampleAuthorityV3Clock(state *productionAuthorityV3LeaseState) (time.Time, error) {
	if state == nil || state.ops.now == nil || state.ops.boottime == nil {
		return time.Time{}, errProductionAuthorityV3Invalid
	}
	raw := state.ops.now()
	boot, err := state.ops.boottime()
	if err != nil || raw.IsZero() || boot <= 0 {
		return time.Time{}, errors.Join(errProductionAuthorityV3Unavailable, err)
	}
	wall := raw.UTC()
	if !state.lastRawTime.IsZero() {
		wallDelta := raw.Sub(state.lastRawTime)
		bootDelta := boot - state.lastBoottime
		if wall.Before(state.lastWallTime) || bootDelta < 0 || wallDelta < 0 || wallDelta+2*time.Second < bootDelta {
			return time.Time{}, errProductionAuthorityV3Unavailable
		}
	}
	state.lastRawTime, state.lastWallTime, state.lastBoottime = raw, wall, boot
	return wall, nil
}

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
