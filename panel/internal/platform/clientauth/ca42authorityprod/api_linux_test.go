//go:build linux && (amd64 || arm64)

package ca42authorityprod

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOpaqueProductionAPICommitBindingRevalidateAndSharedClose(t *testing.T) {
	path := ledgerProductionRootFixture(t)
	retained, err := openLedgerRootCapability(path)
	if err != nil {
		t.Fatal(err)
	}
	root := &Root{retained: retained, productionOrigin: true}
	defer root.Close()
	descriptor := ledgerLeaseDescriptor("opaque-api")
	reservation, err := root.beginCurrent(descriptor, time.Unix(1700000100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Revalidate(); !errors.Is(err, errLedgerBusy) {
		t.Fatalf("retained reservation did not keep root busy: %v", err)
	}
	if _, err := reservation.CommittedBinding(); err == nil {
		t.Fatal("uncommitted reservation minted a binding")
	}
	planned, exact, err := reservation.Planned()
	if err != nil || exact || planned.Validate() != nil {
		t.Fatalf("planned exact=%v ledger=%+v err=%v", exact, planned, err)
	}
	committed, recovered, err := reservation.Commit()
	if err != nil || recovered || committed.RecordSHA256 != planned.RecordSHA256 {
		t.Fatalf("commit recovered=%v ledger=%+v err=%v", recovered, committed, err)
	}
	binding, err := reservation.CommittedBinding()
	if err != nil {
		t.Fatal(err)
	}
	values, err := binding.ValuesAt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if values.LedgerBeforeSHA256 != ([32]byte{}) || values.LedgerPlannedSHA256 != committed.RecordSHA256 ||
		values.LedgerCommittedSHA256 != committed.RecordSHA256 || values.DescriptorSHA256 != descriptor.SHA256 ||
		values.ManifestSHA256 != descriptor.ReleaseManifestSHA256 || values.ClockFloorEpoch != descriptor.ClockFloor.Unix() || values.ExactPlan {
		t.Fatalf("opaque binding values mismatch: %+v", values)
	}
	if _, recovered, err := reservation.Commit(); err != nil || !recovered {
		t.Fatalf("exact retry recovered=%v err=%v", recovered, err)
	}
	if err := binding.RevalidateCommitted(context.Background()); err != nil {
		t.Fatalf("binding exact revalidation failed: %v", err)
	}
	copyReservation := *reservation
	if err := copyReservation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := binding.RevalidateCommitted(context.Background()); err == nil {
		t.Fatal("binding survived reservation close")
	}
	if _, _, err := reservation.Planned(); err == nil {
		t.Fatal("original reservation survived shallow-copy close")
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Revalidate(); err != nil {
		t.Fatalf("root did not recover after reservation close: %v", err)
	}
}

func TestOpaqueProductionAPIRejectsZeroAndNonProductionCapabilities(t *testing.T) {
	if _, err := (*Root)(nil).beginCurrent(ledgerLeaseDescriptor("nil"), time.Now()); err == nil {
		t.Fatal("nil root accepted")
	}
	if err := (Binding{}).RevalidateCommitted(context.Background()); err == nil {
		t.Fatal("zero binding accepted")
	}
	if _, err := (Binding{}).ValuesAt(context.Background()); err == nil {
		t.Fatal("zero binding exposed values")
	}
	retained, err := openLedgerRootCapability(ledgerProductionRootFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer retained.close()
	nonProduction := &Root{retained: retained}
	if _, err := nonProduction.beginCurrent(ledgerLeaseDescriptor("non-production"), time.Now()); err == nil {
		t.Fatal("non-production root wrapper gained mutation authority")
	}
}
