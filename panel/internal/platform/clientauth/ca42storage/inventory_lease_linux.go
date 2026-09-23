//go:build linux

package ca42storage

import (
	"context"
	"errors"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

func platformInventoryOps() inventoryOps {
	return inventoryOps{
		identity: linuxSourceIdentity,
		retain: func(ctx context.Context, entry BoundEntry, source *os.File, now time.Time) (*FDLease, error) {
			return retainVerifiedFDWithOps(ctx, entry, source, now, platformLeaseOps())
		},
		close: func(lease *FDLease) error { return lease.Close() },
	}
}

func linuxSourceIdentity(source *os.File) (sourceIdentity, error) {
	if source == nil {
		return sourceIdentity{}, errors.New("fs-verity inventory source invalid")
	}
	defer runtime.KeepAlive(source)
	raw, err := source.SyscallConn()
	if err != nil {
		return sourceIdentity{}, errors.New("fs-verity inventory source access failed")
	}
	var stat unix.Stat_t
	var statErr error
	if err := raw.Control(func(fd uintptr) {
		if fd < 3 {
			statErr = errors.New("fs-verity inventory source descriptor invalid")
			return
		}
		statErr = unix.Fstat(int(fd), &stat)
	}); err != nil || statErr != nil {
		return sourceIdentity{}, errors.New("fs-verity inventory source identity unavailable")
	}
	if stat.Dev == 0 || stat.Ino == 0 {
		return sourceIdentity{}, errors.New("fs-verity inventory source identity invalid")
	}
	return sourceIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}
