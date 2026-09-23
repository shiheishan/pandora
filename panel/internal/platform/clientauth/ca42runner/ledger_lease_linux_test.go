//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"golang.org/x/sys/unix"
)

func ledgerLeaseDescriptor(label string) ca42authority.Descriptor {
	digest := func(value string) [sha256.Size]byte { return sha256.Sum256([]byte(value)) }
	return ca42authority.Descriptor{
		RootKeysetID: "roots-a", LedgerID: strings.Repeat("1", 64), AuthorityEpoch: 1, AuthoritySequence: 1,
		AuthorizationMode: ca42authority.ModeNormal, AttemptID: "attempt-" + label,
		ReleaseManifestSHA256: digest("manifest-" + label), SHA256: digest("descriptor-" + label),
		ClockFloor: time.Unix(1700000000, 0).UTC(),
	}
}

func openLedgerLeaseRoot(t *testing.T) (*os.File, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned ledger fixture")
	}
	path := filepath.Join(t.TempDir(), "ledger-root")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return root, path
}

func TestLedgerReservationLeasePlanCommitExactRetryAndLock(t *testing.T) {
	root, rootPath := openLedgerLeaseRoot(t)
	defer root.Close()
	now := time.Unix(1700000100, 0).UTC()
	descriptor := ledgerLeaseDescriptor("a")
	lease, err := beginLedgerReservation(int(root.Fd()), nil, descriptor, now)
	if err != nil {
		t.Fatal(err)
	}
	planned, exact, err := lease.plannedLedger()
	if err != nil || exact || planned.Validate() != nil {
		t.Fatalf("planned exact=%v ledger=%+v err=%v", exact, planned, err)
	}
	if contender, err := beginLedgerReservation(int(root.Fd()), nil, descriptor, now); err == nil {
		_ = contender.close()
		t.Fatal("competing ledger lease acquired exclusive lock")
	} else if !errors.Is(err, ErrLedgerBusy) {
		t.Fatalf("ledger contention was not classified: %v", err)
	}
	committed, recovered, err := lease.commit()
	if err != nil || recovered || committed.RecordSHA256 != planned.RecordSHA256 {
		t.Fatalf("commit recovered=%v err=%v", recovered, err)
	}
	if _, recovered, err := lease.commit(); err != nil || !recovered {
		t.Fatalf("same lease exact commit recovered=%v err=%v", recovered, err)
	}
	copyLease := *lease
	if err := lease.close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := copyLease.plannedLedger(); err == nil {
		t.Fatal("shallow-copy lease remained authoritative after close")
	}
	if err := copyLease.close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(rootPath, LedgerPath))
	if err != nil {
		t.Fatal(err)
	}
	current, err := ca42authority.ParseLedger(data)
	if err != nil || current.RecordSHA256 != planned.RecordSHA256 {
		t.Fatalf("durable ledger invalid: %v", err)
	}
	retry, err := beginLedgerReservation(int(root.Fd()), &current, descriptor, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer retry.close()
	if _, exact, err := retry.plannedLedger(); err != nil || !exact {
		t.Fatalf("retry plan exact=%v err=%v", exact, err)
	}
	if _, recovered, err := retry.commit(); err != nil || !recovered {
		t.Fatalf("retry commit recovered=%v err=%v", recovered, err)
	}
}

func TestLedgerReservationLeaseAcquireThenPlanUsesPersistedReservationTime(t *testing.T) {
	root, _ := openLedgerLeaseRoot(t)
	defer root.Close()
	lease, err := acquireLedgerReservation(int(root.Fd()), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	if _, _, err := lease.plannedLedger(); err == nil {
		t.Fatal("unplanned acquired ledger exposed a planned record")
	}
	reservedAt := time.Unix(1700000100, 0).UTC()
	planned, exact, err := lease.plan(ledgerLeaseDescriptor("split"), reservedAt)
	if err != nil || exact || planned.LastTrustedEpoch != reservedAt.Unix() {
		t.Fatalf("split plan exact=%v ledger=%+v err=%v", exact, planned, err)
	}
	replayed, replayExact, err := lease.plan(ledgerLeaseDescriptor("split"), reservedAt)
	if err != nil || replayExact != exact || replayed.RecordSHA256 != planned.RecordSHA256 {
		t.Fatalf("same persisted reservation time changed plan: exact=%v ledger=%+v err=%v", replayExact, replayed, err)
	}
	if _, _, err := lease.plan(ledgerLeaseDescriptor("split"), reservedAt.Add(time.Second)); err == nil {
		t.Fatal("different restart time changed an already-frozen ledger plan")
	}
}

func TestLedgerReservationLeaseCASConflictDoesNotOverwrite(t *testing.T) {
	root, rootPath := openLedgerLeaseRoot(t)
	defer root.Close()
	now := time.Unix(1700000100, 0).UTC()
	lease, err := beginLedgerReservation(int(root.Fd()), nil, ledgerLeaseDescriptor("a"), now)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	foreign, _, err := ca42authority.Advance(nil, ledgerLeaseDescriptor("foreign"), now)
	if err != nil {
		t.Fatal(err)
	}
	foreignBytes, err := foreign.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, LedgerPath), foreignBytes, 0400); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lease.commit(); err == nil {
		t.Fatal("CAS conflict overwrote foreign ledger")
	}
	after, err := os.ReadFile(filepath.Join(rootPath, LedgerPath))
	if err != nil || string(after) != string(foreignBytes) {
		t.Fatalf("foreign ledger changed err=%v", err)
	}
}

func TestLedgerDeterministicCandidateRecoveryAndConflict(t *testing.T) {
	for _, divergent := range []bool{false, true} {
		t.Run(fmt.Sprintf("divergent_%v", divergent), func(t *testing.T) {
			root, rootPath := openLedgerLeaseRoot(t)
			defer root.Close()
			planned, _, err := ca42authority.Advance(nil, ledgerLeaseDescriptor("candidate"), time.Unix(1700000100, 0).UTC())
			if err != nil {
				t.Fatal(err)
			}
			data, err := planned.CanonicalBytes()
			if err != nil {
				t.Fatal(err)
			}
			candidateName := fmt.Sprintf("authority-ledger.candidate.%x", planned.RecordSHA256[:])
			candidateBytes := data
			if divergent {
				candidateBytes = []byte("foreign deterministic candidate\n")
			}
			if err := os.WriteFile(filepath.Join(rootPath, candidateName), candidateBytes, 0400); err != nil {
				t.Fatal(err)
			}
			err = atomicReplaceLedger(int(root.Fd()), data)
			if divergent {
				if !errors.Is(err, ErrLedgerCommitAmbiguous) {
					t.Fatalf("divergent deterministic candidate was not ambiguous: %v", err)
				}
				if _, statErr := os.Stat(filepath.Join(rootPath, LedgerPath)); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("divergent candidate published ledger: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, statErr := os.Stat(filepath.Join(rootPath, candidateName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("recovered candidate basename remained: %v", statErr)
			}
			current, readErr := readCurrentLedger(int(root.Fd()))
			if readErr != nil || current == nil || current.RecordSHA256 != planned.RecordSHA256 {
				t.Fatalf("deterministic candidate did not recover exact ledger: current=%+v err=%v", current, readErr)
			}
		})
	}
}

func TestLedgerReservationLeaseRejectsLockBasenameReplacement(t *testing.T) {
	root, rootPath := openLedgerLeaseRoot(t)
	defer root.Close()
	lease, err := beginLedgerReservation(int(root.Fd()), nil, ledgerLeaseDescriptor("a"), time.Unix(1700000100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	lockPath := filepath.Join(rootPath, ledgerLockName)
	movedPath := filepath.Join(rootPath, "moved-lock")
	if err := os.Rename(lockPath, movedPath); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	replacement.Close()
	if _, _, err := lease.plannedLedger(); err == nil {
		t.Fatal("replacement lock basename accepted")
	}
	if _, err := os.Stat(filepath.Join(rootPath, LedgerPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement test wrote ledger: %v", err)
	}
}

func TestLedgerReservationLeaseDescriptorsCLOEXECAndLegacyWrapper(t *testing.T) {
	root, _ := openLedgerLeaseRoot(t)
	defer root.Close()
	now := time.Unix(1700000100, 0).UTC()
	descriptor := ledgerLeaseDescriptor("a")
	lease, err := beginLedgerReservation(int(root.Fd()), nil, descriptor, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range []int{lease.state.directoryFD, lease.state.lockFD} {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
		if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
			t.Fatalf("fd %d CLOEXEC flags=%x errno=%v", fd, flags, errno)
		}
	}
	if err := lease.close(); err != nil {
		t.Fatal(err)
	}
	committed, recovered, err := reserveLedgerAt(int(root.Fd()), nil, descriptor, now)
	if err != nil || recovered {
		t.Fatalf("legacy wrapper recovered=%v err=%v", recovered, err)
	}
	if err := committed.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, recovered, err := reserveLedgerAt(int(root.Fd()), &committed, descriptor, now.Add(time.Second)); err != nil || !recovered {
		t.Fatalf("legacy wrapper retry recovered=%v err=%v", recovered, err)
	}
	if err := unix.Fsync(int(root.Fd())); err != nil {
		t.Fatal(err)
	}
}
