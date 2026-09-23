//go:build linux && (amd64 || arm64)

package ca42credential

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const commitmentKeyDescriptionPrefix = "pandora-ca42-commitment:"

const requiredKeySeals = unix.F_SEAL_WRITE | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_SEAL

const requiredKernelKeyPermissions uint32 = 0x03010000

// CommitmentKeyFD is a retained, sealed snapshot of one root-owned kernel
// keyring payload. It deliberately does not expose its file descriptor or key
// bytes to callers.
type CommitmentKeyFD struct {
	state *commitmentKeyFDState
}

type commitmentKeyFDState struct {
	mu     sync.Mutex
	file   *os.File
	keyID  string
	dev    uint64
	ino    uint64
	closed bool
}

// LoadCommitmentKeyFD snapshots the exact 32-byte user key named by keyID from
// the current root process keyring into a sealed memfd. It never searches the
// session or user keyrings and never falls back to files or environment data.
func LoadCommitmentKeyFD(keyID string) (*CommitmentKeyFD, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("credential commitment key loader requires EUID 0")
	}
	if !token.MatchString(keyID) || keyID == "none" {
		return nil, errors.New("credential commitment key ID invalid")
	}
	description := commitmentKeyDescriptionPrefix + keyID
	processRing, err := unix.KeyctlGetKeyringID(unix.KEY_SPEC_PROCESS_KEYRING, false)
	if err != nil {
		return nil, fmt.Errorf("credential commitment process keyring unavailable: %w", err)
	}
	serial, metadata, err := findDirectProcessUserKey(processRing, description)
	if err != nil {
		return nil, err
	}
	var payload [sha256.Size + 1]byte
	read, err := unix.KeyctlBuffer(unix.KEYCTL_READ, serial, payload[:], 0)
	if err != nil {
		return nil, fmt.Errorf("credential commitment key read denied: %w", err)
	}
	if read != sha256.Size {
		clear(payload[:])
		return nil, errors.New("credential commitment key size invalid")
	}
	metadataAfter, err := unix.KeyctlString(unix.KEYCTL_DESCRIBE, serial)
	if err != nil || metadataAfter != metadata {
		clear(payload[:])
		return nil, errors.New("credential commitment key metadata changed")
	}
	key, err := newSealedCommitmentKeyFD(keyID, payload[:sha256.Size])
	clear(payload[:])
	return key, err
}

func findDirectProcessUserKey(ringID int, expectedDescription string) (int, string, error) {
	var linked [4096]byte
	read, err := unix.KeyctlBuffer(unix.KEYCTL_READ, ringID, linked[:], 0)
	if err != nil || read <= 0 || read > len(linked) || read%4 != 0 {
		return 0, "", errors.New("credential commitment process keyring layout invalid")
	}
	serial, matchedMetadata := 0, ""
	for offset := 0; offset < read; offset += 4 {
		candidate := int(int32(binary.NativeEndian.Uint32(linked[offset : offset+4])))
		metadata, describeErr := unix.KeyctlString(unix.KEYCTL_DESCRIBE, candidate)
		if describeErr != nil {
			return 0, "", errors.New("credential commitment direct key description denied")
		}
		parts := strings.SplitN(metadata, ";", 5)
		if len(parts) == 5 && parts[0] == "user" && parts[4] == expectedDescription {
			if serial != 0 {
				return 0, "", errors.New("multiple directly linked credential commitment keys")
			}
			if err := validateKernelKeyDescription(metadata, expectedDescription); err != nil {
				return 0, "", err
			}
			serial, matchedMetadata = candidate, metadata
		}
	}
	if serial == 0 {
		return 0, "", errors.New("credential commitment key is not directly linked")
	}
	return serial, matchedMetadata, nil
}

func validateKernelKeyDescription(metadata, expectedDescription string) error {
	parts := strings.SplitN(metadata, ";", 5)
	if len(parts) != 5 || parts[0] != "user" || parts[1] != "0" || parts[2] != "0" ||
		len(parts[3]) != 8 || parts[4] != expectedDescription {
		return errors.New("credential commitment key metadata invalid")
	}
	permissions, err := strconv.ParseUint(parts[3], 16, 32)
	if err != nil || uint32(permissions) != requiredKernelKeyPermissions {
		return errors.New("credential commitment key permissions invalid")
	}
	return nil
}

func newSealedCommitmentKeyFD(keyID string, payload []byte) (*CommitmentKeyFD, error) {
	if !token.MatchString(keyID) || keyID == "none" || len(payload) != sha256.Size {
		return nil, errors.New("credential commitment key snapshot input invalid")
	}
	fd, err := unix.MemfdCreate("pandora-ca42-commitment-key", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("credential commitment memfd create failed: %w", err)
	}
	file := os.NewFile(uintptr(fd), "pandora-ca42-commitment-key")
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()
	written, err := unix.Write(fd, payload)
	if err != nil || written != sha256.Size {
		return nil, errors.New("credential commitment memfd write failed")
	}
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		return nil, fmt.Errorf("credential commitment memfd rewind failed: %w", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, requiredKeySeals); err != nil {
		return nil, fmt.Errorf("credential commitment memfd seal failed: %w", err)
	}
	seals, err := unix.FcntlInt(uintptr(fd), unix.F_GET_SEALS, 0)
	if err != nil || seals&requiredKeySeals != requiredKeySeals {
		return nil, errors.New("credential commitment memfd seal verification failed")
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 0 || stat.Size != sha256.Size {
		return nil, errors.New("credential commitment memfd identity invalid")
	}
	closeOnError = false
	return &CommitmentKeyFD{state: &commitmentKeyFDState{file: file, keyID: keyID, dev: uint64(stat.Dev), ino: stat.Ino}}, nil
}

// VerifyCredential revalidates the retained sealed memfd and uses its payload
// for exactly one HMAC verification. The temporary byte array is cleared.
func (key *CommitmentKeyFD) VerifyCredential(secret []byte, descriptor Descriptor, now time.Time) error {
	if key == nil || key.state == nil {
		return errors.New("credential commitment key FD closed")
	}
	state := key.state
	state.mu.Lock()
	defer state.mu.Unlock()
	trustedDescriptor, err := descriptor.VerifiedCopy(now)
	if err != nil {
		return err
	}
	if state.closed || state.file == nil || trustedDescriptor.CommitmentKeyID != state.keyID {
		return errors.New("credential commitment key FD binding invalid")
	}
	fd := int(state.file.Fd())
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 0 || stat.Size != sha256.Size ||
		uint64(stat.Dev) != state.dev || stat.Ino != state.ino {
		return errors.New("credential commitment key FD identity changed")
	}
	seals, err := unix.FcntlInt(state.file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&requiredKeySeals != requiredKeySeals {
		return errors.New("credential commitment key FD seals changed")
	}
	var payload [sha256.Size]byte
	read, err := unix.Pread(fd, payload[:], 0)
	if err != nil || read != sha256.Size {
		clear(payload[:])
		return errors.New("credential commitment key FD read failed")
	}
	defer clear(payload[:])
	return VerifyHMACCredential(secret, payload[:], trustedDescriptor, now)
}

func (key *CommitmentKeyFD) Close() error {
	if key == nil || key.state == nil {
		return nil
	}
	state := key.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	state.closed = true
	if state.file == nil {
		return nil
	}
	err := state.file.Close()
	state.file = nil
	return err
}
