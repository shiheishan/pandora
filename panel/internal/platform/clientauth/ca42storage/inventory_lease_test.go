package ca42storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

type inventoryProbe struct {
	mu               sync.Mutex
	identities       map[*os.File]sourceIdentity
	retainCalls      int
	failRetainAt     int
	leaseAndErrorAt  int
	cancelRetainAt   int
	cancel           context.CancelFunc
	failRevalidate   uint64
	owned            []*os.File
	leaseOrdinals    map[*FDLease]uint64
	closeErrors      map[uint64]error
	closeOrder       []uint64
	retainError      error
	leaseAndError    error
	verifyCalls      int
	cancelVerifyAt   uint64
	cancelRevalidate context.CancelFunc
}

type inventoryBinderProbe struct {
	validateCalls int
	rebindCalls   int
	failOrdinal   uint64
	closed        bool
}

func (probe *inventoryBinderProbe) validate(ctx context.Context) error {
	probe.validateCalls++
	return ctx.Err()
}

func (probe *inventoryBinderProbe) rebind(_ context.Context, entry BoundEntry, now time.Time) error {
	probe.rebindCalls++
	snapshot, err := entry.SnapshotAt(now)
	if err != nil {
		return err
	}
	if snapshot.Ordinal == probe.failOrdinal {
		return errors.New("injected production path replacement")
	}
	return nil
}

func (probe *inventoryBinderProbe) close() error {
	probe.closed = true
	return nil
}

func (probe *inventoryProbe) ops() inventoryOps {
	return inventoryOps{
		identity: func(source *os.File) (sourceIdentity, error) {
			probe.mu.Lock()
			defer probe.mu.Unlock()
			identity, ok := probe.identities[source]
			if !ok {
				return sourceIdentity{}, errors.New("unknown source")
			}
			return identity, nil
		},
		retain: func(_ context.Context, entry BoundEntry, source *os.File, now time.Time) (*FDLease, error) {
			probe.mu.Lock()
			defer probe.mu.Unlock()
			probe.retainCalls++
			call := probe.retainCalls
			snapshot, snapshotErr := entry.SnapshotAt(now)
			identity := probe.identities[source]
			if snapshotErr != nil || identity.device != snapshot.Device || identity.inode != snapshot.Inode {
				return nil, errors.New("source ordinal mismatch")
			}
			if call == probe.failRetainAt {
				if probe.retainError != nil {
					return nil, probe.retainError
				}
				return nil, errors.New("injected retain failure")
			}
			owned, err := os.Open(source.Name())
			if err != nil {
				return nil, err
			}
			probe.owned = append(probe.owned, owned)
			if call == probe.cancelRetainAt && probe.cancel != nil {
				probe.cancel()
			}
			lease := &FDLease{
				file:  owned,
				entry: entry,
				ops: leaseOps{verify: func(_ context.Context, candidate BoundEntry, _ *os.File, at time.Time) error {
					snapshot, snapshotErr := candidate.SnapshotAt(at)
					if snapshotErr != nil {
						return snapshotErr
					}
					probe.mu.Lock()
					defer probe.mu.Unlock()
					probe.verifyCalls++
					if snapshot.Ordinal == probe.cancelVerifyAt && probe.cancelRevalidate != nil {
						probe.cancelRevalidate()
					}
					if snapshot.Ordinal == probe.failRevalidate {
						return errors.New("injected revalidation failure")
					}
					return nil
				}},
			}
			probe.leaseOrdinals[lease] = snapshot.Ordinal
			if call == probe.leaseAndErrorAt {
				if probe.leaseAndError != nil {
					return lease, probe.leaseAndError
				}
				return lease, errors.New("injected lease and error")
			}
			return lease, nil
		},
		close: func(lease *FDLease) error {
			probe.mu.Lock()
			ordinal := probe.leaseOrdinals[lease]
			probe.closeOrder = append(probe.closeOrder, ordinal)
			injected := probe.closeErrors[ordinal]
			probe.mu.Unlock()
			return errors.Join(lease.Close(), injected)
		},
	}
}

func inventoryFixture(t *testing.T) (BoundDescriptor, []*os.File, *inventoryProbe, time.Time) {
	return inventoryFixtureWithRuntime(t, MinRuntimeEntryCount)
}

func inventoryFixtureWithRuntime(t *testing.T, runtimeCount int) (BoundDescriptor, []*os.File, *inventoryProbe, time.Time) {
	t.Helper()
	data, digest := storageFixtureWithRuntime(t, runtimeCount)
	descriptor, err := Parse(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000100, 0).UTC()
	bound := BoundDescriptor{
		descriptor:    descriptor,
		planSHA:       sha256.Sum256([]byte("inventory-plan")),
		planNotBefore: time.Unix(1700000000, 0).UTC(),
		planNotAfter:  time.Unix(1700003600, 0).UTC(),
		bound:         true,
	}
	count, err := bound.EntryCountAt(now)
	if err != nil {
		t.Fatal(err)
	}
	probe := &inventoryProbe{identities: make(map[*os.File]sourceIdentity, count), leaseOrdinals: make(map[*FDLease]uint64), closeErrors: make(map[uint64]error)}
	_, entries, err := bound.cachedEntriesAt(now)
	if err != nil {
		t.Fatal(err)
	}
	sources := make([]*os.File, 0, count)
	directory := t.TempDir()
	for index := 0; index < count; index++ {
		created, err := os.CreateTemp(directory, "ca42-inventory-*")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := created.Write([]byte{byte(index)}); err != nil {
			t.Fatal(err)
		}
		name := created.Name()
		if err := created.Close(); err != nil {
			t.Fatal(err)
		}
		source, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
		snapshot, err := entries[index].SnapshotAt(now)
		if err != nil {
			t.Fatal(err)
		}
		probe.identities[source] = sourceIdentity{device: snapshot.Device, inode: snapshot.Inode}
	}
	t.Cleanup(func() {
		for _, source := range sources {
			_ = source.Close()
		}
		for _, owned := range probe.owned {
			_ = owned.Close()
		}
	})
	return bound, sources, probe, now
}

func constantClock(now time.Time) inventoryClock { return func() time.Time { return now } }

func setInventoryClock(lease *InventoryLease, clock inventoryClock) {
	lease.state.mu.Lock()
	defer lease.state.mu.Unlock()
	lease.state.clock = clock
}

func assertCallerSourcesOpen(t *testing.T, sources []*os.File) {
	t.Helper()
	for index, source := range sources {
		if source == nil {
			continue
		}
		if _, err := source.Stat(); err != nil {
			t.Fatalf("caller source %d was closed: %v", index, err)
		}
	}
}

func assertOwnedClosed(t *testing.T, owned []*os.File) {
	t.Helper()
	for index, file := range owned {
		if _, err := file.Stat(); err == nil {
			t.Fatalf("owned descriptor %d remained open", index)
		}
	}
}

func TestInventoryLeaseExactCompletenessAndOwnership(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.state.leases) != len(sources) || probe.retainCalls != len(sources) {
		t.Fatalf("incomplete inventory published: leases=%d calls=%d sources=%d", len(lease.state.leases), probe.retainCalls, len(sources))
	}
	if err := lease.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("second close was not idempotent: %v", err)
	}
	assertCallerSourcesOpen(t, sources)
	assertOwnedClosed(t, probe.owned)
}

func TestInventoryLeaseShallowCopySharesLifecycle(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	copyLease := *lease
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	closedCount := len(probe.closeOrder)
	if closedCount != len(probe.owned) {
		t.Fatalf("first close did not close exact inventory: got=%d want=%d", closedCount, len(probe.owned))
	}
	if err := copyLease.Close(); err != nil {
		t.Fatalf("shallow-copy close was not idempotent: %v", err)
	}
	if len(probe.closeOrder) != closedCount {
		t.Fatal("shallow-copy close closed child leases twice")
	}
	if err := copyLease.Revalidate(context.Background()); err == nil {
		t.Fatal("shallow copy remained usable after shared close")
	}
	assertCallerSourcesOpen(t, sources)
}

func TestInventoryLeaseRestrictValidityIsSharedAndHalfOpen(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	current := now
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, func() time.Time { return current }, probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	copyLease := *lease
	notBefore, notAfter := now.Add(-time.Minute), now.Add(time.Minute)
	if err := lease.RestrictValidity(notBefore, notAfter); err != nil {
		t.Fatal(err)
	}
	if copyLease.state.notBefore != notBefore || copyLease.state.notAfter != notAfter {
		t.Fatal("shallow copy did not share restricted validity")
	}
	if err := copyLease.RestrictValidity(bound.planNotBefore, notAfter); err == nil {
		t.Fatal("broader validity start was accepted")
	}
	if err := copyLease.RestrictValidity(notBefore, bound.planNotAfter); err == nil {
		t.Fatal("broader validity end was accepted")
	}
	current = notAfter
	if err := copyLease.Revalidate(context.Background()); err == nil || !lease.state.poisoned {
		t.Fatalf("half-open validity end remained usable: %v", err)
	}
	assertOwnedClosed(t, probe.owned)
}

func TestInventoryLeaseRestrictValidityRejectsExpiredBoundary(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.RestrictValidity(now.Add(-time.Minute), now); err == nil {
		t.Fatal("restriction ending at current time was accepted")
	}
	if lease.state.poisoned || lease.state.closed {
		t.Fatal("rejected restriction mutated lease lifecycle")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryLeaseRevalidatesAndOwnsProductionPathBinder(t *testing.T) {
	bound, sources, inventory, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), inventory.ops())
	if err != nil {
		t.Fatal(err)
	}
	binder := &inventoryBinderProbe{}
	lease.state.binder = binder
	if err := lease.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if binder.validateCalls != 2 || binder.rebindCalls != len(sources) {
		t.Fatalf("incomplete production path rebind: validate=%d rebind=%d entries=%d", binder.validateCalls, binder.rebindCalls, len(sources))
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if !binder.closed {
		t.Fatal("production path binder was not closed with inventory")
	}
}

func TestInventoryLeasePathRebindFailurePoisonsAndClosesAll(t *testing.T) {
	bound, sources, inventory, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), inventory.ops())
	if err != nil {
		t.Fatal(err)
	}
	binder := &inventoryBinderProbe{failOrdinal: 2}
	lease.state.binder = binder
	if err := lease.Revalidate(context.Background()); err == nil {
		t.Fatal("production path replacement was accepted")
	}
	if !lease.state.poisoned || !binder.closed || len(lease.state.leases) != 0 {
		t.Fatalf("path replacement did not poison and close inventory: poisoned=%v binderClosed=%v leases=%d", lease.state.poisoned, binder.closed, len(lease.state.leases))
	}
}

func TestInventoryLeaseMaximumCardinality(t *testing.T) {
	bound, sources, probe, now := inventoryFixtureWithRuntime(t, MaxRuntimeEntryCount)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != AttemptEntryCount+MigrationEntryCount+MaxRuntimeEntryCount || len(lease.state.leases) != len(sources) || probe.retainCalls != len(sources) {
		t.Fatalf("maximum inventory incomplete: sources=%d leases=%d calls=%d", len(sources), len(lease.state.leases), probe.retainCalls)
	}
	if err := lease.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	assertOwnedClosed(t, probe.owned)
	assertCallerSourcesOpen(t, sources)
}

func TestInventoryLeaseRejectsSourcePermutation(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	candidate := append([]*os.File(nil), sources...)
	candidate[0], candidate[1] = candidate[1], candidate[0]
	lease, err := retainInventoryWithOps(context.Background(), bound, candidate, constantClock(now), probe.ops())
	if lease != nil || err == nil || probe.retainCalls != 0 {
		t.Fatalf("permuted sources accepted: lease=%v err=%v calls=%d", lease, err, probe.retainCalls)
	}
	assertCallerSourcesOpen(t, sources)
}

func TestInventoryLeasePreflightRejectsNonExactAndDuplicateSets(t *testing.T) {
	for _, name := range []string{"empty", "missing", "extra", "nil", "duplicate_pointer", "duplicate_identity"} {
		t.Run(name, func(t *testing.T) {
			bound, sources, probe, now := inventoryFixture(t)
			candidate := append([]*os.File(nil), sources...)
			switch name {
			case "empty":
				candidate = candidate[:0]
			case "missing":
				candidate = candidate[:len(candidate)-1]
			case "extra":
				candidate = append(candidate, sources[0])
			case "nil":
				candidate[len(candidate)/2] = nil
			case "duplicate_pointer":
				candidate[1] = candidate[0]
			case "duplicate_identity":
				probe.identities[candidate[1]] = probe.identities[candidate[0]]
			}
			lease, err := retainInventoryWithOps(context.Background(), bound, candidate, constantClock(now), probe.ops())
			if lease != nil || err == nil || probe.retainCalls != 0 {
				t.Fatalf("invalid set reached retention: lease=%v err=%v calls=%d", lease, err, probe.retainCalls)
			}
			assertCallerSourcesOpen(t, sources)
		})
	}
}

func TestInventoryLeaseRollsBackEveryPartialFailure(t *testing.T) {
	for _, failAt := range []int{1, 30, 59} {
		t.Run(time.Unix(int64(failAt), 0).String(), func(t *testing.T) {
			bound, sources, probe, now := inventoryFixture(t)
			probe.failRetainAt = failAt
			lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
			if lease != nil || err == nil || probe.retainCalls != failAt {
				t.Fatalf("partial failure published or overran: lease=%v err=%v calls=%d", lease, err, probe.retainCalls)
			}
			assertCallerSourcesOpen(t, sources)
			assertOwnedClosed(t, probe.owned)
		})
	}
}

func TestInventoryLeaseClosesNonNilLeaseReturnedWithError(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	retainSentinel := errors.New("retain sentinel")
	closeSentinel := errors.New("child close sentinel")
	probe.leaseAndErrorAt = 30
	probe.leaseAndError = retainSentinel
	probe.closeErrors[30] = closeSentinel
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if lease != nil || !errors.Is(err, retainSentinel) || !errors.Is(err, closeSentinel) || probe.retainCalls != 30 {
		t.Fatalf("non-nil failed child was published: lease=%v err=%v calls=%d", lease, err, probe.retainCalls)
	}
	if len(probe.closeOrder) != 30 || probe.closeOrder[0] != 30 || probe.closeOrder[len(probe.closeOrder)-1] != 1 {
		t.Fatalf("failed child rollback order invalid: %v", probe.closeOrder)
	}
	assertOwnedClosed(t, probe.owned)
	assertCallerSourcesOpen(t, sources)
}

func TestInventoryLeaseCancellationAndClockCrossingAreAtomic(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if lease, err := retainInventoryWithOps(cancelled, bound, sources, constantClock(now), probe.ops()); lease != nil || !errors.Is(err, context.Canceled) || probe.retainCalls != 0 {
		t.Fatalf("pre-cancel retained inventory: lease=%v err=%v calls=%d", lease, err, probe.retainCalls)
	}

	bound, sources, probe, now = inventoryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	probe.cancel = cancel
	probe.cancelRetainAt = 2
	lease, err := retainInventoryWithOps(ctx, bound, sources, constantClock(now), probe.ops())
	if lease != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-acquisition cancellation published inventory: lease=%v err=%v", lease, err)
	}
	assertOwnedClosed(t, probe.owned)
	assertCallerSourcesOpen(t, sources)

	bound, sources, probe, now = inventoryFixture(t)
	call := 0
	expiresDuringAcquire := func() time.Time {
		call++
		if call <= len(sources)+5 {
			return now
		}
		return time.Unix(1700003600, 0).UTC()
	}
	lease, err = retainInventoryWithOps(context.Background(), bound, sources, expiresDuringAcquire, probe.ops())
	if lease != nil || err == nil || probe.retainCalls == 0 || probe.retainCalls >= len(sources) {
		t.Fatalf("clock crossing was not atomic: lease=%v err=%v calls=%d", lease, err, probe.retainCalls)
	}
	assertOwnedClosed(t, probe.owned)
	assertCallerSourcesOpen(t, sources)
}

func TestInventoryLeaseRevalidationFailurePoisonsWholeSet(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	probe.failRevalidate = 30
	if err := lease.Revalidate(context.Background()); err == nil {
		t.Fatal("child integrity failure did not fail inventory")
	}
	if !lease.state.poisoned || len(lease.state.leases) != 0 {
		t.Fatalf("failed inventory remained usable: poisoned=%v leases=%d", lease.state.poisoned, len(lease.state.leases))
	}
	assertOwnedClosed(t, probe.owned)
	assertCallerSourcesOpen(t, sources)
	if err := lease.Revalidate(context.Background()); err == nil {
		t.Fatal("poisoned inventory revalidated")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryLeaseClockRegressionAndExpiryPoison(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	regressed := now.Add(-time.Second)
	setInventoryClock(lease, constantClock(regressed))
	if err := lease.Revalidate(context.Background()); err == nil || !lease.state.poisoned {
		t.Fatalf("regressed trusted clock did not poison inventory: %v", err)
	}
	assertOwnedClosed(t, probe.owned)

	bound, sources, probe, now = inventoryFixture(t)
	lease, err = retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	setInventoryClock(lease, constantClock(time.Unix(1700003600, 0).UTC()))
	if err := lease.Revalidate(context.Background()); err == nil || !lease.state.poisoned {
		t.Fatalf("expired inventory remained usable: %v", err)
	}
	assertOwnedClosed(t, probe.owned)
}

func TestInventoryLeaseClaimWithinEnforcesNarrowWindowDuringRealRevalidation(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	current := now
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, func() time.Time { return current }, probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	notBefore, notAfter := now.Add(-time.Minute), now.Add(time.Minute)
	if !notBefore.After(bound.planNotBefore) || !notAfter.Before(bound.planNotAfter) {
		t.Fatal("fixture claim window is not strictly narrower than Plan")
	}
	if err := lease.ClaimBoundDescriptorWithin(context.Background(), bound, notBefore, notAfter); err != nil {
		t.Fatal(err)
	}
	current = notAfter
	if err := lease.Revalidate(context.Background()); err == nil || !lease.state.poisoned {
		t.Fatalf("half-open claimed window remained usable: %v", err)
	}
	assertOwnedClosed(t, probe.owned)
}

func TestInventoryLeaseSecondClaimCannotNarrowFirstOwnersWindow(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	current := now
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, func() time.Time { return current }, probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	firstNotBefore, firstNotAfter := now.Add(-90*time.Second), now.Add(2*time.Minute)
	if err := lease.ClaimBoundDescriptorWithin(context.Background(), bound, firstNotBefore, firstNotAfter); err != nil {
		t.Fatal(err)
	}
	secondNotBefore, secondNotAfter := now.Add(-30*time.Second), now.Add(time.Minute)
	if err := lease.ClaimBoundDescriptorWithin(context.Background(), bound, secondNotBefore, secondNotAfter); err == nil {
		t.Fatal("second inventory handoff claim accepted")
	}
	if lease.state.notBefore != firstNotBefore || lease.state.notAfter != firstNotAfter {
		t.Fatalf("rejected second claim mutated first window: got=[%s,%s) want=[%s,%s)",
			lease.state.notBefore, lease.state.notAfter, firstNotBefore, firstNotAfter)
	}
	current = now.Add(90 * time.Second)
	if err := lease.Revalidate(context.Background()); err != nil {
		t.Fatalf("rejected second claim disturbed first owner's lease: %v", err)
	}
}

func TestInventoryLeaseClockCrossingDuringRevalidationPoisons(t *testing.T) {
	for _, test := range []struct {
		name  string
		clock func(int, int, time.Time) time.Time
	}{
		{name: "post_child_expiry", clock: func(call, _ int, now time.Time) time.Time {
			if call <= 2 {
				return now
			}
			return time.Unix(1700003600, 0).UTC()
		}},
		{name: "post_child_regression", clock: func(call, _ int, now time.Time) time.Time {
			if call <= 2 {
				return now
			}
			return now.Add(-time.Nanosecond)
		}},
		{name: "final_expiry", clock: func(call, count int, now time.Time) time.Time {
			if call <= 2*count+1 {
				return now
			}
			return time.Unix(1700003600, 0).UTC()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bound, sources, probe, now := inventoryFixture(t)
			lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			setInventoryClock(lease, func() time.Time {
				calls++
				return test.clock(calls, len(sources), now)
			})
			if err := lease.Revalidate(context.Background()); err == nil || !lease.state.poisoned {
				t.Fatalf("clock boundary did not poison: %v", err)
			}
			if test.name != "final_expiry" && probe.verifyCalls != 1 {
				t.Fatalf("verification continued after post-child clock failure: %d", probe.verifyCalls)
			}
			if test.name == "final_expiry" && probe.verifyCalls != len(sources) {
				t.Fatalf("final gate did not follow exact full pass: %d", probe.verifyCalls)
			}
			assertOwnedClosed(t, probe.owned)
			assertCallerSourcesOpen(t, sources)
		})
	}
}

func TestInventoryLeaseCallerCancellationPoisons(t *testing.T) {
	for _, name := range []string{"pre_canceled", "during_child"} {
		t.Run(name, func(t *testing.T) {
			bound, sources, probe, now := inventoryFixture(t)
			lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if name == "pre_canceled" {
				cancel()
			} else {
				probe.cancelVerifyAt = 1
				probe.cancelRevalidate = cancel
			}
			if err := lease.Revalidate(ctx); !errors.Is(err, context.Canceled) || !lease.state.poisoned {
				t.Fatalf("caller cancellation did not poison: %v", err)
			}
			assertOwnedClosed(t, probe.owned)
			assertCallerSourcesOpen(t, sources)
			if err := lease.Revalidate(context.Background()); err == nil {
				t.Fatal("canceled inventory revalidated")
			}
		})
	}
}

func TestInventoryLeaseCloseCancelsAndSerializesWithRevalidation(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	lease.state.leases[0].ops.verify = func(context.Context, BoundEntry, *os.File, time.Time) error {
		close(entered)
		<-release
		return nil
	}
	revalidated := make(chan error, 1)
	go func() { revalidated <- lease.Revalidate(context.Background()) }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- lease.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close raced ahead of active revalidation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-revalidated; !errors.Is(err, context.Canceled) {
		t.Fatalf("Close did not cancel active revalidation: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	assertOwnedClosed(t, probe.owned)
	assertCallerSourcesOpen(t, sources)
}

func TestInventoryLeaseConcurrentCloseIsIdempotent(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 32)
	for index := 0; index < cap(results); index++ {
		go func() { results <- lease.Close() }()
	}
	for index := 0; index < cap(results); index++ {
		if err := <-results; err != nil {
			t.Fatalf("concurrent close %d: %v", index, err)
		}
	}
	assertOwnedClosed(t, probe.owned)
	assertCallerSourcesOpen(t, sources)
}

func TestInventoryLeaseCloseContinuesAfterChildCloseErrors(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	first := errors.New("first close sentinel")
	middle := errors.New("middle close sentinel")
	last := errors.New("last close sentinel")
	probe.closeErrors[1] = first
	probe.closeErrors[uint64(len(probe.owned)/2+1)] = middle
	probe.closeErrors[uint64(len(probe.owned))] = last
	if err := lease.Close(); !errors.Is(err, first) || !errors.Is(err, middle) || !errors.Is(err, last) {
		t.Fatalf("child close errors were not aggregated: %v", err)
	}
	if len(probe.closeOrder) != len(probe.owned) {
		t.Fatalf("not every child close was attempted: %d", len(probe.closeOrder))
	}
	for index, ordinal := range probe.closeOrder {
		want := uint64(len(probe.owned) - index)
		if ordinal != want {
			t.Fatalf("close order[%d]=%d want=%d", index, ordinal, want)
		}
	}
	if len(lease.state.leases) != 0 || len(lease.state.entries) != 0 || len(lease.state.paths) != 0 {
		t.Fatal("close error prevented complete state clearing")
	}
	assertOwnedClosed(t, probe.owned)
	assertCallerSourcesOpen(t, sources)
}
