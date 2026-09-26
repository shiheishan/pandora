//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 store_linux.go 的 pathPolicy、openHow、错误模型、系统调用常量与故障注入点，依赖 syscall_linux_{amd64,arm64}.go 的系统调用号，只用标准库 syscall
// [OUTPUT]: 包内提供 writeImmutableAt / openImmutableAt、readStableFD、listDirectoryNames、openTrustedRoot / openAbsoluteDirectory / openJournalDirectory、openAt2、mkdirAt、renameAt2NoReplace、身份比对与 errno 判定等原语
// [POS]: platform/releasejournal 发布日志 v1 命令行（pandora-release-journal）的可信文件系统原语：从 store_linux.go 拆出。逐级 openat2（RESOLVE_BENEATH / NO_SYMLINKS / NO_MAGICLINKS）、设备白名单、不可变文件 root 所有 0600 单链接、读前读后 dev/inode 一致；与 cmd/pandora-cic-journal 的同名原语刻意各持一份，不合并
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package releasejournal

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

func writeImmutableAt(dirFD int, name string, data []byte, expectedDevice uint64) error {
	if !safePathComponent(name) {
		return deny("record_name_invalid")
	}
	fd, err := openAt2(dirFD, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|linuxONoFollow|linuxOCloExec, 0600)
	if err != nil {
		return fmt.Errorf("create immutable record: %w", err)
	}
	defer syscall.Close(fd)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return err
	}
	if err := validateImmutableFile(stat, expectedDevice, nil); err != nil {
		return err
	}
	if err := writeFull(fd, data); err != nil {
		return fmt.Errorf("write immutable record: %w", err)
	}
	if err := releaseFdatasync(fd); err != nil {
		return fmt.Errorf("fdatasync immutable record: %w", err)
	}
	if err := releaseFsync(fd); err != nil {
		return fmt.Errorf("fsync immutable record: %w", err)
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil {
		return err
	}
	if !sameFileIdentity(stat, after) || after.Size != int64(len(data)) {
		return deny("immutable_record_changed")
	}
	return nil
}

func openImmutableAt(dirFD int, name string, expectedDevice uint64, allowed map[uint64]struct{}) (int, syscall.Stat_t, error) {
	fd, err := openAt2(dirFD, name, syscall.O_RDONLY|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, denyErr("record_open_failed", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		syscall.Close(fd)
		return -1, stat, err
	}
	if err := validateImmutableFile(stat, expectedDevice, allowed); err != nil {
		syscall.Close(fd)
		return -1, stat, err
	}
	return fd, stat, nil
}

func validateImmutableFile(stat syscall.Stat_t, expectedDevice uint64, allowed map[uint64]struct{}) error {
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return deny("record_not_regular")
	}
	if stat.Uid != 0 || stat.Mode&07777 != 0600 || stat.Nlink != 1 {
		return deny("record_identity_or_mode_invalid")
	}
	if expectedDevice != ^uint64(0) && uint64(stat.Dev) != expectedDevice {
		return deny("record_device_mismatch")
	}
	if allowed != nil {
		if _, ok := allowed[uint64(stat.Dev)]; !ok {
			return deny("record_device_not_allowed")
		}
	}
	return nil
}

func readStableFD(fd int) ([]byte, syscall.Stat_t, error) {
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return nil, before, err
	}
	if before.Size <= 0 || before.Size > maxRecordBytes {
		return nil, before, deny("record_size_invalid")
	}
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, before, err
	}
	file := os.NewFile(uintptr(duplicate), "release-record")
	if file == nil {
		syscall.Close(duplicate)
		return nil, before, errors.New("wrap record fd")
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, before, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return nil, before, err
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil {
		return nil, after, err
	}
	if !sameFileSnapshot(before, after) || int64(len(data)) != after.Size {
		return nil, after, deny("record_changed_or_short_read")
	}
	return data, after, nil
}

func listDirectoryNames(fd int) ([]string, error) {
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicate), "release-directory")
	if file == nil {
		syscall.Close(duplicate)
		return nil, errors.New("wrap directory fd")
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	Names := make([]string, 0, len(entries))
	for _, entry := range entries {
		Names = append(Names, entry.Name())
	}
	sort.Strings(Names)
	return Names, nil
}

func openTrustedRoot(policy pathPolicy) (int, syscall.Stat_t, error) {
	fd, stat, err := openAbsoluteDirectory(policy.root, policy.allowedDevices)
	if err != nil {
		return -1, stat, err
	}
	if uint64(stat.Dev) != policy.expectedDevice {
		syscall.Close(fd)
		return -1, stat, deny("journal_root_device_mismatch")
	}
	return fd, stat, nil
}

func openAbsoluteDirectory(path string, allowed map[uint64]struct{}) (int, syscall.Stat_t, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.Contains(path, "//") {
		return -1, syscall.Stat_t{}, deny("journal_root_not_absolute_canonical")
	}
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(components) == 1 && components[0] == "" {
		components = nil
	}
	current, err := syscall.Open("/", syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, err
	}
	stat, err := validateDirectoryFD(current, allowed)
	if err != nil {
		syscall.Close(current)
		return -1, stat, err
	}
	for _, component := range components {
		if !safePathComponent(component) {
			syscall.Close(current)
			return -1, stat, deny("journal_root_component_invalid")
		}
		next, openErr := openAt2(current, component, syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
		syscall.Close(current)
		if openErr != nil {
			return -1, stat, denyErr("journal_root_component_open_failed", openErr)
		}
		current = next
		stat, err = validateDirectoryFD(current, allowed)
		if err != nil {
			syscall.Close(current)
			return -1, stat, err
		}
	}
	return current, stat, nil
}

func openJournalDirectory(parentFD int, name string, expectedDevice uint64, allowed map[uint64]struct{}) (int, syscall.Stat_t, error) {
	if !safePathComponent(name) {
		return -1, syscall.Stat_t{}, deny("journal_directory_name_invalid")
	}
	// This lookup is exactly one validated direct child. O_NOFOLLOW plus the
	// retained parent FD and the identity/ownership/mode/device checks below
	// provide the required binding without openat2's false ENOENT observed for
	// immediately renamed hidden directories on supported kernels.
	fd, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, denyErr("journal_directory_open_failed", err)
	}
	stat, err := validateDirectoryFD(fd, allowed)
	if err != nil {
		syscall.Close(fd)
		return -1, stat, err
	}
	if stat.Mode&07777 != 0700 {
		syscall.Close(fd)
		return -1, stat, deny("journal_directory_mode_not_0700")
	}
	if uint64(stat.Dev) != expectedDevice {
		syscall.Close(fd)
		return -1, stat, deny("journal_directory_device_mismatch")
	}
	return fd, stat, nil
}

func validateDirectoryFD(fd int, allowed map[uint64]struct{}) (syscall.Stat_t, error) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return stat, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Uid != 0 || stat.Mode&0022 != 0 {
		return stat, deny("directory_trust_invalid")
	}
	if _, ok := allowed[uint64(stat.Dev)]; !ok {
		return stat, deny("directory_device_not_allowed")
	}
	return stat, nil
}

func validateDirectoryIdentity(fd int, expected syscall.Stat_t, allowed map[uint64]struct{}) error {
	actual, err := validateDirectoryFD(fd, allowed)
	if err != nil {
		return err
	}
	if actual.Dev != expected.Dev || actual.Ino != expected.Ino || actual.Uid != expected.Uid || actual.Mode != expected.Mode {
		return deny("directory_identity_changed")
	}
	return nil
}

func safePathComponent(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.Contains(name, "/") {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func openAt2(parentFD int, name string, flags int, mode uint32) (int, error) {
	if !safePathComponent(name) {
		return -1, deny("openat2_component_invalid")
	}
	pointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return -1, err
	}
	how := openHow{Flags: uint64(flags), Mode: uint64(mode), Resolve: resolveBeneath | resolveNoSymlinks | resolveNoMagicLinks}
	fd, _, errno := syscall.Syscall6(linuxSYSOpenat2, uintptr(parentFD), uintptr(unsafe.Pointer(pointer)), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func mkdirAt(parentFD int, name string, mode uint32) error {
	if !safePathComponent(name) {
		return deny("mkdir_component_invalid")
	}
	pointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_MKDIRAT, uintptr(parentFD), uintptr(unsafe.Pointer(pointer)), uintptr(mode))
	if errno != 0 {
		return errno
	}
	return nil
}

func renameAt2NoReplace(oldDir int, oldName string, newDir int, newName string) error {
	if !safePathComponent(oldName) || !safePathComponent(newName) {
		return deny("rename_component_invalid")
	}
	oldPointer, err := syscall.BytePtrFromString(oldName)
	if err != nil {
		return err
	}
	newPointer, err := syscall.BytePtrFromString(newName)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(linuxSYSRenameat2, uintptr(oldDir), uintptr(unsafe.Pointer(oldPointer)), uintptr(newDir), uintptr(unsafe.Pointer(newPointer)), renameNoReplace, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func randomHex(byteCount int) (string, error) {
	data := make([]byte, byteCount)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", data), nil
}

func writeFull(fd int, data []byte) error {
	for len(data) > 0 {
		count, err := releaseWrite(fd, data)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if count <= 0 {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}

func sameFileIdentity(a, b syscall.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Uid == b.Uid && a.Gid == b.Gid && a.Mode == b.Mode && a.Nlink == b.Nlink
}

func sameFileSnapshot(a, b syscall.Stat_t) bool {
	return sameFileIdentity(a, b) && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func releaseErrnoIs(err error, target syscall.Errno) bool {
	for err != nil {
		if errno, ok := err.(syscall.Errno); ok {
			return errno == target
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
