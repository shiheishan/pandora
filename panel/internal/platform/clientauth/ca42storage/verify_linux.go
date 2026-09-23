//go:build linux

package ca42storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type verityMeasureBuffer struct {
	unix.FsverityDigest
	Digest [sha256.Size]byte
}

// VerifyFD rechecks one already-retained descriptor against the actual file
// and the kernel fs-verity measurement. It never reopens by pathname.
func VerifyFD(ctx context.Context, boundEntry BoundEntry, file *os.File, now time.Time) error {
	entry, err := boundEntry.verifiedEntryAt(now)
	if err != nil {
		return errors.New("fs-verity artifact entry is not plan-bound")
	}
	if ctx == nil || file == nil || int(file.Fd()) < 3 || entry.Size == 0 || !nonZeroHex64(entry.ContentSHA256) || !nonZeroHex64(entry.VeritySHA256) {
		return errors.New("fs-verity retained file verification input invalid")
	}
	defer runtime.KeepAlive(file)
	if err := ctx.Err(); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return errors.New("fs-verity retained file is not read-only")
	}
	fdFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		return errors.New("fs-verity retained file is not close-on-exec")
	}
	before, beforeMount, err := fdIdentity(file)
	if err != nil || !entryMatchesStat(entry, before, beforeMount) {
		return errors.New("fs-verity retained file metadata mismatch")
	}
	content, err := hashFD(ctx, file, entry.Size)
	if err != nil {
		return err
	}
	expectedContent, _ := hex.DecodeString(entry.ContentSHA256)
	if !hmac.Equal(content[:], expectedContent) {
		return errors.New("fs-verity retained file content mismatch")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	measurement, err := MeasureVerity(file)
	if err != nil {
		return err
	}
	expectedVerity, _ := hex.DecodeString(entry.VeritySHA256)
	if !hmac.Equal(measurement[:], expectedVerity) {
		return errors.New("fs-verity retained file measurement mismatch")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	after, afterMount, err := fdIdentity(file)
	if err != nil || !sameSecurityIdentity(before, after) || beforeMount != afterMount || !entryMatchesStat(entry, after, afterMount) {
		return errors.New("fs-verity retained file changed during verification")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func sameSecurityIdentity(before, after unix.Stat_t) bool {
	return before.Dev == after.Dev && before.Ino == after.Ino && before.Mode == after.Mode &&
		before.Uid == after.Uid && before.Gid == after.Gid && before.Nlink == after.Nlink &&
		before.Size == after.Size
}

// MeasureVerity invokes FS_IOC_MEASURE_VERITY on the exact retained FD.
func MeasureVerity(file *os.File) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	if file == nil || int(file.Fd()) < 3 {
		return empty, errors.New("fs-verity measurement file invalid")
	}
	buffer := verityMeasureBuffer{}
	buffer.Algorithm = unix.FS_VERITY_HASH_ALG_SHA256
	buffer.Size = sha256.Size
	var errno syscall.Errno
	for attempt := 0; attempt < 3; attempt++ {
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, file.Fd(), uintptr(unix.FS_IOC_MEASURE_VERITY), uintptr(unsafe.Pointer(&buffer)))
		if errno != unix.EINTR {
			break
		}
	}
	runtime.KeepAlive(file)
	if errno != 0 {
		return empty, errors.New("fs-verity kernel measurement unavailable")
	}
	if buffer.Algorithm != unix.FS_VERITY_HASH_ALG_SHA256 || buffer.Size != sha256.Size || buffer.Digest == empty {
		return empty, errors.New("fs-verity kernel measurement invalid")
	}
	return buffer.Digest, nil
}

func hashFD(ctx context.Context, file *os.File, size uint64) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	hasher := sha256.New()
	buffer := make([]byte, 1<<20)
	var offset int64
	for uint64(offset) < size {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		want := uint64(len(buffer))
		if remaining := size - uint64(offset); remaining < want {
			want = remaining
		}
		count, err := file.ReadAt(buffer[:want], offset)
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
			offset += int64(count)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return empty, errors.New("fs-verity retained file read failed")
		}
		if count == 0 {
			return empty, errors.New("fs-verity retained file truncated")
		}
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func fdIdentity(file *os.File) (unix.Stat_t, uint64, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return stat, 0, err
	}
	var statx unix.Statx_t
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_STATX_DONT_SYNC, unix.STATX_MNT_ID, &statx); err != nil || statx.Mask&unix.STATX_MNT_ID == 0 {
		return stat, 0, errors.New("fs-verity retained file mount identity unavailable")
	}
	return stat, statx.Mnt_id, nil
}

func entryMatchesStat(entry Entry, stat unix.Stat_t, mountID uint64) bool {
	mode, err := strconv.ParseUint(entry.Mode, 8, 32)
	return err == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG && uint64(stat.Mode&0o7777) == mode &&
		uint64(stat.Uid) == entry.UID && uint64(stat.Gid) == entry.GID && uint64(stat.Nlink) == entry.NLink &&
		stat.Size > 0 && uint64(stat.Size) == entry.Size && uint64(stat.Dev) == entry.Device &&
		stat.Ino == entry.Inode && mountID == entry.MountID
}
