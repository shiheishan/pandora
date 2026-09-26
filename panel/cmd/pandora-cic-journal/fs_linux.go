//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 main_linux.go 的 pathOptions、openHow 与 policyError，依赖 syscall_linux_{amd64,arm64}.go 的系统调用号，只用标准库 syscall
// [OUTPUT]: 包内提供 openTrustedRoot、openAbsoluteDirectory、openTrustedDirectoryAt、openAt2、renameAt2NoReplace、目录与日志文件的身份校验、writeFull、readJournalFD
// [POS]: pandora-cic-journal 的可信文件系统原语：从 main_linux.go 拆出。逐级 openat2（RESOLVE_BENEATH / NO_SYMLINKS / NO_MAGICLINKS）打开目录并核对设备白名单；日志文件必须 root 所有、0600、单链接，读前读后 dev/inode 一致
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

func openTrustedRoot(options pathOptions) (int, syscall.Stat_t, error) {
	fd, stat, err := openAbsoluteDirectory(options.journalRoot, options.allowedDevices)
	if err != nil {
		return -1, syscall.Stat_t{}, err
	}
	if uint64(stat.Dev) != options.expectedRootDev {
		syscall.Close(fd)
		return -1, syscall.Stat_t{}, deny("journal_root_device_mismatch")
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
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return -1, syscall.Stat_t{}, deny("journal_root_component_invalid")
		}
	}
	current, err := syscall.Open("/", syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, fmt.Errorf("open filesystem root: %w", err)
	}
	stat, err := validateDirectoryFD(current, allowed)
	if err != nil {
		syscall.Close(current)
		return -1, syscall.Stat_t{}, err
	}
	for _, component := range components {
		next, err := openAt2(current, component,
			syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
		if err != nil {
			syscall.Close(current)
			return -1, syscall.Stat_t{}, &policyError{reason: "journal_root_component_open_failed", err: err}
		}
		syscall.Close(current)
		current = next
		stat, err = validateDirectoryFD(current, allowed)
		if err != nil {
			syscall.Close(current)
			return -1, syscall.Stat_t{}, err
		}
	}
	return current, stat, nil
}

func openTrustedDirectoryAt(parentFD int, name string, allowed map[uint64]struct{}) (int, syscall.Stat_t, error) {
	if !safePathComponent(name) {
		return -1, syscall.Stat_t{}, deny("directory_name_invalid")
	}
	fd, err := openAt2(parentFD, name,
		syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, &policyError{reason: "run_directory_open_failed", err: err}
	}
	stat, err := validateDirectoryFD(fd, allowed)
	if err != nil {
		syscall.Close(fd)
		return -1, syscall.Stat_t{}, err
	}
	return fd, stat, nil
}

func safePathComponent(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.Contains(name, "/") {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func openAt2(parentFD int, name string, flags int, mode uint32) (int, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return -1, deny("openat2_component_invalid")
	}
	namePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return -1, err
	}
	how := openHow{
		Flags:   uint64(flags),
		Mode:    uint64(mode),
		Resolve: resolveBeneath | resolveNoSymlinks | resolveNoMagicLinks,
	}
	fd, _, errno := syscall.Syscall6(
		linuxSYSOpenat2,
		uintptr(parentFD),
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(&how)),
		unsafe.Sizeof(how),
		0,
		0,
	)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func renameAt2NoReplace(oldDir int, oldName string, newDir int, newName string) error {
	oldPtr, err := syscall.BytePtrFromString(oldName)
	if err != nil {
		return err
	}
	newPtr, err := syscall.BytePtrFromString(newName)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(
		linuxSYSRenameat2,
		uintptr(oldDir),
		uintptr(unsafe.Pointer(oldPtr)),
		uintptr(newDir),
		uintptr(unsafe.Pointer(newPtr)),
		renameNoReplace,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func validateDirectoryFD(fd int, allowed map[uint64]struct{}) (syscall.Stat_t, error) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return stat, fmt.Errorf("fstat trusted directory: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return stat, deny("ancestor_not_directory")
	}
	if stat.Uid != 0 {
		return stat, deny("ancestor_not_root_owned")
	}
	if stat.Mode&0022 != 0 {
		return stat, deny("ancestor_group_or_world_writable")
	}
	if _, ok := allowed[uint64(stat.Dev)]; !ok {
		return stat, deny("ancestor_device_not_allowed")
	}
	return stat, nil
}

func validateDirectoryIdentity(fd int, expected syscall.Stat_t, allowed map[uint64]struct{}) error {
	actual, err := validateDirectoryFD(fd, allowed)
	if err != nil {
		return err
	}
	if actual.Dev != expected.Dev || actual.Ino != expected.Ino || actual.Uid != expected.Uid ||
		actual.Mode != expected.Mode {
		return deny("trusted_directory_identity_changed")
	}
	return nil
}

func validateJournalStat(stat syscall.Stat_t, directoryDev uint64) error {
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return deny("journal_not_regular")
	}
	if stat.Uid != 0 {
		return deny("journal_not_root_owned")
	}
	if stat.Mode&07777 != 0600 {
		return deny("journal_mode_not_0600")
	}
	if stat.Nlink != 1 {
		return deny("journal_link_count_not_one")
	}
	if uint64(stat.Dev) != directoryDev {
		return deny("journal_directory_device_mismatch")
	}
	return nil
}

func sameFileIdentity(a, b syscall.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Uid == b.Uid &&
		a.Gid == b.Gid && a.Mode == b.Mode && a.Nlink == b.Nlink
}

func sameFileSnapshot(a, b syscall.Stat_t) bool {
	return sameFileIdentity(a, b) && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func writeFull(fd int, data []byte) error {
	for len(data) > 0 {
		count, err := journalWrite(fd, data)
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

func readJournalFD(fd int) ([]byte, syscall.Stat_t, error) {
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return nil, before, fmt.Errorf("fstat before journal read: %w", err)
	}
	if before.Size <= 0 || before.Size > maxJournalBytes {
		return nil, before, deny("journal_size_invalid")
	}
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, before, fmt.Errorf("dup journal fd: %w", err)
	}
	file := os.NewFile(uintptr(duplicate), "pandora-cic-journal-read")
	if file == nil {
		syscall.Close(duplicate)
		return nil, before, errors.New("cannot wrap duplicated journal fd")
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, before, fmt.Errorf("seek journal: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	if err != nil {
		return nil, before, fmt.Errorf("read journal: %w", err)
	}
	if len(data) > maxJournalBytes {
		return nil, before, deny("journal_too_large")
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil {
		return nil, after, fmt.Errorf("fstat after journal read: %w", err)
	}
	if !sameFileSnapshot(before, after) || int64(len(data)) != after.Size {
		return nil, after, deny("journal_changed_or_short_read")
	}
	return data, after, nil
}
