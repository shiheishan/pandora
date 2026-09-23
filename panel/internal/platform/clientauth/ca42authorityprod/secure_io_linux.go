//go:build linux

package ca42authorityprod

import (
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
		return nil, errors.New("authority ledger root path invalid")
	}
	components := strings.Split(strings.TrimPrefix(absolutePath, "/"), "/")
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			_ = unix.Close(current)
			return nil, errors.New("authority ledger root component invalid")
		}
		next, openErr := unix.Openat2(current, component, &unix.OpenHow{
			Flags:   uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		_ = unix.Close(current)
		if openErr != nil {
			return nil, openErr
		}
		if validateErr := requireRootOwnedDirectory(next); validateErr != nil {
			_ = unix.Close(next)
			return nil, validateErr
		}
		current = next
	}
	var finalStat unix.Stat_t
	if err := unix.Fstat(current, &finalStat); err != nil || finalStat.Mode&0o7777 != 0o700 {
		_ = unix.Close(current)
		return nil, errors.Join(errors.New("authority ledger root must be root-only mode 0700"), err)
	}
	file := os.NewFile(uintptr(current), absolutePath)
	if file == nil {
		_ = unix.Close(current)
		return nil, errors.New("authority ledger root descriptor conversion failed")
	}
	return file, nil
}

func requireRootOwnedDirectory(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink < 1 || stat.Mode&0o022 != 0 {
		return errors.New("authority ledger directory ownership or mode denied")
	}
	return nil
}

func readRootOwnedRegularAt(directoryFD int, name string, maxBytes int64, expected *[sha256.Size]byte) ([]byte, [sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if name == "" || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." || maxBytes <= 0 {
		return nil, zero, errors.New("authority ledger record request invalid")
	}
	fd, err := unix.Openat2(directoryFD, name, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
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
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, zero, errors.New("authority ledger record descriptor conversion failed")
	}
	defer file.Close()
	before, err := retainedStat(fd)
	if err != nil {
		return nil, zero, err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return nil, zero, errors.New("authority ledger record descriptor must be read-only")
	}
	fdFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		return nil, zero, errors.New("authority ledger record descriptor must be close-on-exec")
	}
	if before.mode&unix.S_IFMT != unix.S_IFREG || before.uid != 0 || before.gid != 0 || before.nlink != 1 ||
		before.mode&0o7777 != 0o400 || before.size <= 0 || before.size > maxBytes {
		return nil, zero, errors.New("authority ledger record ownership, mode, link, or size denied")
	}
	data := make([]byte, before.size)
	if _, err := io.ReadFull(file, data); err != nil {
		return nil, zero, errors.New("authority ledger record read denied")
	}
	var extra [1]byte
	if read, readErr := file.Read(extra[:]); read != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return nil, zero, errors.New("authority ledger record length changed during read")
	}
	digest := sha256.Sum256(data)
	if expected != nil && subtle.ConstantTimeCompare(digest[:], expected[:]) != 1 {
		return nil, zero, errors.New("authority ledger record SHA256 mismatch")
	}
	after, err := retainedStat(fd)
	if err != nil || before != after {
		return nil, zero, errors.Join(errors.New("authority ledger record identity changed during read"), err)
	}
	return data, digest, nil
}

func retainedStat(fd int) (retainedIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return retainedIdentity{}, fmt.Errorf("authority ledger fstat failed: %w", err)
	}
	return retainedIdentity{
		dev: uint64(stat.Dev), ino: stat.Ino, nlink: uint64(stat.Nlink), mode: stat.Mode,
		uid: stat.Uid, gid: stat.Gid, size: stat.Size,
		mtimeSec: stat.Mtim.Sec, mtimeNsec: stat.Mtim.Nsec,
		ctimeSec: stat.Ctim.Sec, ctimeNsec: stat.Ctim.Nsec,
	}, nil
}
