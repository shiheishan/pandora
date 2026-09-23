package ca42storage

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

type sourceIdentity struct {
	device uint64
	inode  uint64
}

type inventoryOps struct {
	identity func(*os.File) (sourceIdentity, error)
	retain   func(context.Context, BoundEntry, *os.File, time.Time) (*FDLease, error)
	close    func(*FDLease) error
}

type inventoryClock func() time.Time

type inventoryPathBinder interface {
	validate(context.Context) error
	rebind(context.Context, BoundEntry, time.Time) error
	close() error
}

type inventorySpec struct {
	path   string
	source *os.File
}

// InventoryLease owns one verified FDLease for every canonical descriptor
// entry, in exact ordinal order. It is structural retained evidence only and
// never exposes an FD or grants execution/admission authority.
type InventoryLease struct {
	state *inventoryLeaseState
}

type inventoryLeaseState struct {
	noCopy         noCopy
	mu             sync.Mutex
	cond           *sync.Cond
	descriptor     BoundDescriptor
	entries        []BoundEntry
	paths          []string
	leases         []*FDLease
	lastTime       time.Time
	notBefore      time.Time
	notAfter       time.Time
	active         bool
	roleActive     bool
	closing        bool
	cancel         context.CancelFunc
	clock          inventoryClock
	closeChild     func(*FDLease) error
	binder         inventoryPathBinder
	poisoned       bool
	closed         bool
	handoffClaimed bool
}

// RetainInventory requires exactly one caller-owned source FD per descriptor
// ordinal, in canonical ordinal order. Every source device/inode must match its
// entry before acquisition; no missing, extra, duplicate, permuted, or mixed
// set can produce a lease. Sources must remain open and unmodified until this
// call returns and are never retained or closed. Production time checks use the
// process wall clock; callers cannot replace or freeze the lease clock.
func RetainInventory(ctx context.Context, descriptor BoundDescriptor, sources []*os.File) (*InventoryLease, error) {
	if !leasePlatformSupported() {
		return nil, ErrFSVerityUnsupported
	}
	return retainInventoryWithOps(ctx, descriptor, sources, time.Now, platformInventoryOps())
}

func retainInventoryWithOps(ctx context.Context, descriptor BoundDescriptor, sources []*os.File, clock inventoryClock, ops inventoryOps) (_ *InventoryLease, resultErr error) {
	if ctx == nil || sources == nil || clock == nil || ops.identity == nil || ops.retain == nil || ops.close == nil {
		return nil, errors.New("fs-verity inventory lease input invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var lastTime time.Time
	now, err := sampleInventoryClock(clock, &lastTime)
	if err != nil {
		return nil, err
	}
	trusted, entries, err := descriptor.cachedEntriesAt(now)
	if err != nil {
		return nil, err
	}
	count := len(entries)
	if count <= 0 || count > AttemptEntryCount+MigrationEntryCount+MaxRuntimeEntryCount || len(sources) != count {
		return nil, errors.New("fs-verity inventory lease cardinality invalid")
	}
	callerSources := append([]*os.File(nil), sources...)
	specs := make([]inventorySpec, 0, count)
	seenSources := make(map[sourceIdentity]bool, count)
	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now, err = sampleInventoryClock(clock, &lastTime)
		if err != nil {
			return nil, err
		}
		entry := entries[index]
		snapshot, err := entry.SnapshotAt(now)
		if err != nil {
			return nil, err
		}
		source := callerSources[index]
		if source == nil {
			return nil, errors.New("fs-verity inventory lease source missing")
		}
		identity, err := ops.identity(source)
		if err != nil || identity.device == 0 || identity.inode == 0 ||
			identity.device != snapshot.Device || identity.inode != snapshot.Inode || seenSources[identity] {
			return nil, errors.New("fs-verity inventory lease source identity invalid or duplicate")
		}
		seenSources[identity] = true
		specs = append(specs, inventorySpec{path: snapshot.Path, source: source})
	}
	if len(specs) != count || len(seenSources) != count {
		return nil, errors.New("fs-verity inventory lease preflight completeness invalid")
	}
	now, err = sampleInventoryClock(clock, &lastTime)
	if err != nil {
		return nil, err
	}
	trusted, entries, err = trusted.cachedEntriesAt(now)
	if err != nil {
		return nil, err
	}
	state := &inventoryLeaseState{descriptor: trusted, entries: make([]BoundEntry, 0, count), paths: make([]string, 0, count), leases: make([]*FDLease, 0, count),
		lastTime: lastTime, notBefore: trusted.planNotBefore, notAfter: trusted.planNotAfter, clock: clock, closeChild: ops.close}
	state.cond = sync.NewCond(&state.mu)
	lease := &InventoryLease{state: state}
	success := false
	defer func() {
		if !success {
			resultErr = errors.Join(resultErr, lease.Close())
		}
	}()
	for index, spec := range specs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now, err = sampleInventoryClock(clock, &lastTime)
		if err != nil {
			return nil, err
		}
		current := entries[index]
		currentSnapshot, err := current.SnapshotAt(now)
		if err != nil || currentSnapshot.Path != spec.path {
			return nil, errors.Join(errors.New("fs-verity inventory lease preflight identity changed"), err)
		}
		retained, retainErr := ops.retain(ctx, current, spec.source, now)
		if retainErr != nil || retained == nil {
			if retained != nil {
				retainErr = errors.Join(retainErr, ops.close(retained))
			}
			return nil, errors.Join(errors.New("fs-verity inventory lease retention failed"), retainErr)
		}
		now, err = sampleInventoryClock(clock, &lastTime)
		if err != nil {
			return nil, errors.Join(err, ops.close(retained))
		}
		postSnapshot, snapshotErr := current.SnapshotAt(now)
		if snapshotErr != nil || postSnapshot != currentSnapshot {
			return nil, errors.Join(errors.New("fs-verity inventory lease validity changed during retention"), snapshotErr, ops.close(retained))
		}
		state.entries = append(state.entries, current)
		state.paths = append(state.paths, currentSnapshot.Path)
		state.leases = append(state.leases, retained)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now, err = sampleInventoryClock(clock, &lastTime)
	if err != nil {
		return nil, err
	}
	finalDescriptor, finalEntries, err := trusted.cachedEntriesAt(now)
	if err != nil {
		return nil, err
	}
	finalCount := len(finalEntries)
	if finalCount != count || len(state.entries) != count || len(state.paths) != count || len(state.leases) != count {
		return nil, errors.New("fs-verity inventory lease completeness invalid")
	}
	for index := range finalEntries {
		if finalEntries[index].seal != state.entries[index].seal {
			return nil, errors.New("fs-verity inventory lease final identity invalid")
		}
	}
	state.descriptor = finalDescriptor
	state.lastTime = lastTime
	for index := range callerSources {
		callerSources[index] = nil
	}
	success = true
	return lease, nil
}

// Revalidate rechecks descriptor time/identity, ordinal/path correspondence,
// and every retained FD. Any failure poisons and closes the entire inventory.
func (lease *InventoryLease) Revalidate(ctx context.Context) error {
	if lease == nil || lease.state == nil || ctx == nil {
		return errors.New("fs-verity inventory lease invalid")
	}
	state := lease.state
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return errors.New("fs-verity inventory lease closed")
	}
	if state.poisoned {
		state.mu.Unlock()
		return errors.New("fs-verity inventory lease poisoned")
	}
	if state.closing || state.active || state.clock == nil || state.cond == nil {
		state.mu.Unlock()
		return errors.New("fs-verity inventory lease busy or invalid")
	}
	if err := ctx.Err(); err != nil {
		state.poisoned = true
		result := errors.Join(err, state.closeOwnedLocked())
		state.mu.Unlock()
		return result
	}
	operationContext, cancel := context.WithCancel(ctx)
	state.active = true
	state.cancel = cancel
	descriptor := state.descriptor
	retainedEntries := state.entries
	paths := state.paths
	retainedLeases := state.leases
	lastTime := state.lastTime
	clock := state.clock
	binder := state.binder
	notBefore, notAfter := state.notBefore, state.notAfter
	state.mu.Unlock()
	finalized := false
	defer func() {
		if finalized {
			return
		}
		cancel()
		state.mu.Lock()
		state.cancel, state.active, state.roleActive, state.poisoned, state.closed = nil, false, false, true, true
		state.cond.Broadcast()
		_ = state.closeOwnedLocked()
		state.mu.Unlock()
	}()

	result := revalidateInventory(operationContext, descriptor, retainedEntries, paths, retainedLeases, clock, &lastTime, notBefore, notAfter, binder)
	cancel()
	state.mu.Lock()
	state.cancel = nil
	state.active = false
	state.cond.Broadcast()
	if state.closing && result == nil {
		result = context.Canceled
	}
	if result != nil {
		state.poisoned = true
		result = errors.Join(result, state.closeOwnedLocked())
	} else {
		state.lastTime = lastTime
	}
	state.mu.Unlock()
	finalized = true
	return result
}

// RevalidateBoundDescriptor proves that this exact retained inventory was
// acquired from the supplied opaque Plan-bound descriptor. It exposes no
// descriptor projection, FD, path, or detached seal.
func (lease *InventoryLease) RevalidateBoundDescriptor(ctx context.Context, descriptor BoundDescriptor) error {
	if lease == nil || lease.state == nil || ctx == nil {
		return errors.New("fs-verity inventory descriptor binding invalid")
	}
	if err := lease.Revalidate(ctx); err != nil {
		return err
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closing || state.active {
		return errors.New("fs-verity inventory lease busy or invalid")
	}
	if state.closed || state.poisoned || !descriptor.bound || !state.descriptor.bound ||
		descriptor.planSHA != state.descriptor.planSHA || descriptor.descriptor.SHA256 != state.descriptor.descriptor.SHA256 ||
		descriptor.descriptor.InventorySHA256 != state.descriptor.descriptor.InventorySHA256 ||
		descriptor.descriptor.AttemptID != state.descriptor.descriptor.AttemptID || len(state.entries) == 0 {
		state.poisoned = true
		return errors.Join(errors.New("fs-verity inventory descriptor binding mismatch"), state.closeOwnedLocked())
	}
	return nil
}

// ClaimBoundDescriptorWithin atomically narrows the Plan window, proves the
// descriptor binding, revalidates every retained object, and transfers this
// InventoryLease to one handoff. A rejected second claim cannot mutate the
// first owner's live lease window.
func (lease *InventoryLease) ClaimBoundDescriptorWithin(ctx context.Context, descriptor BoundDescriptor, notBefore, notAfter time.Time) error {
	if lease == nil || lease.state == nil || ctx == nil || notBefore.IsZero() || !notAfter.After(notBefore) {
		return errors.New("fs-verity inventory descriptor claim invalid")
	}
	notBefore, notAfter = notBefore.UTC(), notAfter.UTC()
	state := lease.state
	state.mu.Lock()
	if state.closed || state.poisoned || state.closing || state.active || state.clock == nil || state.cond == nil {
		state.mu.Unlock()
		return errors.New("fs-verity inventory lease busy or invalid")
	}
	if state.handoffClaimed {
		state.mu.Unlock()
		return errors.New("fs-verity inventory handoff already claimed")
	}
	if !descriptor.bound || !state.descriptor.bound || descriptor.planSHA != state.descriptor.planSHA ||
		descriptor.descriptor.SHA256 != state.descriptor.descriptor.SHA256 ||
		descriptor.descriptor.InventorySHA256 != state.descriptor.descriptor.InventorySHA256 ||
		descriptor.descriptor.AttemptID != state.descriptor.descriptor.AttemptID || len(state.entries) == 0 {
		state.poisoned = true
		result := errors.Join(errors.New("fs-verity inventory descriptor claim mismatch"), state.closeOwnedLocked())
		state.mu.Unlock()
		return result
	}
	if err := ctx.Err(); err != nil {
		state.poisoned = true
		result := errors.Join(err, state.closeOwnedLocked())
		state.mu.Unlock()
		return result
	}
	if notBefore.Before(state.notBefore) || notAfter.After(state.notAfter) {
		state.poisoned = true
		result := errors.Join(errors.New("fs-verity inventory claim validity outside retained Plan window"), state.closeOwnedLocked())
		state.mu.Unlock()
		return result
	}
	now, err := sampleInventoryClock(state.clock, &state.lastTime)
	if err != nil || now.Before(notBefore) || !now.Before(notAfter) {
		state.poisoned = true
		result := errors.Join(errors.New("fs-verity inventory claim validity expired"), err, state.closeOwnedLocked())
		state.mu.Unlock()
		return result
	}
	state.notBefore, state.notAfter = notBefore, notAfter
	operationContext, cancel := context.WithCancel(ctx)
	state.active, state.handoffClaimed, state.cancel = true, true, cancel
	trusted, retainedEntries, paths, retainedLeases := state.descriptor, state.entries, state.paths, state.leases
	lastTime, clock, binder := state.lastTime, state.clock, state.binder
	notBefore, notAfter = state.notBefore, state.notAfter
	state.mu.Unlock()

	finalized := false
	defer func() {
		if finalized {
			return
		}
		cancel()
		state.mu.Lock()
		state.cancel, state.active, state.roleActive, state.poisoned, state.closed = nil, false, false, true, true
		state.cond.Broadcast()
		_ = state.closeOwnedLocked()
		state.mu.Unlock()
	}()
	result := revalidateInventory(operationContext, trusted, retainedEntries, paths, retainedLeases, clock, &lastTime, notBefore, notAfter, binder)
	cancel()
	state.mu.Lock()
	state.cancel, state.active = nil, false
	state.cond.Broadcast()
	if state.closing && result == nil {
		result = context.Canceled
	}
	if result != nil {
		state.poisoned, state.closed = true, true
		result = errors.Join(result, state.closeOwnedLocked())
	} else {
		state.lastTime = lastTime
	}
	state.mu.Unlock()
	finalized = true
	return result
}

func revalidateInventory(ctx context.Context, descriptor BoundDescriptor, retainedEntries []BoundEntry, paths []string, leases []*FDLease, clock inventoryClock, lastTime *time.Time, notBefore, notAfter time.Time, binder inventoryPathBinder) error {
	sample := func() (time.Time, error) {
		now, err := sampleInventoryClock(clock, lastTime)
		if err != nil || notBefore.IsZero() || notAfter.IsZero() || now.Before(notBefore) || !now.Before(notAfter) {
			return time.Time{}, errors.Join(errors.New("fs-verity inventory effective validity expired"), err)
		}
		return now, nil
	}
	if binder != nil {
		if err := binder.validate(ctx); err != nil {
			return err
		}
	}
	now, err := sample()
	if err != nil {
		return err
	}
	trusted, entries, err := descriptor.cachedEntriesAt(now)
	if err != nil {
		return err
	}
	count := len(entries)
	if count <= 0 || len(retainedEntries) != count || len(paths) != count || len(leases) != count {
		return errors.New("fs-verity inventory lease completeness changed")
	}
	for index := 0; index < count; index++ {
		now, err = sample()
		if err != nil {
			return err
		}
		entry := entries[index]
		expected, err := entry.SnapshotAt(now)
		if err != nil || expected.Path != paths[index] || leases[index] == nil || entry.seal != retainedEntries[index].seal {
			return errors.Join(errors.New("fs-verity inventory lease ordinal identity changed"), err)
		}
		if binder != nil {
			if err := binder.rebind(ctx, entry, now); err != nil {
				return err
			}
		}
		actual, err := leases[index].entry.SnapshotAt(now)
		if err != nil || actual != expected {
			return errors.Join(errors.New("fs-verity inventory lease entry identity changed"), err)
		}
		if err := leases[index].RevalidateAt(ctx, now); err != nil {
			return err
		}
		now, err = sample()
		if err != nil {
			return err
		}
		postSnapshot, snapshotErr := entry.SnapshotAt(now)
		if snapshotErr != nil || postSnapshot != expected {
			return errors.Join(errors.New("fs-verity inventory lease validity changed during revalidation"), snapshotErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now, err = sample()
	if err != nil {
		return err
	}
	_, finalEntries, err := trusted.cachedEntriesAt(now)
	if err != nil || len(finalEntries) != count {
		return errors.Join(errors.New("fs-verity inventory lease final completeness changed"), err)
	}
	if binder != nil {
		if err := binder.validate(ctx); err != nil {
			return err
		}
	}
	return nil
}

// RestrictValidity can only narrow an inventory lease's existing Plan window.
// ArtifactSet uses it to bind the narrower attestation+credential effective
// window before a production lease is published.
func (lease *InventoryLease) RestrictValidity(notBefore, notAfter time.Time) error {
	if lease == nil || lease.state == nil || notBefore.IsZero() || !notAfter.After(notBefore) {
		return errors.New("fs-verity inventory validity restriction invalid")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.poisoned || state.closing || state.active || state.clock == nil ||
		notBefore.Before(state.notBefore) || notAfter.After(state.notAfter) {
		return errors.New("fs-verity inventory validity restriction denied")
	}
	now, err := sampleInventoryClock(state.clock, &state.lastTime)
	if err != nil || now.Before(notBefore) || !now.Before(notAfter) {
		return errors.Join(errors.New("fs-verity inventory validity restriction expired"), err)
	}
	state.notBefore, state.notAfter = notBefore.UTC(), notAfter.UTC()
	return nil
}

func sampleInventoryClock(clock inventoryClock, last *time.Time) (time.Time, error) {
	if clock == nil || last == nil {
		return time.Time{}, errors.New("fs-verity inventory clock invalid")
	}
	now := clock().UTC()
	if now.IsZero() || (!last.IsZero() && now.Before(*last)) {
		return time.Time{}, errors.New("fs-verity inventory clock invalid or regressed")
	}
	*last = now
	return now, nil
}

// Close is nil-safe and idempotent and closes owned entry leases in reverse
// acquisition order. It never closes caller-owned source FDs.
func (lease *InventoryLease) Close() error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	if state.cond == nil {
		state.cond = sync.NewCond(&state.mu)
	}
	state.closing = true
	if state.cancel != nil {
		state.cancel()
	}
	if state.active && state.roleActive {
		return ErrInventoryRoleBusy
	}
	for state.active {
		state.cond.Wait()
	}
	state.closed = true
	state.closing = false
	return state.closeOwnedLocked()
}

func (state *inventoryLeaseState) closeOwnedLocked() error {
	var result error
	for index := len(state.leases) - 1; index >= 0; index-- {
		if state.leases[index] != nil {
			if state.closeChild == nil {
				result = errors.Join(result, errors.New("fs-verity inventory child closer missing"))
			} else {
				result = errors.Join(result, state.closeChild(state.leases[index]))
			}
			state.leases[index] = nil
		}
	}
	state.leases = nil
	state.entries = nil
	state.paths = nil
	state.descriptor = BoundDescriptor{}
	state.lastTime = time.Time{}
	state.notBefore = time.Time{}
	state.notAfter = time.Time{}
	state.roleActive = false
	state.clock = nil
	state.closeChild = nil
	if state.binder != nil {
		result = errors.Join(result, state.binder.close())
		state.binder = nil
	}
	return result
}
