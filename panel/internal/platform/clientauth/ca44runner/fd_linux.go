//go:build linux

package ca44runner

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrTrustedFilesUnsupported is returned by non-Linux builds. Linux callers
// may use errors.Is without weakening fail-closed behavior on other platforms.
var ErrTrustedFilesUnsupported = errors.New("trusted files: platform unsupported")

// TrustedFileSpec is the complete pathname-independent policy applied while a
// regular file is opened beneath a retained trusted directory descriptor.
type TrustedFileSpec struct {
	RelativePath   string
	ExpectedSHA256 string
	MaxBytes       int64
	ExactMode      os.FileMode
	ExpectedUID    uint32
}

// TrustedFileIdentity is an opaque snapshot of the object and content opened
// by OpenTrustedRegularAt. Keeping its fields private prevents callers from
// manufacturing an identity that did not pass the initial trust checks.
type TrustedFileIdentity struct {
	dev         uint64
	ino         uint64
	mode        uint32
	uid         uint32
	gid         uint32
	nlink       uint64
	size        int64
	mtimeSec    int64
	mtimeNsec   int64
	ctimeSec    int64
	ctimeNsec   int64
	sha256      [sha256.Size]byte
	initialized bool
}

// OpenTrustedRegularAt opens one clean relative path under rootFD. It does not
// fall back when openat2 is unavailable and never reopens the path after the
// trusted descriptor has been obtained.
func OpenTrustedRegularAt(
	rootFD int,
	relativePath string,
	expectedSHA string,
	maxBytes int64,
	exactMode os.FileMode,
	expectedUID uint32,
) (*os.File, TrustedFileIdentity, error) {
	spec := TrustedFileSpec{
		RelativePath:   relativePath,
		ExpectedSHA256: expectedSHA,
		MaxBytes:       maxBytes,
		ExactMode:      exactMode,
		ExpectedUID:    expectedUID,
	}
	expectedDigest, err := validateTrustedFileSpec(spec)
	if err != nil {
		return nil, TrustedFileIdentity{}, err
	}
	if rootFD < 0 {
		return nil, TrustedFileIdentity{}, errors.New("trusted files: invalid root descriptor")
	}

	fd, trustedDevice, err := openTrustedRegularPath(rootFD, relativePath, expectedUID)
	if err != nil {
		return nil, TrustedFileIdentity{}, fmt.Errorf("trusted files: openat2 denied: %w", err)
	}
	file := os.NewFile(uintptr(fd), "trusted-file")
	if file == nil {
		_ = unix.Close(fd)
		return nil, TrustedFileIdentity{}, errors.New("trusted files: descriptor conversion failed")
	}
	success := false
	defer func() {
		if !success {
			_ = file.Close()
		}
	}()

	before, err := trustedIdentityFromFile(file)
	if err != nil {
		return nil, TrustedFileIdentity{}, fmt.Errorf("trusted files: initial fstat failed: %w", err)
	}
	if err := enforceTrustedIdentityPolicy(before, spec); err != nil {
		return nil, TrustedFileIdentity{}, err
	}
	if before.dev != trustedDevice {
		return nil, TrustedFileIdentity{}, errors.New("trusted files: device boundary mismatch")
	}
	if err := requireFileOffset(file, 0); err != nil {
		return nil, TrustedFileIdentity{}, err
	}
	digest, err := hashRetainedFile(file, before.size)
	if err != nil {
		return nil, TrustedFileIdentity{}, err
	}
	if subtle.ConstantTimeCompare(digest[:], expectedDigest[:]) != 1 {
		return nil, TrustedFileIdentity{}, errors.New("trusted files: sha256 mismatch")
	}
	after, err := trustedIdentityFromFile(file)
	if err != nil {
		return nil, TrustedFileIdentity{}, fmt.Errorf("trusted files: final fstat failed: %w", err)
	}
	if !sameTrustedStat(before, after) {
		return nil, TrustedFileIdentity{}, errors.New("trusted files: identity changed during verification")
	}
	if err := requireFileOffset(file, 0); err != nil {
		return nil, TrustedFileIdentity{}, err
	}

	after.sha256 = digest
	after.initialized = true
	success = true
	return file, after, nil
}

func openTrustedRegularPath(rootFD int, relativePath string, expectedUID uint32) (int, uint64, error) {
	trustedDevice, err := trustedDirectoryDevice(rootFD, expectedUID)
	if err != nil {
		return -1, 0, err
	}
	parts := strings.Split(relativePath, "/")
	current, err := unix.FcntlInt(uintptr(rootFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, 0, err
	}
	for _, component := range parts[:len(parts)-1] {
		next, openErr := unix.Openat2(current, component, &unix.OpenHow{
			Flags: uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
				unix.RESOLVE_NO_MAGICLINKS,
		})
		unix.Close(current)
		if openErr != nil {
			return -1, 0, openErr
		}
		current = next
		device, validateErr := trustedDirectoryDevice(current, expectedUID)
		if validateErr != nil || device != trustedDevice {
			unix.Close(current)
			if validateErr != nil {
				return -1, 0, validateErr
			}
			return -1, 0, errors.New("trusted files: ancestor device mismatch")
		}
	}
	fd, openErr := unix.Openat2(current, parts[len(parts)-1], &unix.OpenHow{
		Flags: uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_MAGICLINKS,
	})
	unix.Close(current)
	if openErr != nil {
		return -1, 0, openErr
	}
	return fd, trustedDevice, nil
}

func trustedDirectoryDevice(fd int, expectedUID uint32) (uint64, error) {
	var stat unix.Stat_t
	if fd < 0 {
		return 0, errors.New("trusted files: invalid directory descriptor")
	}
	if err := unix.Fstat(fd, &stat); err != nil {
		return 0, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != expectedUID ||
		stat.Nlink < 1 || stat.Mode&0o022 != 0 {
		return 0, errors.New("trusted files: untrusted ancestor directory")
	}
	return uint64(stat.Dev), nil
}

// VerifySameFile revalidates the object, content digest, and shared offset of
// the retained descriptor. Hashing uses ReadAt and therefore cannot advance the
// open-file-description offset inherited by a child process.
func VerifySameFile(file *os.File, expected TrustedFileIdentity, expectedOffset int64) error {
	if file == nil || !expected.initialized || expectedOffset < 0 {
		return errors.New("trusted files: invalid recheck input")
	}
	before, err := trustedIdentityFromFile(file)
	if err != nil {
		return fmt.Errorf("trusted files: recheck fstat failed: %w", err)
	}
	if !sameTrustedStat(before, expected) {
		return errors.New("trusted files: identity differs from trusted snapshot")
	}
	if err := requireFileOffset(file, expectedOffset); err != nil {
		return err
	}
	digest, err := hashRetainedFile(file, expected.size)
	if err != nil {
		return err
	}
	after, err := trustedIdentityFromFile(file)
	if err != nil {
		return fmt.Errorf("trusted files: post-hash fstat failed: %w", err)
	}
	if !sameTrustedStat(before, after) || !sameTrustedStat(after, expected) {
		return errors.New("trusted files: identity changed during recheck")
	}
	if subtle.ConstantTimeCompare(digest[:], expected.sha256[:]) != 1 {
		return errors.New("trusted files: sha256 differs from trusted snapshot")
	}
	return requireFileOffset(file, expectedOffset)
}

func validateTrustedFileSpec(spec TrustedFileSpec) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if err := validateTrustedRelativePath(spec.RelativePath); err != nil {
		return digest, err
	}
	if spec.MaxBytes <= 0 {
		return digest, errors.New("trusted files: maximum size must be positive")
	}
	if spec.ExactMode < 0 || uint64(spec.ExactMode) > 0o777 {
		return digest, errors.New("trusted files: exact mode must contain only permission bits")
	}
	if len(spec.ExpectedSHA256) != sha256.Size*2 || strings.ToLower(spec.ExpectedSHA256) != spec.ExpectedSHA256 {
		return digest, errors.New("trusted files: expected sha256 must be exact lowercase hex")
	}
	decoded, err := hex.DecodeString(spec.ExpectedSHA256)
	if err != nil || len(decoded) != sha256.Size {
		return digest, errors.New("trusted files: expected sha256 must be exact lowercase hex")
	}
	copy(digest[:], decoded)
	return digest, nil
}

func validateTrustedRelativePath(relativePath string) error {
	if relativePath == "" || strings.HasPrefix(relativePath, "/") ||
		strings.HasSuffix(relativePath, "/") || strings.ContainsAny(relativePath, "\\\x00") {
		return errors.New("trusted files: invalid relative path")
	}
	for _, component := range strings.Split(relativePath, "/") {
		if component == "" || component == "." || component == ".." {
			return errors.New("trusted files: invalid relative path")
		}
	}
	return nil
}

func enforceTrustedIdentityPolicy(identity TrustedFileIdentity, spec TrustedFileSpec) error {
	if identity.mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("trusted files: object is not a regular file")
	}
	if identity.nlink != 1 {
		return errors.New("trusted files: regular file must have one link")
	}
	if identity.uid != spec.ExpectedUID {
		return errors.New("trusted files: owner mismatch")
	}
	if identity.mode&0o7777 != uint32(spec.ExactMode) {
		return errors.New("trusted files: mode mismatch")
	}
	if identity.size < 0 || identity.size > spec.MaxBytes {
		return errors.New("trusted files: size exceeds policy")
	}
	return nil
}

func trustedIdentityFromFile(file *os.File) (TrustedFileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return TrustedFileIdentity{}, err
	}
	return TrustedFileIdentity{
		dev:       uint64(stat.Dev),
		ino:       stat.Ino,
		mode:      stat.Mode,
		uid:       stat.Uid,
		gid:       stat.Gid,
		nlink:     uint64(stat.Nlink),
		size:      stat.Size,
		mtimeSec:  stat.Mtim.Sec,
		mtimeNsec: stat.Mtim.Nsec,
		ctimeSec:  stat.Ctim.Sec,
		ctimeNsec: stat.Ctim.Nsec,
	}, nil
}

func sameTrustedStat(left, right TrustedFileIdentity) bool {
	return left.dev == right.dev && left.ino == right.ino &&
		left.mode == right.mode && left.uid == right.uid && left.gid == right.gid &&
		left.nlink == right.nlink && left.size == right.size &&
		left.mtimeSec == right.mtimeSec && left.mtimeNsec == right.mtimeNsec &&
		left.ctimeSec == right.ctimeSec && left.ctimeNsec == right.ctimeNsec
}

func hashRetainedFile(file *os.File, size int64) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if size < 0 {
		return digest, errors.New("trusted files: invalid file size")
	}
	hasher := sha256.New()
	n, err := io.Copy(hasher, io.NewSectionReader(file, 0, size))
	if err != nil {
		return digest, fmt.Errorf("trusted files: retained descriptor read failed: %w", err)
	}
	if n != size {
		return digest, errors.New("trusted files: short read from retained descriptor")
	}
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func requireFileOffset(file *os.File, expected int64) error {
	offset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("trusted files: offset inspection failed: %w", err)
	}
	if offset != expected {
		return errors.New("trusted files: shared offset mismatch")
	}
	return nil
}
