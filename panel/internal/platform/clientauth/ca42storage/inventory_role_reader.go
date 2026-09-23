package ca42storage

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

var errInventoryRoleReaderExpired = errors.New("fs-verity inventory role reader expired")

// ErrInventoryRoleBusy tells a synchronous callback that Close has canceled
// the role operation and that the callback must return before final closure.
var ErrInventoryRoleBusy = errors.New("fs-verity inventory role operation closing")

func readableAttemptRoleMaximum(role string) (uint64, bool) {
	switch role {
	case "external_manifest":
		return 4 << 20, true
	case "attestation_public_key":
		return 16 << 10, true
	case "credential_source_descriptor":
		return 16 << 10, true
	case "runtime_closure_manifest":
		return 32 << 10, true
	case "goose_binary":
		return 256 << 20, true
	case "goose_build_info":
		return 16 << 10, true
	default:
		return 0, false
	}
}

// InventoryRoleOperation receives a reader that is valid only for the dynamic
// callback scope. Close cancels its context; a synchronous Close from inside
// the callback returns ErrInventoryRoleBusy instead of deadlocking.
type InventoryRoleOperation func(context.Context, io.ReaderAt, uint64) error

type inventoryRoleReader struct {
	mu     sync.Mutex
	file   *os.File
	active bool
}

func (reader *inventoryRoleReader) ReadAt(output []byte, offset int64) (int, error) {
	if reader == nil || offset < 0 {
		return 0, errInventoryRoleReaderExpired
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if !reader.active || reader.file == nil {
		return 0, errInventoryRoleReaderExpired
	}
	return reader.file.ReadAt(output, offset)
}

func (reader *inventoryRoleReader) revoke() {
	if reader == nil {
		return
	}
	reader.mu.Lock()
	reader.active = false
	reader.file = nil
	reader.mu.Unlock()
}

// WithRoleReaderAt provides a non-FD reader for exact retained parser input.
// The reader is revoked on return, panic, or Goexit, and full inventory
// revalidation brackets every successful callback.
func (lease *InventoryLease) WithRoleReaderAt(ctx context.Context, role string, maximum uint64, operation InventoryRoleOperation) error {
	if lease == nil || lease.state == nil || ctx == nil || operation == nil || maximum == 0 || maximum > MaxOrdinaryEntryBytes {
		return errors.New("fs-verity inventory role reader input invalid")
	}
	roleMaximum, allowed := readableAttemptRoleMaximum(role)
	if !allowed {
		return errors.New("fs-verity inventory role reader denied")
	}
	if maximum > roleMaximum {
		maximum = roleMaximum
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
	state.active, state.roleActive, state.cancel = true, true, cancel
	descriptor, retainedEntries, paths, retainedLeases := state.descriptor, state.entries, state.paths, state.leases
	lastTime, clock, binder := state.lastTime, state.clock, state.binder
	notBefore, notAfter := state.notBefore, state.notAfter
	state.mu.Unlock()
	finalized := false
	defer func() {
		// This path covers callback panic and runtime.Goexit. It does not recover
		// the panic; it only revokes the capability and releases Close waiters.
		if finalized {
			return
		}
		cancel()
		state.mu.Lock()
		state.cancel, state.active, state.roleActive, state.poisoned = nil, false, false, true
		state.cond.Broadcast()
		_ = state.closeOwnedLocked()
		state.mu.Unlock()
	}()

	result := revalidateInventory(operationContext, descriptor, retainedEntries, paths, retainedLeases, clock, &lastTime, notBefore, notAfter, binder)
	if result == nil {
		result = useInventoryRole(operationContext, descriptor, retainedEntries, retainedLeases, clock, &lastTime, notBefore, notAfter, role, maximum, operation)
	}
	if result == nil {
		result = revalidateInventory(operationContext, descriptor, retainedEntries, paths, retainedLeases, clock, &lastTime, notBefore, notAfter, binder)
	}
	cancel()
	state.mu.Lock()
	state.cancel, state.active, state.roleActive = nil, false, false
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

func useInventoryRole(ctx context.Context, descriptor BoundDescriptor, retainedEntries []BoundEntry, retainedLeases []*FDLease,
	clock inventoryClock, lastTime *time.Time, notBefore, notAfter time.Time, role string, maximum uint64, operation InventoryRoleOperation) error {
	now, err := sampleInventoryClock(clock, lastTime)
	if err != nil || now.Before(notBefore) || !now.Before(notAfter) {
		return errors.Join(errors.New("fs-verity inventory role validity expired"), err)
	}
	_, entries, err := descriptor.cachedEntriesAt(now)
	if err != nil || len(entries) != len(retainedEntries) || len(entries) != len(retainedLeases) {
		return errors.Join(errors.New("fs-verity inventory role completeness changed"), err)
	}
	index := -1
	var size uint64
	for candidate := 0; candidate < AttemptEntryCount; candidate++ {
		snapshot, snapshotErr := entries[candidate].SnapshotAt(now)
		if snapshotErr != nil {
			return snapshotErr
		}
		if snapshot.Role == role {
			if index >= 0 || snapshot.Scope != "attempt" || snapshot.Size == 0 || snapshot.Size > maximum {
				return errors.New("fs-verity inventory role identity invalid")
			}
			index, size = candidate, snapshot.Size
		}
	}
	if index < 0 || retainedLeases[index] == nil || entries[index].seal != retainedEntries[index].seal {
		return errors.New("fs-verity inventory role unavailable")
	}
	retained := retainedLeases[index]
	retained.mu.Lock()
	if err := retained.stateErrorLocked(); err != nil {
		retained.mu.Unlock()
		return err
	}
	reader := &inventoryRoleReader{file: retained.file, active: true}
	var operationErr error
	func() {
		defer retained.mu.Unlock()
		defer reader.revoke()
		operationErr = operation(ctx, reader, size)
	}()
	if operationErr != nil {
		return operationErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now, err = sampleInventoryClock(clock, lastTime)
	if err != nil || now.Before(notBefore) || !now.Before(notAfter) {
		return errors.Join(errors.New("fs-verity inventory role validity expired"), err)
	}
	return retained.RevalidateAt(ctx, now)
}

// ReadRoleBytes copies a bounded manifest/data role from the exact retained
// object. The returned bytes are evidence only; consumers must still parse and
// cross-bind them through the opaque ArtifactSet graph.
func (lease *InventoryLease) ReadRoleBytes(ctx context.Context, role string, maximum uint64) ([]byte, error) {
	if role == "goose_binary" {
		return nil, errors.New("fs-verity inventory binary byte export denied")
	}
	var result []byte
	err := lease.WithRoleReaderAt(ctx, role, maximum, func(_ context.Context, reader io.ReaderAt, size uint64) error {
		if size > uint64(int(^uint(0)>>1)) {
			return errors.New("fs-verity inventory role size unsupported")
		}
		buffer := make([]byte, int(size))
		if _, err := io.ReadFull(io.NewSectionReader(reader, 0, int64(size)), buffer); err != nil {
			return errors.Join(errors.New("fs-verity inventory role read failed"), err)
		}
		result = buffer
		return nil
	})
	if err != nil {
		for index := range result {
			result[index] = 0
		}
		return nil, err
	}
	return result, nil
}
