//go:build ca42e2e && linux && (amd64 || arm64)

package ca42runner

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"golang.org/x/sys/unix"
)

const (
	ca42E2EHostPIDNamespaceFD   = 8
	ca42E2EHostMountNamespaceFD = 9
)

// compiledRootKeyset is available only to the explicitly tagged, disposable
// namespace E2E binary. It contains public test keys only. Production release
// gates must reject binaries built with the ca42e2e tag.
func compiledRootKeyset() (ca42authority.RootKeyset, error) {
	if err := requireCA42E2EAttestedNamespace(); err != nil {
		return ca42authority.RootKeyset{}, errors.Join(errCompiledRootsUnprovisioned, err)
	}
	return ca42E2EPublicRootKeyset(), nil
}

func ca42E2EPublicRootKeyset() ca42authority.RootKeyset {
	return ca42authority.RootKeyset{
		ID:     "pandora-ca42-e2e-roots-v1",
		Quorum: ca42authority.RequiredQuorum,
		Keys: []ca42authority.RootKey{
			{ID: "root-a", PublicKey: ed25519.PublicKey{
				0xd5, 0x42, 0x07, 0xda, 0x19, 0x49, 0x77, 0xdc,
				0xf4, 0x6a, 0xdb, 0xfe, 0xc2, 0xbc, 0x2e, 0x75,
				0xb5, 0x2d, 0x5a, 0x8a, 0x42, 0x18, 0x4f, 0xed,
				0xfd, 0xc0, 0x00, 0x24, 0xf0, 0xe3, 0xe8, 0xda,
			}},
			{ID: "root-b", PublicKey: ed25519.PublicKey{
				0x51, 0x1c, 0x34, 0xa1, 0xa2, 0xcb, 0x52, 0x1d,
				0xf1, 0x6b, 0xb2, 0x46, 0xb8, 0xde, 0x8e, 0x79,
				0x97, 0xce, 0x23, 0x5c, 0x7e, 0x76, 0xb2, 0x2a,
				0x3d, 0x75, 0x03, 0xa2, 0x48, 0x19, 0xdd, 0x8a,
			}},
			{ID: "root-c", PublicKey: ed25519.PublicKey{
				0x31, 0xde, 0xbe, 0x55, 0xd3, 0x7c, 0x72, 0x27,
				0x68, 0xb1, 0x37, 0x13, 0x1c, 0xaa, 0x60, 0x87,
				0x08, 0x0b, 0x2e, 0x0b, 0x60, 0xb9, 0x4b, 0xd7,
				0x85, 0xd1, 0x45, 0x75, 0xcf, 0xa4, 0x98, 0xbc,
			}},
		},
	}
}

func requireCA42E2EAttestedNamespace() error {
	if os.Getenv("PANDORA_CA42_E2E_ISOLATED") != "1" || unix.Geteuid() != 0 {
		return errors.New("CA42 E2E isolation marker or root identity unavailable")
	}
	if err := requireDifferentCA42E2ENamespace(ca42E2EHostPIDNamespaceFD, "pid"); err != nil {
		return err
	}
	if err := requireDifferentCA42E2ENamespace(ca42E2EHostMountNamespaceFD, "mnt"); err != nil {
		return err
	}
	for _, root := range []string{"/etc", "/var/lib", "/run", "/root"} {
		var filesystem unix.Statfs_t
		if err := unix.Statfs(root, &filesystem); err != nil {
			return fmt.Errorf("CA42 E2E isolation root unavailable: %s: %w", root, err)
		}
		if filesystem.Type != unix.TMPFS_MAGIC {
			return fmt.Errorf("CA42 E2E isolation root is not tmpfs: %s", root)
		}
	}
	var fixtureFilesystem unix.Statfs_t
	if err := unix.Statfs("/var/lib/pandora", &fixtureFilesystem); err != nil {
		return fmt.Errorf("CA42 E2E fixture root unavailable: %w", err)
	}
	if fixtureFilesystem.Type != unix.EXT4_SUPER_MAGIC {
		return errors.New("CA42 E2E fixture root is not ext4")
	}
	return nil
}

func requireDifferentCA42E2ENamespace(hostFD int, kind string) error {
	link, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", hostFD))
	if err != nil || !strings.HasPrefix(link, kind+":[") {
		return fmt.Errorf("CA42 E2E inherited host %s namespace unavailable: %w", kind, err)
	}
	var host, current unix.Stat_t
	if err := unix.Fstat(hostFD, &host); err != nil {
		return fmt.Errorf("CA42 E2E inherited host %s namespace invalid: %w", kind, err)
	}
	if err := unix.Stat("/proc/thread-self/ns/"+kind, &current); err != nil {
		return fmt.Errorf("CA42 E2E current %s namespace unavailable: %w", kind, err)
	}
	if host.Dev == current.Dev && host.Ino == current.Ino {
		return fmt.Errorf("CA42 E2E %s namespace did not diverge from host", kind)
	}
	return nil
}
