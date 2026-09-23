//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"path"
	"runtime"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
)

type v3AttestationCoreBinding struct {
	attemptID  string
	digest     [sha256.Size]byte
	chain      [sha256.Size]byte
	device     uint64
	mode       uint32
	identity   retainedIdentity
	setBinding string
}

func (source *productionV3ControlBundle) bindParsedGraph(ctx context.Context, set ca42artifactsv2.Set) (*boundV3ControlBundle, error) {
	if source == nil || source.bundle == nil || source.bundle.state == nil || ctx == nil {
		return nil, errV3ArtifactHandoffUnavailable
	}
	state := source.bundle.state
	state.mu.Lock()
	if !state.productionOrigin || state.closed || state.poisoned || state.closing || state.active || state.coreBinding != nil {
		state.mu.Unlock()
		return nil, errV3ArtifactHandoffUnavailable
	}
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		return nil, err
	}
	operationContext, cancel := context.WithCancel(ctx)
	state.active, state.cancel = true, cancel
	state.mu.Unlock()

	finalized := false
	defer func() {
		if finalized {
			return
		}
		cancel()
		state.mu.Lock()
		state.active, state.cancel, state.poisoned, state.closed = false, nil, true, true
		state.cond.Broadcast()
		_ = closeV3ControlBundleOwned(state)
		state.mu.Unlock()
	}()

	boundSet, binding, boundAt, result := func() (ca42artifactsv2.Set, *v3AttestationCoreBinding, time.Time, error) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		return bindV3CoreTwice(operationContext, state, set, time.Time{})
	}()
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
		state.mu.Unlock()
		finalized = true
		return nil, result
	}
	state.boundSet, state.coreBinding, state.boundTime = boundSet, binding, boundAt
	state.mu.Unlock()
	finalized = true
	return &boundV3ControlBundle{production: source, binding: binding}, nil
}

func bindV3CoreTwice(ctx context.Context, state *retainedV3ControlBundleState, set ca42artifactsv2.Set, previous time.Time) (ca42artifactsv2.Set, *v3AttestationCoreBinding, time.Time, error) {
	firstTime := time.Now().UTC()
	if !previous.IsZero() && firstTime.Before(previous) {
		return ca42artifactsv2.Set{}, nil, time.Time{}, errors.New("CA42 V3 control clock moved backwards")
	}
	firstSet, first, err := bindV3CorePass(ctx, state, set, firstTime)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, time.Time{}, err
	}
	secondTime := time.Now().UTC()
	if secondTime.Before(firstTime) {
		return ca42artifactsv2.Set{}, nil, time.Time{}, errors.New("CA42 V3 control clock moved backwards")
	}
	secondSet, second, err := bindV3CorePass(ctx, state, firstSet, secondTime)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, time.Time{}, err
	}
	if *first != *second {
		return ca42artifactsv2.Set{}, nil, time.Time{}, errors.New("CA42 V3 attestation core binding changed")
	}
	return secondSet, second, secondTime, nil
}

func bindV3CorePass(ctx context.Context, state *retainedV3ControlBundleState, set ca42artifactsv2.Set, now time.Time) (ca42artifactsv2.Set, *v3AttestationCoreBinding, error) {
	if err := revalidateV3ControlBundle(ctx, state); err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	spec, err := ca42controlv3.EntryForRole(ca42controlv3.AttestationCoreRole)
	if err != nil || spec.Ordinal != 4 || spec.Name != ca42controlv3.AttestationCoreName || spec.Mode != ca42controlv3.ExecutableMode || spec.MaxBytes != ca42controlv3.MaxAttestationCoreBytes {
		return ca42artifactsv2.Set{}, nil, errV3ControlBundleInvalid
	}
	selected := state.entries[spec.Ordinal-1]
	if selected == nil || selected.spec != spec || selected.file == nil || selected.identity.size <= 0 || selected.identity.dev == 0 {
		return ca42artifactsv2.Set{}, nil, errV3ControlBundleUnavailable
	}
	absolutePath := path.Join(ca42controlv3.RootPath, state.attemptKey, ca42controlv3.AttestationCoreName)
	chain, err := absoluteArtifactChainDigest(ctx, selected.file, selected.identity, absolutePath)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	verified, err := set.VerifiedCopyBoundToAttestationCoreAt(now, state.attemptKey, selected.digest, chain,
		selected.identity.dev, selected.identity.mode&0o7777)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	snapshot, err := verified.SnapshotAt(now)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	if err := revalidateV3ControlBundle(ctx, state); err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	finalChain, err := absoluteArtifactChainDigest(ctx, selected.file, selected.identity, absolutePath)
	if err != nil || finalChain != chain {
		return ca42artifactsv2.Set{}, nil, errors.Join(errors.New("CA42 V3 attestation core path chain changed"), err)
	}
	return verified, &v3AttestationCoreBinding{attemptID: state.attemptKey, digest: selected.digest, chain: chain,
		device: selected.identity.dev, mode: selected.identity.mode & 0o7777, identity: selected.identity,
		setBinding: snapshot.BindingSHA256}, nil
}

func revalidateBoundV3Control(ctx context.Context, control *boundV3ControlBundle) error {
	if ctx == nil || control == nil || control.production == nil || control.production.bundle == nil || control.binding == nil {
		return errV3ArtifactHandoffUnavailable
	}
	state := control.production.bundle.state
	state.mu.Lock()
	if state.closed || state.poisoned || state.closing || state.active || state.coreBinding != control.binding || state.boundTime.IsZero() {
		state.mu.Unlock()
		return errV3ArtifactHandoffUnavailable
	}
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		return err
	}
	operationContext, cancel := context.WithCancel(ctx)
	state.active, state.cancel = true, cancel
	set, previous := state.boundSet, state.boundTime
	state.mu.Unlock()
	finalized := false
	defer func() {
		if finalized {
			return
		}
		cancel()
		state.mu.Lock()
		state.active, state.cancel, state.poisoned, state.closed = false, nil, true, true
		state.cond.Broadcast()
		_ = closeV3ControlBundleOwned(state)
		state.mu.Unlock()
	}()

	verified, binding, boundAt, result := func() (ca42artifactsv2.Set, *v3AttestationCoreBinding, time.Time, error) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		return bindV3CoreTwice(operationContext, state, set, previous)
	}()
	cancel()
	state.mu.Lock()
	state.active, state.cancel = false, nil
	state.cond.Broadcast()
	if state.closing && result == nil {
		result = context.Canceled
	}
	if result == nil && *binding != *control.binding {
		result = errors.New("CA42 V3 bound control identity changed")
	}
	if result != nil {
		state.poisoned, state.closed = true, true
		result = errors.Join(result, closeV3ControlBundleOwned(state))
	} else {
		state.boundSet, state.boundTime = verified, boundAt
	}
	state.mu.Unlock()
	finalized = true
	return result
}
