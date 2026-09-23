//go:build ca42e2e && linux && (amd64 || arm64)

package ca42storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	ca42E2EHostPIDNamespaceFD   = 8
	ca42E2EHostMountNamespaceFD = 9
)

// CA42E2ERuntimeFile describes one already-provisioned runtime-closure file.
// The first four entries must be bash, docker, the architecture loader, and at
// least one library in the canonical order enforced by Parse.
type CA42E2ERuntimeFile struct {
	Role string
	Path string
	Mode uint32
}

// CA42E2ERequiredFile is a value-only projection of the production inventory
// contract. It lets the tagged producer create exact paths without duplicating
// the private role and migration inventories.
type CA42E2ERequiredFile struct {
	Scope string
	Role  string
	Kind  string
	Path  string
	Mode  uint32
}

// CA42E2EStorageInput identifies a complete, already-published E2E inventory.
// It is available only in the disposable ca42e2e build and never writes paths.
type CA42E2EStorageInput struct {
	ReleaseID    string
	ReleaseRunID string
	AttemptID    string
	Architecture string
	RuntimeFiles []CA42E2ERuntimeFile
}

// CA42E2EStorageResult carries the sealed descriptor and explicit media state.
// Tainted means phase two started but did not finish; the caller must destroy
// the complete disposable ext4 image and must never retry or repair it.
// SealAttempts is incremented before each irreversible ioctl; the verified
// count advances only after measurement, post-seal hashing, and identity checks.
type CA42E2EStorageResult struct {
	Raw                 []byte
	Descriptor          Descriptor
	Tainted             bool
	SealAttempts        int
	VerifiedSealedCount int
}

type ca42E2EFile struct {
	scope string
	role  string
	kind  string
	path  string
	mode  uint32
}

// CA42E2ERequiredInventoryFiles returns the exact ordered inventory contract.
// It performs no filesystem access and conveys no verification authority.
func CA42E2ERequiredInventoryFiles(attemptID, architecture string, runtimeFiles []CA42E2ERuntimeFile) ([]CA42E2ERequiredFile, error) {
	if !safeToken.MatchString(attemptID) || architecture != runtime.GOARCH ||
		len(runtimeFiles) < MinRuntimeEntryCount || len(runtimeFiles) > MaxRuntimeEntryCount {
		return nil, errors.New("CA42 E2E inventory layout identity invalid")
	}
	root := "/run/pandora/ca42/" + attemptID
	files := make([]CA42E2ERequiredFile, 0, AttemptEntryCount+MigrationEntryCount+len(runtimeFiles))
	for _, expected := range attemptRoles {
		files = append(files, CA42E2ERequiredFile{Scope: "attempt", Role: expected.role, Kind: expected.kind,
			Path: root + "/" + expected.name, Mode: parseCA42E2EMode(expected.mode)})
	}
	for _, name := range migrationNames {
		files = append(files, CA42E2ERequiredFile{Scope: "migration", Role: "migration_sql", Kind: "sql",
			Path: root + "/migrations/" + name, Mode: 0o400})
	}
	for _, entry := range runtimeFiles {
		files = append(files, CA42E2ERequiredFile{Scope: "system", Role: entry.Role, Kind: "elf", Path: entry.Path, Mode: entry.Mode})
	}
	internal := make([]ca42E2EFile, len(files))
	for index, file := range files {
		internal[index] = ca42E2EFile{scope: file.Scope, role: file.Role, kind: file.Kind, path: file.Path, mode: file.Mode}
	}
	if err := validateCA42E2EFileLayout(internal, root, len(runtimeFiles), architecture); err != nil {
		return nil, err
	}
	return files, nil
}

type ca42E2ERetainedFile struct {
	spec    ca42E2EFile
	file    *os.File
	stat    unix.Stat_t
	mountID uint64
	content [sha256.Size]byte
}

// BuildCA42E2EStorageDescriptor seals every already-published inventory file
// with fs-verity, measures its retained descriptor, and emits the exact
// production descriptor format. It cannot create, replace, chmod, or chown a
// file and therefore cannot silently repair an invalid fixture.
func BuildCA42E2EStorageDescriptor(ctx context.Context, input CA42E2EStorageInput) (CA42E2EStorageResult, error) {
	var result CA42E2EStorageResult
	if err := requireCA42E2EStorageIsolation(); err != nil {
		return result, err
	}
	if ctx == nil || input.Architecture != runtime.GOARCH || !safeToken.MatchString(input.ReleaseID) ||
		!safeToken.MatchString(input.ReleaseRunID) || !safeToken.MatchString(input.AttemptID) {
		return result, errors.New("CA42 E2E storage identity invalid")
	}
	root := "/run/pandora/ca42/" + input.AttemptID
	required, err := CA42E2ERequiredInventoryFiles(input.AttemptID, input.Architecture, input.RuntimeFiles)
	if err != nil {
		return result, err
	}
	files := make([]ca42E2EFile, len(required))
	for index, file := range required {
		files[index] = ca42E2EFile{scope: file.Scope, role: file.Role, kind: file.Kind, path: file.Path, mode: file.Mode}
	}
	fixtureDevice, hostRootDevice, err := ca42E2EFixtureDevices()
	if err != nil {
		return result, err
	}
	retained := make([]ca42E2ERetainedFile, 0, len(files))
	defer func() {
		for index := range retained {
			_ = retained[index].file.Close()
		}
	}()
	seen := make(map[string]struct{}, len(files))
	for index, spec := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		opened, err := preflightCA42E2EFile(ctx, spec, fixtureDevice, hostRootDevice)
		if err != nil {
			return result, fmt.Errorf("CA42 E2E storage preflight entry %d: %w", index+1, err)
		}
		identity := fmt.Sprintf("%d:%d", opened.stat.Dev, opened.stat.Ino)
		if _, duplicate := seen[identity]; duplicate {
			_ = opened.file.Close()
			return result, errors.New("CA42 E2E storage preflight duplicate file identity")
		}
		seen[identity] = struct{}{}
		retained = append(retained, opened)
	}

	lines := make([]string, 0, len(retained))
	result.Tainted = true
	for index := range retained {
		result.SealAttempts++
		entry, err := sealAndMeasureCA42E2EFile(ctx, index+1, &retained[index])
		if err != nil {
			return result, fmt.Errorf("CA42 E2E storage sealing entry %d: %w", index+1, err)
		}
		result.VerifiedSealedCount++
		lines = append(lines, entry)
	}
	inventory := strings.Join(lines, "\n") + "\n"
	inventorySHA := sha256.Sum256([]byte(inventory))
	headers := []string{
		"format=" + Format,
		"profile=" + Profile,
		"release_id=" + input.ReleaseID,
		"release_run_id=" + input.ReleaseRunID,
		"attempt_id=" + input.AttemptID,
		"architecture=" + input.Architecture,
		"attempt_root_hex=" + hex.EncodeToString([]byte(root)),
		"content_hash_algorithm=sha256",
		"verity_hash_algorithm=sha256",
		"attempt_entry_count=" + strconv.Itoa(AttemptEntryCount),
		"migration_entry_count=" + strconv.Itoa(MigrationEntryCount),
		"runtime_entry_count=" + strconv.Itoa(len(input.RuntimeFiles)),
		"entry_count=" + strconv.Itoa(len(files)),
		"inventory_sha256=" + hex.EncodeToString(inventorySHA[:]),
	}
	raw := []byte(strings.Join(headers, "\n") + "\n" + inventory)
	digest := sha256.Sum256(raw)
	descriptor, err := Parse(raw, digest)
	if err != nil {
		return result, fmt.Errorf("CA42 E2E storage descriptor is not production-canonical: %w", err)
	}
	result.Raw = raw
	result.Descriptor = descriptor
	result.Tainted = false
	return result, nil
}

func preflightCA42E2EFile(ctx context.Context, spec ca42E2EFile, fixtureDevice, hostRootDevice uint64) (ca42E2ERetainedFile, error) {
	var result ca42E2ERetainedFile
	if spec.path == "" || spec.mode == 0 {
		return result, errors.New("file specification invalid")
	}
	fd, err := unix.Open(spec.path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return result, err
	}
	file := os.NewFile(uintptr(fd), spec.path)
	if file == nil {
		_ = unix.Close(fd)
		return result, errors.New("retained file unavailable")
	}
	stat, mountID, err := fdIdentity(file)
	if err != nil {
		_ = file.Close()
		return result, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 ||
		stat.Size <= 0 || uint32(stat.Mode&0o7777) != spec.mode {
		_ = file.Close()
		return result, errors.New("root-owned single-link file identity invalid")
	}
	maximum := MaxOrdinaryEntryBytes
	if spec.role == "database_dump" {
		maximum = MaxDatabaseDumpBytes
	}
	if uint64(stat.Size) > maximum {
		_ = file.Close()
		return result, errors.New("inventory file size exceeds production bound")
	}
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(fd, &filesystem); err != nil || filesystem.Type != unix.EXT4_SUPER_MAGIC {
		_ = file.Close()
		return result, errors.New("inventory file is not on the isolated ext4 fixture")
	}
	if uint64(stat.Dev) != fixtureDevice || uint64(stat.Dev) == hostRootDevice {
		_ = file.Close()
		return result, errors.New("inventory file device is not the disposable fixture device")
	}
	content, err := ca42E2EHashFile(ctx, file, uint64(stat.Size))
	if err != nil {
		_ = file.Close()
		return result, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return result, errors.New("inventory file sync failed")
	}
	return ca42E2ERetainedFile{spec: spec, file: file, stat: stat, mountID: mountID, content: content}, nil
}

func sealAndMeasureCA42E2EFile(ctx context.Context, ordinal int, retained *ca42E2ERetainedFile) (string, error) {
	if retained == nil || retained.file == nil {
		return "", errors.New("retained inventory file invalid")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := enableCA42E2EVerity(retained.file); err != nil {
		return "", err
	}
	verity, err := MeasureVerity(retained.file)
	if err != nil {
		return "", err
	}
	afterContent, err := ca42E2EHashFile(ctx, retained.file, uint64(retained.stat.Size))
	if err != nil || afterContent != retained.content {
		return "", errors.New("inventory content changed before fs-verity sealing")
	}
	after, afterMountID, err := fdIdentity(retained.file)
	if err != nil || !sameSecurityIdentity(retained.stat, after) || retained.mountID != afterMountID {
		return "", errors.New("inventory file changed while enabling fs-verity")
	}
	return fmt.Sprintf("entry=%06d|%s|%s|%s|%s|%04o|%d|%d|%d|%d|%d|%d|%d|%x|%x",
		ordinal, retained.spec.scope, retained.spec.role, retained.spec.kind, hex.EncodeToString([]byte(retained.spec.path)), retained.spec.mode,
		retained.stat.Uid, retained.stat.Gid, retained.stat.Nlink, retained.stat.Size, retained.stat.Dev, retained.stat.Ino,
		retained.mountID, retained.content, verity), nil
}

func enableCA42E2EVerity(file *os.File) error {
	argument := unix.FsverityEnableArg{Version: 1, Hash_algorithm: unix.FS_VERITY_HASH_ALG_SHA256, Block_size: uint32(os.Getpagesize())}
	var errno syscall.Errno
	for attempt := 0; attempt < 3; attempt++ {
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, file.Fd(), uintptr(unix.FS_IOC_ENABLE_VERITY), uintptr(unsafe.Pointer(&argument)))
		if errno != unix.EINTR {
			break
		}
	}
	runtime.KeepAlive(file)
	if errno != 0 {
		return fmt.Errorf("fs-verity enable failed: %w", errno)
	}
	return nil
}

func ca42E2EHashFile(ctx context.Context, file *os.File, size uint64) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	hasher := sha256.New()
	buffer := make([]byte, 1<<20)
	for offset := int64(0); uint64(offset) < size; {
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
			return empty, errors.New("inventory file read failed")
		}
		if count == 0 {
			return empty, errors.New("inventory file truncated")
		}
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func requireCA42E2EStorageIsolation() error {
	if os.Getenv("PANDORA_CA42_E2E_ISOLATED") != "1" || unix.Geteuid() != 0 {
		return errors.New("CA42 E2E storage isolation marker or root identity unavailable")
	}
	for fd, kind := range map[int]string{ca42E2EHostPIDNamespaceFD: "pid", ca42E2EHostMountNamespaceFD: "mnt"} {
		link, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		if err != nil || !strings.HasPrefix(link, kind+":[") {
			return fmt.Errorf("CA42 E2E inherited host %s namespace unavailable", kind)
		}
		var host, current unix.Stat_t
		if err := unix.Fstat(fd, &host); err != nil {
			return err
		}
		if err := unix.Stat("/proc/thread-self/ns/"+kind, &current); err != nil {
			return err
		}
		if host.Dev == current.Dev && host.Ino == current.Ino {
			return fmt.Errorf("CA42 E2E %s namespace did not diverge from host", kind)
		}
	}
	for _, root := range []string{"/etc", "/var/lib", "/run", "/root", "/usr", "/lib", "/lib64"} {
		var filesystem unix.Statfs_t
		if err := unix.Statfs(root, &filesystem); err != nil || filesystem.Type != unix.TMPFS_MAGIC {
			return fmt.Errorf("CA42 E2E storage isolation root is not tmpfs: %s", root)
		}
	}
	for _, root := range []string{"/var/lib/pandora", "/run/pandora"} {
		var filesystem unix.Statfs_t
		if err := unix.Statfs(root, &filesystem); err != nil || filesystem.Type != unix.EXT4_SUPER_MAGIC {
			return fmt.Errorf("CA42 E2E storage fixture root is not ext4: %s", root)
		}
	}
	return nil
}

func validateCA42E2EFileLayout(files []ca42E2EFile, root string, runtimeCount int, architecture string) error {
	entries := make([]Entry, len(files))
	for index, file := range files {
		entries[index] = Entry{Ordinal: uint64(index + 1), Scope: file.scope, Role: file.role, Kind: file.kind,
			Path: file.path, Mode: fmt.Sprintf("%04o", file.mode)}
	}
	if err := validateInventory(entries, root, runtimeCount, architecture); err != nil {
		return fmt.Errorf("CA42 E2E storage layout invalid: %w", err)
	}
	return nil
}

func ca42E2EFixtureDevices() (uint64, uint64, error) {
	var fixture, hostRoot unix.Stat_t
	if err := unix.Stat("/var/lib/pandora", &fixture); err != nil {
		return 0, 0, err
	}
	if err := unix.Stat("/", &hostRoot); err != nil {
		return 0, 0, err
	}
	if fixture.Dev == hostRoot.Dev {
		return 0, 0, errors.New("CA42 E2E fixture device aliases the inherited root device")
	}
	return uint64(fixture.Dev), uint64(hostRoot.Dev), nil
}

func parseCA42E2EMode(value string) uint32 {
	parsed, _ := strconv.ParseUint(value, 8, 32)
	return uint32(parsed)
}
