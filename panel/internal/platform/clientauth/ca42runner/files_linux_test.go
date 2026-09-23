//go:build linux

package ca42runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenRootOwnedArtifactAtEnforcesFrozenModeAndDigest(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root ownership contract requires root")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	for _, test := range []struct {
		name string
		mode os.FileMode
	}{
		{name: "manifest", mode: 0o400},
		{name: "attestation-core", mode: 0o500},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := bytes.Repeat([]byte(test.name), 8193)
			path := directory + "/" + test.name
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(content)
			file, got, gotDigest, err := openRootOwnedArtifactAt(context.Background(), int(parent.Fd()), test.name, int64(len(content)), uint32(test.mode), &digest)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, content) || gotDigest != digest {
				t.Fatal("trusted artifact bytes or digest changed")
			}
			if file.Fd() < 3 {
				t.Fatalf("trusted artifact returned low descriptor %d", file.Fd())
			}
			flags, flagErr := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
			fdFlags, fdFlagErr := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
			offset, seekErr := file.Seek(0, 1)
			if flagErr != nil || fdFlagErr != nil || seekErr != nil || flags&unix.O_ACCMODE != unix.O_RDONLY ||
				fdFlags&unix.FD_CLOEXEC == 0 || offset != 0 {
				t.Fatalf("trusted descriptor contract drifted: flags=%d fdflags=%d offset=%d errors=%v/%v/%v", flags, fdFlags, offset, flagErr, fdFlagErr, seekErr)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTrustedDirectoryAndArtifactRejectNonRootGroup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root ownership contract requires root")
	}
	directory := t.TempDir()
	if err := os.Chown(directory, 0, 1); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireRootOwnedDirectory(int(parent.Fd())); err == nil {
		parent.Close()
		t.Fatal("non-root directory group was accepted")
	}
	if err := os.Chown(directory, 0, 0); err != nil {
		parent.Close()
		t.Fatal(err)
	}
	path := directory + "/artifact"
	if err := os.WriteFile(path, []byte("artifact"), 0o400); err != nil {
		parent.Close()
		t.Fatal(err)
	}
	if err := os.Chown(path, 0, 1); err != nil {
		parent.Close()
		t.Fatal(err)
	}
	if _, _, _, err := openRootOwnedArtifactAt(context.Background(), int(parent.Fd()), "artifact", 8, 0o400, nil); err == nil {
		parent.Close()
		t.Fatal("non-root artifact group was accepted")
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRootOwnedArtifactAtRejectsCancellationAndInvalidMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := openRootOwnedArtifactAt(ctx, -1, "artifact", 1, 0o400, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled request was not preserved: %v", err)
	}
	if _, _, _, err := openRootOwnedArtifactAt(context.Background(), -1, "artifact", 1, 0o700, nil); err == nil {
		t.Fatal("unsupported artifact mode was accepted")
	}
}
