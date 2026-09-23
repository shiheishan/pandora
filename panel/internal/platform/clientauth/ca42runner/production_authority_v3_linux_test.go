//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"golang.org/x/sys/unix"
)

func TestAuthorityV3LeaseRetainsFixedCapabilitiesAndSharesClose(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	copyOfLease := *lease
	if err := copyOfLease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.revalidate(context.Background()); !errors.Is(err, errProductionAuthorityV3Busy) {
		t.Fatalf("closed shallow copy remained usable: %v", err)
	}
}

func TestAuthorityV3LeasePersistsLedgerAndExactReopens(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	first, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.ledgerDir, LedgerPath))
	if err != nil {
		t.Fatal(err)
	}
	committed, err := ca42authority.ParseLedger(data)
	if err != nil || committed.DescriptorSHA256 != first.state.descriptorSHA || committed.LastAttemptID != "attempt-ca42-1" {
		t.Fatalf("persisted authority ledger mismatch: ledger=%+v err=%v", committed, err)
	}
	before := ledgerFileFingerprint(t, fixture.ledgerDir)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if !second.state.ledgerBinding.exactRetry || second.state.ledgerBinding.recordSHA != committed.RecordSHA256 {
		t.Fatalf("reopen did not bind exact persisted ledger: %+v", second.state.ledgerBinding)
	}
	if err := second.revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := ledgerFileFingerprint(t, fixture.ledgerDir)
	if before != after {
		t.Fatalf("exact reopen rewrote the durable ledger: before=%+v after=%+v", before, after)
	}
	entries, err := os.ReadDir(fixture.ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != ledgerLockName || entries[1].Name() != LedgerPath {
		t.Fatalf("exact reopen left unexpected ledger inventory: %+v", entries)
	}
}

func TestAuthorityV3LeaseLedgerRejectsClockRollbackAcrossReopen(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	first, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	baseline := ledgerFileFingerprint(t, fixture.ledgerDir)
	exact, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatalf("same-clock exact reopen failed before rollback probe: %v", err)
	}
	if err := exact.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.clock = time.Unix(1700000099, 0)
	second, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if second != nil || err == nil {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("persisted ledger accepted clock rollback: lease=%v err=%v", second, err)
	}
	if after := ledgerFileFingerprint(t, fixture.ledgerDir); after != baseline {
		t.Fatalf("rejected rollback mutated the durable ledger: before=%+v after=%+v", baseline, after)
	}
	entries, readDirErr := os.ReadDir(fixture.ledgerDir)
	if readDirErr != nil {
		t.Fatal(readDirErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "authority-ledger.candidate.") {
			t.Fatalf("rejected rollback left a candidate: %s", entry.Name())
		}
	}
	fixture.clock = time.Unix(1700000100, 0)
	recovered, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatalf("valid exact reopen failed after rejected rollback: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityV3LeaseHoldsLedgerReservationAndRecoversCommittedRecord(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()

	contender, err := openLedgerRootCapability(fixture.ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.close()
	if competing, err := contender.beginCurrentReservation(fixture.descriptor, fixture.clock); err == nil {
		_ = competing.close()
		t.Fatal("authority lease did not retain the durable ledger lock")
	} else if !errors.Is(err, ErrLedgerBusy) {
		t.Fatalf("retained authority ledger contention was not classified: %v", err)
	}

	committed, recovered, err := lease.state.ledgerReservation.commit()
	if err != nil || !recovered || committed.RecordSHA256 != lease.state.ledgerBinding.recordSHA {
		t.Fatalf("committed authority record was not recovered exactly: recovered=%v ledger=%+v err=%v", recovered, committed, err)
	}
	if err := lease.revalidate(context.Background()); err != nil {
		t.Fatalf("lease failed after exact committed-record recovery: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	exact, err := contender.beginCurrentReservation(fixture.descriptor, fixture.clock)
	if err != nil {
		t.Fatalf("ledger lock remained held after authority close: %v", err)
	}
	planned, isExact, err := exact.plannedLedger()
	if err != nil || !isExact || planned.RecordSHA256 != committed.RecordSHA256 {
		t.Fatalf("post-close exact reservation mismatch: exact=%v ledger=%+v err=%v", isExact, planned, err)
	}
	if _, recovered, err := exact.commit(); err != nil || !recovered {
		t.Fatalf("post-close exact commit did not recover existing record: recovered=%v err=%v", recovered, err)
	}
	if err := exact.close(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityV3LeaseRevalidationRejectsValidForeignLedgerReplacement(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()

	foreign, _, err := ca42authority.Advance(nil, ledgerLeaseDescriptor("foreign-revalidation"), fixture.clock)
	if err != nil {
		t.Fatal(err)
	}
	foreignBytes, err := foreign.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(fixture.ledgerDir, "foreign-ledger.tmp")
	if err := os.WriteFile(temporary, foreignBytes, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(temporary, 0o400); err != nil {
		t.Fatal(err)
	}
	if parsed, err := ca42authority.ParseLedger(foreignBytes); err != nil || parsed.RecordSHA256 != foreign.RecordSHA256 {
		t.Fatalf("foreign replacement fixture was not a valid ledger: ledger=%+v err=%v", parsed, err)
	}
	if err := os.Rename(temporary, filepath.Join(fixture.ledgerDir, LedgerPath)); err != nil {
		t.Fatal(err)
	}
	if err := lease.revalidate(context.Background()); err == nil {
		t.Fatal("authority revalidation accepted a valid foreign durable ledger")
	}
	if err := lease.revalidate(context.Background()); !errors.Is(err, errProductionAuthorityV3Busy) {
		t.Fatalf("foreign ledger replacement did not poison authority lease: %v", err)
	}
}

func TestAuthorityV3LeaseRejectsByteIdenticalCanonicalReplacement(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(TrustRootPath, AuthorityPath)
	detached := filepath.Join(fixture.runnerDir, "authority.detached")
	if err := os.Rename(original, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, fixture.authority, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(original, 0o400); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(TrustRootPath); err != nil || len(entries) != 2 {
		t.Fatalf("replacement test did not preserve exact inventory: entries=%d err=%v", len(entries), err)
	}
	if err := lease.revalidate(context.Background()); err == nil {
		t.Fatal("byte-identical canonical authority replacement accepted")
	}
	if err := lease.revalidate(context.Background()); !errors.Is(err, errProductionAuthorityV3Busy) {
		t.Fatalf("replacement failure did not poison lease: %v", err)
	}
}

func TestAuthorityV3LeaseRejectsByteIdenticalHostReplacement(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(TrustRootPath, HostIdentityPath)
	detached := filepath.Join(fixture.runnerDir, "host.detached")
	hostBytes, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, hostBytes, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(original, 0o400); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(TrustRootPath); err != nil || len(entries) != 2 {
		t.Fatalf("replacement test did not preserve exact inventory: entries=%d err=%v", len(entries), err)
	}
	if err := lease.revalidate(context.Background()); err == nil {
		t.Fatal("byte-identical canonical host replacement accepted")
	}
}

func TestAuthorityV3LeaseFinalCanonicalRebindCatchesTimeHookReplacement(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(TrustRootPath, HostIdentityPath)
	detached := filepath.Join(fixture.runnerDir, "host-time-hook.detached")
	hostBytes, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	lease.state.ops.now = func() time.Time {
		if err := os.Rename(original, detached); err != nil {
			t.Error(err)
			return fixture.clock
		}
		if err := os.WriteFile(original, hostBytes, 0o400); err != nil {
			t.Error(err)
			return fixture.clock
		}
		if err := os.Chmod(original, 0o400); err != nil {
			t.Error(err)
		}
		return fixture.clock
	}
	if err := lease.revalidate(context.Background()); err == nil {
		t.Fatal("canonical replacement during trusted-time/parse window accepted")
	}
}

func TestAuthorityV3LeaseFinalClockSampleRejectsExpiryDuringCanonicalPass(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	originalOpenRoot := lease.state.ops.openTrustRoot
	count := 0
	lease.state.ops.openTrustRoot = func() (*os.File, error) {
		count++
		root, err := originalOpenRoot()
		if count == 2 {
			fixture.clock = time.Unix(1700000301, 0)
		}
		return root, err
	}
	if err := lease.revalidate(context.Background()); err == nil {
		t.Fatal("descriptor expiry during final canonical pass was accepted")
	}
}

func TestAuthorityV3LeaseRejectsClockRollback(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock = fixture.clock.Add(-time.Second)
	if err := lease.revalidate(context.Background()); err == nil {
		t.Fatal("wall clock rollback accepted")
	}
}

func TestAuthorityV3LeaseCloseCancelsAndWaitsForActiveRevalidation(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	releaseClock := make(chan struct{})
	lease.state.ops.now = func() time.Time {
		close(started)
		<-releaseClock
		return fixture.clock
	}
	revalidateDone := make(chan error, 1)
	go func() { revalidateDone <- lease.revalidate(context.Background()) }()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- lease.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before active operation exited: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseClock)
	if err := <-revalidateDone; err == nil {
		t.Fatal("canceled active revalidation succeeded")
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityV3LeasePanicPoisonsAndCloses(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	lease.state.ops.now = func() time.Time { panic("authority-v3-clock-panic") }
	func() {
		defer func() {
			if recovered := recover(); recovered != "authority-v3-clock-panic" {
				t.Fatalf("unexpected panic: %v", recovered)
			}
		}()
		_ = lease.revalidate(context.Background())
	}()
	if err := lease.revalidate(context.Background()); !errors.Is(err, errProductionAuthorityV3Busy) {
		t.Fatalf("panic did not poison lease: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityV3LeaseGoexitPoisonsAndCloses(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	lease, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	lease.state.ops.now = func() time.Time {
		runtime.Goexit()
		return time.Time{}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = lease.revalidate(context.Background())
		t.Error("runtime.Goexit returned")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime.Goexit cleanup deadlocked")
	}
	if err := lease.revalidate(context.Background()); !errors.Is(err, errProductionAuthorityV3Busy) {
		t.Fatalf("Goexit did not poison lease: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProductionAuthorityV3SourceBuildFailsClosed(t *testing.T) {
	lease, err := openProductionAuthorityV3Lease(context.Background(), "attempt-ca42-1")
	if unix.Geteuid() != 0 {
		if lease != nil || !errors.Is(err, errProductionAuthorityV3Unavailable) {
			t.Fatalf("non-root production opener did not fail closed: lease=%v err=%v", lease, err)
		}
		return
	}
	if lease != nil || !errors.Is(err, errCompiledRootsUnprovisioned) {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatalf("unprovisioned source build opened production authority: lease=%v err=%v", lease, err)
	}
}

func TestProductionAuthorityV3WrapperEnforcesOriginAndSharedClose(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	raw, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	production := &productionAuthorityV3Lease{retained: raw}
	if err := production.Revalidate(context.Background()); !errors.Is(err, errProductionAuthorityV3Unavailable) {
		t.Fatalf("raw lease crossed production provenance gate: %v", err)
	}
	raw.state.mu.Lock()
	raw.state.productionOrigin = true // method-level test only; fixed opener owns this transition in production.
	raw.state.mu.Unlock()
	if err := production.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	binding, err := production.releaseAuthorityBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if binding.ManifestSHA256 != fixture.descriptor.ReleaseManifestSHA256 ||
		binding.SignerSHA256 != fixture.descriptor.ReleaseSignerSHA256 ||
		binding.BindingSHA256 != fixture.descriptor.BindingSHA256 ||
		binding.Epoch != fixture.descriptor.AuthorityEpoch || binding.Sequence != fixture.descriptor.AuthoritySequence ||
		binding.ReleaseSignerKeyID != fixture.descriptor.ReleaseSignerKeyID ||
		string(binding.PublicKey) != string(fixture.descriptor.ReleaseSignerKey) {
		t.Fatal("production release authority binding mismatch")
	}
	binding.PublicKey[0] ^= 0xff
	secondBinding, err := production.releaseAuthorityBinding(context.Background())
	if err != nil || string(secondBinding.PublicKey) != string(fixture.descriptor.ReleaseSignerKey) {
		t.Fatalf("caller mutated retained production signer: %v", err)
	}
	copyOfProduction := *production
	if err := copyOfProduction.Close(); err != nil {
		t.Fatal(err)
	}
	if err := production.Revalidate(context.Background()); !errors.Is(err, errProductionAuthorityV3Unavailable) {
		t.Fatalf("production shallow copy remained usable after close: %v", err)
	}
}

func TestProductionAuthorityV3WrapperCanceledRevalidatePoisonsLease(t *testing.T) {
	fixture := newAuthorityV3FixedFixture(t)
	raw, err := openAuthorityV3LeaseWithOps(context.Background(), "attempt-ca42-1", fixture.ops)
	if err != nil {
		t.Fatal(err)
	}
	raw.state.mu.Lock()
	raw.state.productionOrigin = true // method-level test only.
	raw.state.mu.Unlock()
	production := &productionAuthorityV3Lease{retained: raw}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := production.Revalidate(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled production revalidation was not preserved: %v", err)
	}
	if err := production.Revalidate(context.Background()); !errors.Is(err, errProductionAuthorityV3Unavailable) {
		t.Fatalf("canceled production lease was not poisoned: %v", err)
	}
}

type authorityV3FixedFixture struct {
	ops        authorityV3Ops
	authority  []byte
	descriptor ca42authority.Descriptor
	clock      time.Time
	runnerDir  string
	ledgerDir  string
}

func newAuthorityV3FixedFixture(t *testing.T) *authorityV3FixedFixture {
	t.Helper()
	if unix.Geteuid() != 0 {
		t.Skip("requires root")
	}
	if os.Getenv("PANDORA_CA42_AUTHORITY_FIXED_ROOT_TEST") != "1" {
		t.Skip("fixed /etc trust-root test is opt-in")
	}
	if _, err := os.Lstat(TrustRootPath); !errors.Is(err, os.ErrNotExist) {
		t.Skip("fixed CA42 trust root already exists")
	}
	parent := filepath.Dir(TrustRootPath)
	createdParent := false
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		createdParent = true
	} else if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(TrustRootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(TrustRootPath)
		if createdParent {
			_ = os.Remove(parent)
		}
	})

	runnerDir, err := os.MkdirTemp("/root", "pandora-authority-v3-runner.")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runnerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runnerDir) })
	runnerBytes := []byte("ELF-fixture-root-runner-v3")
	runnerPath := filepath.Join(runnerDir, "runner.fixture")
	if err := os.WriteFile(runnerPath, runnerBytes, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runnerPath, 0o500); err != nil {
		t.Fatal(err)
	}
	runnerSHA := sha256.Sum256(runnerBytes)
	ledgerDir := filepath.Join(runnerDir, "ledger")
	if err := os.Mkdir(ledgerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hostBytes := []byte("authority-v3-host-identity")
	hostSHA := sha256.Sum256(hostBytes)

	private := make([]ed25519.PrivateKey, 3)
	keys := make([]ca42authority.RootKey, 3)
	for index := range private {
		private[index] = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(index + 21)}, ed25519.SeedSize))
		keys[index] = ca42authority.RootKey{ID: "root-" + string(rune('a'+index)), PublicKey: private[index].Public().(ed25519.PublicKey)}
	}
	roots := ca42authority.RootKeyset{ID: "pandora-ca42-roots-v1", Quorum: 2, Keys: keys}
	releasePrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{55}, ed25519.SeedSize))
	manifestSHA := sha256.Sum256([]byte("authority-v3-release-manifest"))
	authority := buildAuthorityForArchitecture(t, roots, private, releasePrivate.Public().(ed25519.PublicKey), hostSHA, runnerSHA, manifestSHA, runtime.GOARCH)
	descriptor, err := ca42authority.ParseAndVerify(authority, roots, runtime.GOARCH, hostSHA, time.Unix(1700000100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(TrustRootPath, AuthorityPath), authority, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(TrustRootPath, AuthorityPath), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(TrustRootPath, HostIdentityPath), hostBytes, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(TrustRootPath, HostIdentityPath), 0o400); err != nil {
		t.Fatal(err)
	}

	fixture := &authorityV3FixedFixture{authority: authority, descriptor: descriptor, clock: time.Unix(1700000100, 0), runnerDir: runnerDir, ledgerDir: ledgerDir}
	fixture.ops = authorityV3Ops{
		now:      func() time.Time { return fixture.clock },
		boottime: func() (time.Duration, error) { return 10 * time.Minute, nil },
		openRoots: func(ctx context.Context) (*productionRootLease, error) {
			return openProductionRootLeaseWith(ctx, func() (ca42authority.RootKeyset, error) { return roots, nil })
		},
		openTrustRoot: func() (*os.File, error) { return openFixedTrustedDirectory(TrustRootPath) },
		openRunner: func(ctx context.Context) (*os.File, [sha256.Size]byte, error) {
			parent, err := openFixedTrustedDirectory(runnerDir)
			if err != nil {
				return nil, [sha256.Size]byte{}, err
			}
			defer parent.Close()
			file, data, digest, err := openRootOwnedExecutableAt(ctx, int(parent.Fd()), "runner.fixture", maxRootRunnerBytes, &runnerSHA)
			zeroBytes(data)
			return file, digest, err
		},
		openLedgerRoot: func() (*ledgerRootCapability, error) { return openLedgerRootCapability(ledgerDir) },
	}
	return fixture
}

type ledgerFingerprint struct {
	SHA256 [sha256.Size]byte
	Size   int64
	Ino    uint64
	MTime  unix.Timespec
	CTime  unix.Timespec
}

func ledgerFileFingerprint(t *testing.T, root string) ledgerFingerprint {
	t.Helper()
	path := filepath.Join(root, LedgerPath)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Fatal(err)
	}
	return ledgerFingerprint{
		SHA256: sha256.Sum256(data),
		Size:   stat.Size,
		Ino:    stat.Ino,
		MTime:  stat.Mtim,
		CTime:  stat.Ctim,
	}
}
