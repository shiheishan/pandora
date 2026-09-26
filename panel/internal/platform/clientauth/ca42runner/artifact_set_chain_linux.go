//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 artifact_set_linux.go 的 retainedArtifact 与 artifactSpec，依赖同包的 retainedIdentity 与迁移清单，依赖 golang.org/x/sys/unix
// [OUTPUT]: 包内提供 openFixedArtifactDirectoryAt、requireExactDirectoryEntries、validateAbsoluteArtifactChain、absoluteArtifactChainDigest、appendPathChainRecord
// [POS]: ca42runner 制品集的目录与绝对路径链：从 artifact_set_linux.go 拆出。固定目录按设备打开、条目须与清单完全一致；制品从根到自身的整条路径链逐级记录身份并算摘要
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca42runner

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

func openFixedArtifactDirectoryAt(parent *os.File, name string, expectedDevice uint64) (*os.File, retainedIdentity, error) {
	if parent == nil || name != MigrationsDirectoryName || expectedDevice == 0 {
		return nil, retainedIdentity{}, errors.New("CA42 fixed artifact directory request invalid")
	}
	fd, err := unix.Openat2(int(parent.Fd()), name, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, retainedIdentity{}, err
	}
	identity, err := retainedStat(fd)
	if err != nil || identity.mode&unix.S_IFMT != unix.S_IFDIR || identity.uid != 0 || identity.nlink < 1 ||
		identity.mode&0o7777 != 0o700 || identity.dev != expectedDevice {
		unix.Close(fd)
		return nil, retainedIdentity{}, errors.New("CA42 fixed artifact directory identity denied")
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, retainedIdentity{}, errors.New("CA42 fixed artifact directory descriptor conversion failed")
	}
	return file, identity, nil
}

func requireExactDirectoryEntries(directory *os.File, expected []migrationInventoryEntry) error {
	if directory == nil || len(expected) != migrationInventoryCount {
		return errors.New("CA42 migration directory inventory invalid")
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	if _, seekErr := directory.Seek(0, io.SeekStart); err == nil && seekErr != nil {
		err = seekErr
	}
	if err != nil || len(entries) != len(expected) {
		return errors.New("CA42 migration directory entry count mismatch")
	}
	actual := make([]string, len(entries))
	for index, entry := range entries {
		actual[index] = entry.Name()
	}
	sort.Strings(actual)
	expectedNames := make([]string, len(expected))
	for index, entry := range expected {
		expectedNames[index] = entry.name
	}
	if !equalStringSlices(actual, expectedNames) {
		return errors.New("CA42 migration directory entries mismatch")
	}
	return nil
}

func equalStringSlices(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func validateAbsoluteArtifactChain(ctx context.Context, artifact *retainedArtifact) error {
	if artifact == nil || artifact.file == nil || artifact.absolutePath == "" || artifact.expectedChainHex == "" ||
		!strings.HasPrefix(artifact.absolutePath, "/") || path.Clean(artifact.absolutePath) != artifact.absolutePath ||
		strings.Contains(artifact.absolutePath, "//") || !artifactSHA256Pattern.MatchString(artifact.expectedChainHex) {
		return errors.New("CA42 artifact path-chain request invalid")
	}
	actual, err := absoluteArtifactChainDigest(ctx, artifact.file, artifact.identity, artifact.absolutePath)
	if err != nil {
		return err
	}
	expected, _ := hex.DecodeString(artifact.expectedChainHex)
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return errors.New("CA42 artifact path-chain SHA256 mismatch")
	}
	return nil
}

func absoluteArtifactChainDigest(ctx context.Context, file *os.File, identity retainedIdentity, absolutePath string) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if ctx == nil || file == nil || absolutePath == "" || !strings.HasPrefix(absolutePath, "/") ||
		path.Clean(absolutePath) != absolutePath || strings.Contains(absolutePath, "//") {
		return zero, errors.New("CA42 artifact path-chain request invalid")
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if retained, err := retainedStat(int(file.Fd())); err != nil || retained != identity {
		return zero, errors.New("CA42 artifact path-chain retained target changed")
	}
	components := strings.Split(strings.TrimPrefix(absolutePath, "/"), "/")
	currentFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return zero, err
	}
	hasher := sha256.New()
	defer func() { _ = unix.Close(currentFD) }()
	if err := appendPathChainRecord(hasher, 0, "directory", currentFD, false); err != nil {
		return zero, err
	}
	for index, component := range components {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if component == "" || component == "." || component == ".." {
			return zero, errors.New("CA42 artifact path component invalid")
		}
		last := index == len(components)-1
		flags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		kind := "directory"
		if last {
			flags = unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW
			kind = "artifact"
		}
		nextFD, openErr := unix.Openat2(currentFD, component, &unix.OpenHow{
			Flags: uint64(flags), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		if openErr != nil {
			return zero, openErr
		}
		unix.Close(currentFD)
		currentFD = nextFD
		if err := appendPathChainRecord(hasher, index+1, kind, currentFD, last); err != nil {
			return zero, err
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
	}
	chainTarget, err := retainedStat(currentFD)
	if err != nil || chainTarget != identity {
		return zero, errors.New("CA42 artifact path-chain target binding mismatch")
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func appendPathChainRecord(writer io.Writer, index int, kind string, fd int, artifact bool) error {
	identity, err := retainedStat(fd)
	if err != nil {
		return err
	}
	if artifact {
		if identity.mode&unix.S_IFMT != unix.S_IFREG || identity.uid != 0 || identity.gid != 0 || identity.nlink != 1 || identity.mode&0o022 != 0 {
			return errors.New("CA42 artifact path-chain target denied")
		}
	} else if identity.mode&unix.S_IFMT != unix.S_IFDIR || identity.uid != 0 || identity.gid != 0 || identity.nlink < 1 || identity.mode&0o022 != 0 {
		return errors.New("CA42 artifact path-chain ancestor denied")
	}
	_, err = fmt.Fprintf(writer, "%d|%s|%d|%d|%d|%04o|%d\n", index, kind, identity.dev, identity.ino, identity.uid, identity.mode&0o7777, identity.nlink)
	return err
}
