package ca42storage

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestInventoryRoleReaderUsesExactLeaseAndExpires(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	entry, err := bound.EntryAt(0, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := entry.SnapshotAt(now)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, int(snapshot.Size))
	for index := range want {
		want[index] = byte(index % 251)
	}
	if err := os.WriteFile(sources[0].Name(), want, 0o600); err != nil {
		t.Fatal(err)
	}
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	var escaped io.ReaderAt
	if err := lease.WithRoleReaderAt(context.Background(), "external_manifest", snapshot.Size, func(_ context.Context, reader io.ReaderAt, size uint64) error {
		if size != snapshot.Size {
			t.Fatalf("role size=%d want=%d", size, snapshot.Size)
		}
		escaped = reader
		buffer := make([]byte, len(want))
		if _, err := reader.ReadAt(buffer, 0); err != nil || string(buffer) != string(want) {
			t.Fatalf("exact retained role read failed: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.ReadAt(make([]byte, 1), 0); !errors.Is(err, errInventoryRoleReaderExpired) {
		t.Fatalf("escaped reader remained live: %v", err)
	}
	if err := lease.Revalidate(context.Background()); err != nil {
		t.Fatalf("successful role read poisoned lease: %v", err)
	}
}

func TestInventoryRoleReaderDeniesUnlistedRoleWithoutCallback(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	called := false
	err = lease.WithRoleReaderAt(context.Background(), "database_dump", MaxOrdinaryEntryBytes, func(context.Context, io.ReaderAt, uint64) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("unlisted role accepted: called=%v err=%v", called, err)
	}
	if err := lease.Revalidate(context.Background()); err != nil {
		t.Fatalf("denied role poisoned lease: %v", err)
	}
}

func TestInventoryRoleReaderFailurePoisonsAndCloses(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected parser denial")
	err = lease.WithRoleReaderAt(context.Background(), "external_manifest", MaxOrdinaryEntryBytes, func(context.Context, io.ReaderAt, uint64) error {
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("callback error classification lost: %v", err)
	}
	if err := lease.Revalidate(context.Background()); err == nil {
		t.Fatal("failed role read did not close inventory")
	}
}

func TestInventoryRoleReaderCloseCancelsOperation(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		readDone <- lease.WithRoleReaderAt(context.Background(), "external_manifest", MaxOrdinaryEntryBytes, func(ctx context.Context, _ io.ReaderAt, _ uint64) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("role operation did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- lease.Close() }()
	select {
	case err := <-readDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("role operation cancellation lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("role operation did not observe Close cancellation")
	}
	select {
	case err := <-closeDone:
		if !errors.Is(err, ErrInventoryRoleBusy) {
			t.Fatalf("active role Close classification lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not complete")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryRoleReaderPanicRevokesAndPoisons(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	var escaped io.ReaderAt
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("callback panic was swallowed")
			}
		}()
		_ = lease.WithRoleReaderAt(context.Background(), "external_manifest", MaxOrdinaryEntryBytes,
			func(_ context.Context, reader io.ReaderAt, _ uint64) error {
				escaped = reader
				panic("injected parser panic")
			})
	}()
	if _, err := escaped.ReadAt(make([]byte, 1), 0); !errors.Is(err, errInventoryRoleReaderExpired) {
		t.Fatalf("panic escaped a live role reader: %v", err)
	}
	if err := lease.Revalidate(context.Background()); err == nil {
		t.Fatal("panic did not poison and close inventory")
	}
	closed := make(chan error, 1)
	go func() { closed <- lease.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close remained blocked after callback panic")
	}
}

func TestReadRoleBytesEnforcesProtocolLimitAndDeniesBinary(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := lease.ReadRoleBytes(context.Background(), "goose_binary", MaxOrdinaryEntryBytes); err == nil {
		t.Fatal("Goose executable bytes were exported")
	}
	called := false
	err = lease.WithRoleReaderAt(context.Background(), "attestation_public_key", MaxOrdinaryEntryBytes,
		func(_ context.Context, _ io.ReaderAt, size uint64) error {
			called = true
			if size > 16<<10 {
				t.Fatalf("role protocol maximum not enforced: %d", size)
			}
			return nil
		})
	if err != nil || !called {
		t.Fatalf("bounded public-key role failed: called=%v err=%v", called, err)
	}
}

func TestInventoryRoleReaderSynchronousCloseDoesNotDeadlock(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- lease.WithRoleReaderAt(context.Background(), "external_manifest", MaxOrdinaryEntryBytes,
			func(context.Context, io.ReaderAt, uint64) error {
				return lease.Close()
			})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrInventoryRoleBusy) {
			t.Fatalf("synchronous Close classification lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("synchronous callback Close deadlocked")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryRoleReaderGoexitRevokesAndReleasesClose(t *testing.T) {
	bound, sources, probe, now := inventoryFixture(t)
	lease, err := retainInventoryWithOps(context.Background(), bound, sources, constantClock(now), probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	var escaped io.ReaderAt
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = lease.WithRoleReaderAt(context.Background(), "external_manifest", MaxOrdinaryEntryBytes,
			func(_ context.Context, reader io.ReaderAt, _ uint64) error {
				escaped = reader
				runtime.Goexit()
				return nil
			})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Goexit did not unwind role operation")
	}
	if _, err := escaped.ReadAt(make([]byte, 1), 0); !errors.Is(err, errInventoryRoleReaderExpired) {
		t.Fatalf("Goexit escaped a live role reader: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- lease.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close remained blocked after Goexit")
	}
}
