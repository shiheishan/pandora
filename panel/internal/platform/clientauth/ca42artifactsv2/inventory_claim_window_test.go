package ca42artifactsv2

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

type inventoryClaimProbe struct {
	now                      time.Time
	order                    []string
	notBefore, notAfter      time.Time
	claimErr                 error
	claimedDescriptorEntries int
}

func (probe *inventoryClaimProbe) ClaimBoundDescriptorWithin(ctx context.Context, descriptor ca42storage.BoundDescriptor, notBefore, notAfter time.Time) error {
	probe.order = append(probe.order, "claim-within")
	probe.notBefore, probe.notAfter = notBefore, notAfter
	if err := ctx.Err(); err != nil {
		return err
	}
	count, err := descriptor.EntryCountAt(probe.now)
	if err != nil {
		return err
	}
	probe.claimedDescriptorEntries = count
	return probe.claimErr
}

func TestVerifiedCopyClaimingInventoryNarrowsEffectiveWindowBeforeClaim(t *testing.T) {
	fixture := newGraphFixture(t, "amd64", "inventory-window")
	set, err := New(fixture.inputs, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := set.SnapshotAt(fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.EffectiveNotBefore.After(fixture.inputs.Plan.NotBefore) || !snapshot.EffectiveNotAfter.Before(fixture.inputs.Plan.NotAfter) {
		t.Fatalf("fixture does not prove a narrower effective window: plan=[%s,%s) effective=[%s,%s)",
			fixture.inputs.Plan.NotBefore, fixture.inputs.Plan.NotAfter, snapshot.EffectiveNotBefore, snapshot.EffectiveNotAfter)
	}
	probe := &inventoryClaimProbe{now: fixture.now}
	if _, err := set.verifiedCopyClaimingInventoryAt(context.Background(), probe, fixture.now); err != nil {
		t.Fatal(err)
	}
	if len(probe.order) != 1 || probe.order[0] != "claim-within" {
		t.Fatalf("inventory window/claim order invalid: %v", probe.order)
	}
	if probe.notBefore != snapshot.EffectiveNotBefore || probe.notAfter != snapshot.EffectiveNotAfter {
		t.Fatalf("inventory received wrong effective window: got=[%s,%s) want=[%s,%s)",
			probe.notBefore, probe.notAfter, snapshot.EffectiveNotBefore, snapshot.EffectiveNotAfter)
	}
	if probe.claimedDescriptorEntries == 0 {
		t.Fatal("inventory claim did not receive the verified bound descriptor")
	}
}

func TestVerifiedCopyClaimingInventoryAtomicClaimFailureIsPreserved(t *testing.T) {
	fixture := newGraphFixture(t, "arm64", "inventory-window-failure")
	set, err := New(fixture.inputs, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("inventory-window-rejected")
	probe := &inventoryClaimProbe{now: fixture.now, claimErr: sentinel}
	if _, err := set.verifiedCopyClaimingInventoryAt(context.Background(), probe, fixture.now); !errors.Is(err, sentinel) {
		t.Fatalf("atomic claim failure was not preserved: %v", err)
	}
	if len(probe.order) != 1 || probe.order[0] != "claim-within" || probe.claimedDescriptorEntries == 0 {
		t.Fatalf("atomic claim call invalid: order=%v entries=%d", probe.order, probe.claimedDescriptorEntries)
	}
}
