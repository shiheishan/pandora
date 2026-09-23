//go:build linux

package ca42storage

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func leasePlatformSupported() bool { return true }

func platformLeaseOps() leaseOps {
	return leaseOps{duplicate: duplicateCloexec, verify: VerifyFD}
}

func duplicateCloexec(source *os.File) (*os.File, error) {
	if source == nil {
		return nil, errLeaseInvalid
	}
	defer runtime.KeepAlive(source)
	raw, err := source.SyscallConn()
	if err != nil {
		return nil, errors.New("fs-verity retained FD access failed")
	}
	fd := -1
	var duplicateErr error
	if err := raw.Control(func(sourceFD uintptr) {
		if sourceFD < 3 {
			duplicateErr = errLeaseInvalid
			return
		}
		fd, duplicateErr = unix.FcntlInt(sourceFD, unix.F_DUPFD_CLOEXEC, 3)
	}); err != nil {
		return nil, fmt.Errorf("fs-verity retained FD access failed: %w", err)
	}
	if duplicateErr != nil || fd < 3 {
		if duplicateErr != nil {
			return nil, fmt.Errorf("fs-verity retained FD duplication failed: %w", duplicateErr)
		}
		return nil, errors.New("fs-verity retained FD duplication failed")
	}
	duplicate := os.NewFile(uintptr(fd), "pandora-ca42-fsverity-lease")
	if duplicate == nil {
		_ = unix.Close(fd)
		return nil, errors.New("fs-verity retained FD adoption failed")
	}
	return duplicate, nil
}
