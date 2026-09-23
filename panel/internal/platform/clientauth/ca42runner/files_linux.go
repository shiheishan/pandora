//go:build linux

package ca42runner

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type retainedIdentity struct {
	dev, ino, nlink uint64
	mode, uid, gid  uint32
	size            int64
	mtimeSec        int64
	mtimeNsec       int64
	ctimeSec        int64
	ctimeNsec       int64
}

func openFixedTrustedDirectory(absolutePath string) (*os.File, error) {
	if !strings.HasPrefix(absolutePath, "/") || absolutePath == "/" || strings.ContainsAny(absolutePath, "\\\x00") {
		return nil, errors.New("trusted directory path invalid")
	}
	components := strings.Split(strings.TrimPrefix(absolutePath, "/"), "/")
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			unix.Close(current)
			return nil, errors.New("trusted directory component invalid")
		}
		next, openErr := unix.Openat2(current, component, &unix.OpenHow{
			Flags:   uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		unix.Close(current)
		if openErr != nil {
			return nil, openErr
		}
		if validateErr := requireRootOwnedDirectory(next); validateErr != nil {
			unix.Close(next)
			return nil, validateErr
		}
		current = next
	}
	var finalStat unix.Stat_t
	if err := unix.Fstat(current, &finalStat); err != nil || finalStat.Mode&0o7777 != 0o700 {
		unix.Close(current)
		return nil, errors.New("trusted root directory must be root-only mode 0700")
	}
	file := os.NewFile(uintptr(current), absolutePath)
	if file == nil {
		unix.Close(current)
		return nil, errors.New("trusted directory descriptor conversion failed")
	}
	return file, nil
}

func openTrustedChildDirectory(parentFD int, name string) (*os.File, error) {
	if !attemptIDPattern.MatchString(name) {
		return nil, errors.New("trusted child directory name invalid")
	}
	fd, err := unix.Openat2(parentFD, name, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	if err := requireRootOwnedDirectory(fd); err != nil {
		unix.Close(fd)
		return nil, err
	}
	var finalStat unix.Stat_t
	if err := unix.Fstat(fd, &finalStat); err != nil || finalStat.Mode&0o7777 != 0o700 {
		unix.Close(fd)
		return nil, errors.New("trusted attempt directory must be root-only mode 0700")
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("trusted child descriptor conversion failed")
	}
	return file, nil
}

func requireRootOwnedDirectory(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink < 1 || stat.Mode&0o022 != 0 {
		return errors.New("trusted directory ownership or mode denied")
	}
	return nil
}

func readRootOwnedRegularAt(directoryFD int, name string, maxBytes int64, expected *[sha256.Size]byte) ([]byte, [sha256.Size]byte, error) {
	file, data, digest, err := openRootOwnedRegularAt(directoryFD, name, maxBytes, expected)
	if file != nil {
		_ = file.Close()
	}
	return data, digest, err
}

func openRootOwnedRegularAt(directoryFD int, name string, maxBytes int64, expected *[sha256.Size]byte) (*os.File, []byte, [sha256.Size]byte, error) {
	return openRootOwnedArtifactAt(context.Background(), directoryFD, name, maxBytes, 0o400, expected)
}

func openRootOwnedExecutableAt(ctx context.Context, directoryFD int, name string, maxBytes int64, expected *[sha256.Size]byte) (*os.File, []byte, [sha256.Size]byte, error) {
	return openRootOwnedArtifactAt(ctx, directoryFD, name, maxBytes, 0o500, expected)
}

func openRootOwnedArtifactAt(ctx context.Context, directoryFD int, name string, maxBytes int64, requiredMode uint32, expected *[sha256.Size]byte) (*os.File, []byte, [sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if ctx == nil || name == "" || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." || maxBytes <= 0 ||
		(requiredMode != 0o400 && requiredMode != 0o500) {
		return nil, nil, zero, errors.New("trusted file request invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, zero, err
	}
	fd, err := unix.Openat2(directoryFD, name, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, nil, zero, err
	}
	if fd < 3 {
		promoted, promoteErr := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
		_ = unix.Close(fd)
		if promoteErr != nil {
			return nil, nil, zero, promoteErr
		}
		fd = promoted
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, nil, zero, errors.New("trusted file descriptor conversion failed")
	}
	closeFile := true
	defer func() {
		if closeFile {
			_ = file.Close()
		}
	}()
	before, err := retainedStat(fd)
	if err != nil {
		return nil, nil, zero, err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return nil, nil, zero, errors.New("trusted file descriptor must be read-only")
	}
	fdFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		return nil, nil, zero, errors.New("trusted file descriptor must be close-on-exec")
	}
	if before.mode&unix.S_IFMT != unix.S_IFREG || before.uid != 0 || before.gid != 0 || before.nlink != 1 ||
		before.mode&0o7777 != requiredMode || before.size <= 0 || before.size > maxBytes {
		return nil, nil, zero, errors.New("trusted file ownership, mode, link, or size denied")
	}
	data := make([]byte, 0, before.size)
	buffer := make([]byte, 64<<10)
	for int64(len(data)) < before.size {
		if err := ctx.Err(); err != nil {
			return nil, nil, zero, err
		}
		remaining := before.size - int64(len(data))
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		read, readErr := file.Read(chunk)
		if read > 0 {
			data = append(data, chunk[:read]...)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, nil, zero, errors.New("trusted file read denied")
		}
		if read == 0 {
			return nil, nil, zero, errors.New("trusted file read denied")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, zero, err
	}
	if int64(len(data)) != before.size || int64(len(data)) > maxBytes {
		return nil, nil, zero, errors.New("trusted file read denied")
	}
	digest := sha256.Sum256(data)
	if expected != nil && subtle.ConstantTimeCompare(digest[:], expected[:]) != 1 {
		return nil, nil, zero, errors.New("trusted file SHA256 mismatch")
	}
	after, err := retainedStat(fd)
	if err != nil || before != after {
		return nil, nil, zero, errors.New("trusted file identity changed during read")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, zero, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, zero, err
	}
	closeFile = false
	return file, data, digest, nil
}

func runningExecutableSHA256() ([sha256.Size]byte, error) {
	file, digest, err := openRunningExecutable()
	if file != nil {
		_ = file.Close()
	}
	return digest, err
}

func openRunningExecutable() (*os.File, [sha256.Size]byte, error) {
	return openRunningExecutableContext(context.Background())
}

func openRunningExecutableContext(ctx context.Context) (*os.File, [sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if ctx == nil {
		return nil, zero, errors.New("running executable context invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, zero, err
	}
	fd, err := unix.Open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, zero, err
	}
	if fd < 3 {
		promoted, promoteErr := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
		_ = unix.Close(fd)
		if promoteErr != nil {
			return nil, zero, promoteErr
		}
		fd = promoted
	}
	file := os.NewFile(uintptr(fd), "/proc/self/exe")
	if file == nil {
		unix.Close(fd)
		return nil, zero, errors.New("runner descriptor conversion failed")
	}
	closeFile := true
	defer func() {
		if closeFile {
			_ = file.Close()
		}
	}()
	before, err := retainedStat(fd)
	flags, flagsErr := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	fdFlags, fdFlagsErr := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || flagsErr != nil || fdFlagsErr != nil || flags&unix.O_ACCMODE != unix.O_RDONLY || fdFlags&unix.FD_CLOEXEC == 0 ||
		before.mode&unix.S_IFMT != unix.S_IFREG || before.uid != 0 || before.gid != 0 || before.nlink != 1 ||
		before.mode&0o7777 != 0o500 || before.size <= 0 || before.size > maxRootRunnerBytes {
		return nil, zero, errors.New("running executable identity denied")
	}
	hasher := sha256.New()
	if err := hashReaderAt(ctx, hasher, file, before.size); err != nil {
		return nil, zero, err
	}
	after, err := retainedStat(fd)
	if err != nil || before != after {
		return nil, zero, errors.New("running executable changed during hash")
	}
	copy(zero[:], hasher.Sum(nil))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	closeFile = false
	return file, zero, nil
}

func retainedStat(fd int) (retainedIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return retainedIdentity{}, fmt.Errorf("trusted fstat failed: %w", err)
	}
	return retainedIdentity{
		dev: uint64(stat.Dev), ino: stat.Ino, nlink: uint64(stat.Nlink), mode: stat.Mode,
		uid: stat.Uid, gid: stat.Gid, size: stat.Size,
		mtimeSec: stat.Mtim.Sec, mtimeNsec: stat.Mtim.Nsec,
		ctimeSec: stat.Ctim.Sec, ctimeNsec: stat.Ctim.Nsec,
	}, nil
}
