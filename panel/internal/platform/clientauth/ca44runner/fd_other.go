//go:build !linux

package ca44runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

// ErrTrustedFilesUnsupported is returned on platforms that cannot provide the
// required Linux openat2 trust boundary.
var ErrTrustedFilesUnsupported = errors.New("trusted files: platform unsupported")

type TrustedFileSpec struct {
	RelativePath   string
	ExpectedSHA256 string
	MaxBytes       int64
	ExactMode      os.FileMode
	ExpectedUID    uint32
}

// TrustedFileIdentity is deliberately opaque and cannot be initialized on an
// unsupported platform.
type TrustedFileIdentity struct {
	initialized bool
}

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
	if _, err := validateTrustedFileSpec(spec); err != nil {
		return nil, TrustedFileIdentity{}, err
	}
	return nil, TrustedFileIdentity{}, ErrTrustedFilesUnsupported
}

func VerifySameFile(file *os.File, expected TrustedFileIdentity, expectedOffset int64) error {
	return ErrTrustedFilesUnsupported
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
