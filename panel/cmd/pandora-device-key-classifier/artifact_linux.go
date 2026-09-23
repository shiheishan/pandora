//go:build linux

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func readRootOwnedSecretFD(sourceFD int) ([]byte, error) {
	if os.Geteuid() != 0 || sourceFD < 3 {
		return nil, errors.New("root_and_inherited_fd_required")
	}
	fd, err := unix.Dup(sourceFD)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if stat.Uid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&0o077 != 0 || stat.Nlink != 1 || stat.Size != 32 {
		unix.Close(fd)
		return nil, errors.New("secret_fd_not_root_owned_private_regular")
	}
	openFlags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || openFlags&unix.O_ACCMODE != unix.O_RDONLY {
		unix.Close(fd)
		return nil, errors.New("secret_fd_not_read_only")
	}
	defer unix.Close(fd)
	value := make([]byte, 32)
	n, err := unix.Pread(fd, value, 0)
	if err != nil || n != len(value) {
		return nil, errors.New("invalid_secret_length")
	}
	return value, nil
}

func writeRootOwnedArtifact(sourceDirFD int, baseName string, content []byte) error {
	if os.Geteuid() != 0 || sourceDirFD < 3 {
		return errors.New("root_and_inherited_directory_fd_required")
	}
	if baseName == "" || baseName == "." || baseName == ".." ||
		strings.ContainsAny(baseName, `/\`) {
		return errors.New("invalid_artifact_name")
	}
	dirFD, err := unix.Dup(sourceDirFD)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	var dirStat unix.Stat_t
	if err := unix.Fstat(dirFD, &dirStat); err != nil {
		return err
	}
	if dirStat.Uid != 0 || dirStat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		dirStat.Mode&0o077 != 0 {
		return errors.New("artifact_directory_not_root_owned_private")
	}
	dirFlags, err := unix.FcntlInt(uintptr(dirFD), unix.F_GETFL, 0)
	if err != nil || dirFlags&unix.O_ACCMODE != unix.O_RDONLY ||
		dirFlags&unix.O_DIRECTORY == 0 || dirFlags&unix.O_PATH != 0 {
		return errors.New("artifact_directory_fd_not_syncable_read_only_directory")
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tempName := "." + baseName + ".tmp-" + hex.EncodeToString(nonce[:])
	tempFD, err := unix.Openat(
		dirFD,
		tempName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return err
	}
	tempExists := true
	defer func() {
		if tempExists {
			_ = unix.Unlinkat(dirFD, tempName, 0)
		}
	}()

	file := os.NewFile(uintptr(tempFD), tempName)
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unix.Renameat2(dirFD, tempName, dirFD, baseName, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	tempExists = false
	return unix.Fsync(dirFD)
}
