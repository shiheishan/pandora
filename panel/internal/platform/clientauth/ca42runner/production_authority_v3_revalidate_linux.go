//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 production_authority_v3_linux.go 的 productionAuthorityV3LeaseState，依赖 clientauth 的 ca42authority 描述符与账本绑定
// [OUTPUT]: 包内提供 revalidateAuthorityV3、finalCanonicalAuthorityV3Binding、描述符与账本的 bind* / match* / revalidate*、sampleAuthorityV3Clock
// [POS]: ca42runner v3 生产授权租约的复核：从 production_authority_v3_linux.go 拆出。每次复核都把保留的与规范的能力逐项重绑定；finalCanonicalAuthorityV3Binding 关上解析与取时之间的 TOCTOU 窗口，描述符在解析后再重绑定一遍才可发布
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca42runner

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
)

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
