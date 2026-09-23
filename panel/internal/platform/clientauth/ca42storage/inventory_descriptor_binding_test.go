package ca42storage

import (
	"context"
	"testing"
	"time"
)

func TestInventoryRevalidateBoundDescriptorAcceptsExactCapability(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := lease.RevalidateBoundDescriptor(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryClaimBoundDescriptorWithinExactlyOnce(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := lease.ClaimBoundDescriptorWithin(context.Background(), bound, bound.planNotBefore, bound.planNotAfter); err != nil {
		t.Fatal(err)
	}
	if err := lease.ClaimBoundDescriptorWithin(context.Background(), bound, now.Add(-time.Minute), now.Add(time.Minute)); err == nil {
		t.Fatal("second inventory handoff claim accepted")
	}
	if err := lease.Revalidate(context.Background()); err != nil {
		t.Fatalf("rejected second claim disturbed first owner's lease: %v", err)
	}
}

type panicInventoryBinder struct{ closed bool }

func (*panicInventoryBinder) validate(context.Context) error                      { panic("inventory binder panic") }
func (*panicInventoryBinder) rebind(context.Context, BoundEntry, time.Time) error { return nil }
func (binder *panicInventoryBinder) close() error                                 { binder.closed = true; return nil }

func TestInventoryRevalidatePanicClosesWithoutDeadlock(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	binder := &panicInventoryBinder{}
	lease.state.binder = binder
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("inventory panic swallowed")
			}
		}()
		_ = lease.Revalidate(context.Background())
	}()
	if !binder.closed || !lease.state.closed || lease.state.active {
		t.Fatal("inventory panic did not revoke and close lease")
	}
	done := make(chan error, 1)
	go func() { done <- lease.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("inventory Close deadlocked after panic")
	}
}

func TestInventoryRevalidateBoundDescriptorRejectsMixedGraphAndCloses(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	other := bound
	other.planSHA[0] ^= 1
	if err := lease.RevalidateBoundDescriptor(context.Background(), other); err == nil {
		t.Fatal("mixed inventory descriptor accepted")
	}
	if err := lease.Revalidate(context.Background()); err == nil {
		t.Fatal("mixed inventory descriptor did not close lease")
	}
}
