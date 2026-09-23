//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
)

func ledgerProductionRootFixture(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixture")
	}
	base := os.Getenv("PANDORA_CA42_LEDGER_TEST_ROOT")
	if base == "" {
		base = "/root"
	}
	if !filepath.IsAbs(base) || filepath.Clean(base) != base || (base != "/root" && !strings.HasPrefix(base, "/root/")) {
		t.Fatalf("ledger test root must be canonical beneath /root: %q", base)
	}
	path, err := os.MkdirTemp(base, "pandora-ca42-ledger-root.")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

func TestLedgerProductionRootBoundPlanCommitAndSharedClose(t *testing.T) {
	path := ledgerProductionRootFixture(t)
	first, err := openLedgerRootCapability(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	second, err := openLedgerRootCapability(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	now := time.Unix(1700000100, 0).UTC()
	descriptor := ledgerLeaseDescriptor("bound")
	bound, err := first.beginReservation(nil, descriptor, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.validate(); !errors.Is(err, ErrLedgerBusy) {
		t.Fatalf("active bound lease did not make root validate nonblocking-busy: %v", err)
	}
	if err := first.close(); !errors.Is(err, ErrLedgerBusy) {
		t.Fatalf("active bound lease did not make root close nonblocking-busy: %v", err)
	}
	if nested, err := first.beginReservation(nil, descriptor, now); err == nil {
		_ = nested.close()
		t.Fatal("same capability began a nested ledger reservation")
	} else if !errors.Is(err, ErrLedgerBusy) {
		t.Fatalf("nested reservation was not classified busy: %v", err)
	}
	if competing, err := second.beginReservation(nil, descriptor, now); err == nil {
		_ = competing.close()
		t.Fatal("competing canonical ledger capability acquired the lock")
	} else if !errors.Is(err, ErrLedgerBusy) {
		t.Fatalf("canonical ledger contention was not classified: %v", err)
	}
	planned, exact, err := bound.plannedLedger()
	if err != nil || exact || planned.Validate() != nil {
		t.Fatalf("bound plan exact=%v ledger=%+v err=%v", exact, planned, err)
	}
	committed, recovered, err := bound.commit()
	if err != nil || recovered || committed.RecordSHA256 != planned.RecordSHA256 {
		t.Fatalf("bound commit recovered=%v ledger=%+v err=%v", recovered, committed, err)
	}
	copyBound := *bound
	if err := bound.close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := copyBound.plannedLedger(); err == nil {
		t.Fatal("shallow-copy bound ledger lease remained authoritative")
	}
	if err := copyBound.close(); err != nil {
		t.Fatal(err)
	}
	if err := first.validate(); err != nil {
		t.Fatalf("canonical root invalid after lease close: %v", err)
	}
}

func TestLedgerProductionRootCapabilityShallowCopySharesCloseState(t *testing.T) {
	capability, err := openLedgerRootCapability(ledgerProductionRootFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	copyCapability := *capability
	if err := copyCapability.close(); err != nil {
		t.Fatal(err)
	}
	if err := capability.validate(); err == nil {
		t.Fatal("original ledger root capability remained authoritative after shallow-copy close")
	}
	if err := capability.close(); err != nil {
		t.Fatalf("shared ledger root close was not idempotent: %v", err)
	}
}

func TestLedgerProductionRootRejectsCanonicalReplacementBeforeCommit(t *testing.T) {
	path := ledgerProductionRootFixture(t)
	capability, err := openLedgerRootCapability(path)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.close()
	bound, err := capability.beginReservation(nil, ledgerLeaseDescriptor("replace"), time.Unix(1700000100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer bound.close()
	displaced := path + ".old"
	t.Cleanup(func() { _ = os.RemoveAll(displaced) })
	if err := os.Rename(path, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bound.commit(); err == nil {
		t.Fatal("bound ledger committed after canonical root replacement")
	}
	if _, err := os.Stat(filepath.Join(displaced, LedgerPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement failure mutated displaced ledger: %v", err)
	}
	if err := capability.validateLocked(); err == nil {
		t.Fatal("canonical ledger replacement accepted")
	}
}

func TestLedgerProductionRootCurrentReservationRejectsReadBeforeFlockRace(t *testing.T) {
	path := ledgerProductionRootFixture(t)
	reader, err := openLedgerRootCapability(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()
	writer, err := openLedgerRootCapability(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.close()

	now := time.Unix(1700000100, 0).UTC()
	foreignDescriptor := ledgerLeaseDescriptor("foreign-between-read-and-flock")
	bound, err := reader.beginCurrentReservationWithHook(foreignDescriptor, now, func() error {
		foreign, err := writer.beginReservation(nil, foreignDescriptor, now)
		if err != nil {
			return err
		}
		defer foreign.close()
		_, _, err = foreign.commit()
		return err
	})
	if bound != nil {
		_ = bound.close()
		t.Fatal("stale snapshot acquired reservation after a foreign commit")
	}
	if !errors.Is(err, ca42authority.ErrLedgerCASConflict) {
		t.Fatalf("read-before-flock race was not classified as a snapshot CAS conflict: %v", err)
	}

	data, readErr := os.ReadFile(filepath.Join(path, LedgerPath))
	if readErr != nil {
		t.Fatal(readErr)
	}
	committed, parseErr := ca42authority.ParseLedger(data)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if committed.DescriptorSHA256 != foreignDescriptor.SHA256 {
		t.Fatalf("CAS failure damaged the foreign record: %+v", committed)
	}
	entries, readDirErr := os.ReadDir(path)
	if readDirErr != nil {
		t.Fatal(readDirErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "authority-ledger.candidate.") {
			t.Fatalf("CAS failure left an ambiguous candidate: %s", entry.Name())
		}
	}
}
