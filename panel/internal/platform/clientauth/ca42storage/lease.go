package ca42storage

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"
)

var (
	ErrFSVerityUnsupported = errors.New("fs-verity retained FD lease requires native Linux")
	errLeaseInvalid        = errors.New("fs-verity retained FD lease invalid")
	errLeaseClosed         = errors.New("fs-verity retained FD lease closed")
	errLeasePoisoned       = errors.New("fs-verity retained FD lease poisoned")
)

type leaseOps struct {
	duplicate func(*os.File) (*os.File, error)
	verify    func(context.Context, BoundEntry, *os.File, time.Time) error
}

type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// FDLease owns a verified duplicate of one descriptor-bound file descriptor.
// It never exposes or returns an FD. A future execution handoff must be a
// separate capability that rechecks the complete ArtifactSet effective window
// and performs the final verification at the actual use boundary.
type FDLease struct {
	noCopy   noCopy
	mu       sync.Mutex
	file     *os.File
	entry    BoundEntry
	ops      leaseOps
	poisoned bool
	closed   bool
}

// RetainVerifiedFD duplicates source and verifies the duplicate. Ownership of
// source remains with the caller in every success and failure path.
func RetainVerifiedFD(ctx context.Context, entry BoundEntry, source *os.File, now time.Time) (*FDLease, error) {
	if !leasePlatformSupported() {
		return nil, ErrFSVerityUnsupported
	}
	return retainVerifiedFDWithOps(ctx, entry, source, now, platformLeaseOps())
}

func retainVerifiedFDWithOps(ctx context.Context, entry BoundEntry, source *os.File, now time.Time, ops leaseOps) (*FDLease, error) {
	if ctx == nil || source == nil || ops.duplicate == nil || ops.verify == nil {
		return nil, errLeaseInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := entry.verifiedEntryAt(now); err != nil {
		return nil, errLeaseInvalid
	}
	duplicate, err := ops.duplicate(source)
	runtime.KeepAlive(source)
	if err != nil {
		return nil, err
	}
	if duplicate == nil || int(duplicate.Fd()) < 3 {
		if duplicate != nil {
			_ = duplicate.Close()
		}
		return nil, errLeaseInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, duplicate.Close())
	}
	if err := ops.verify(ctx, entry, duplicate, now); err != nil {
		closeErr := duplicate.Close()
		return nil, errors.Join(err, closeErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, duplicate.Close())
	}
	return &FDLease{file: duplicate, entry: entry, ops: leaseOps{verify: ops.verify}}, nil
}

// RevalidateAt verifies the exact retained FD. Any failure permanently poisons
// and closes the lease so a caller cannot continue after ambiguous integrity.
func (lease *FDLease) RevalidateAt(ctx context.Context, now time.Time) error {
	if lease == nil || ctx == nil || now.IsZero() {
		return errLeaseInvalid
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.stateErrorLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, lease.poisonLocked())
	}
	if _, err := lease.entry.verifiedEntryAt(now); err != nil {
		return errors.Join(err, lease.poisonLocked())
	}
	if err := lease.ops.verify(ctx, lease.entry, lease.file, now); err != nil {
		return errors.Join(err, lease.poisonLocked())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, lease.poisonLocked())
	}
	return nil
}

// Close is nil-safe and idempotent. It closes only the descriptor owned by the
// lease; caller-owned source is never affected.
func (lease *FDLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return nil
	}
	lease.closed = true
	var err error
	if lease.file != nil {
		err = lease.file.Close()
	}
	lease.file = nil
	lease.entry = BoundEntry{}
	lease.ops = leaseOps{}
	return err
}

func (lease *FDLease) stateErrorLocked() error {
	if lease.closed {
		return errLeaseClosed
	}
	if lease.poisoned {
		return errLeasePoisoned
	}
	if lease.file == nil || int(lease.file.Fd()) < 3 || lease.ops.verify == nil {
		return errLeaseInvalid
	}
	return nil
}

func (lease *FDLease) poisonLocked() error {
	lease.poisoned = true
	var err error
	if lease.file != nil {
		err = lease.file.Close()
	}
	lease.file = nil
	lease.entry = BoundEntry{}
	return err
}
