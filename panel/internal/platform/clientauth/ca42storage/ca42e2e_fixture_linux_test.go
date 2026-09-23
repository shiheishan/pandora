//go:build ca42e2e && linux && (amd64 || arm64)

package ca42storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

const ca42E2EStorageBinaryMarker = "pandora-ca42-e2e-storage-fixture-v1"

func TestCA42E2EStorageDescriptorBuilderFailsClosedOutsideHarness(t *testing.T) {
	original, present := os.LookupEnv("PANDORA_CA42_E2E_ISOLATED")
	if err := os.Unsetenv("PANDORA_CA42_E2E_ISOLATED"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv("PANDORA_CA42_E2E_ISOLATED", original)
		} else {
			_ = os.Unsetenv("PANDORA_CA42_E2E_ISOLATED")
		}
	})
	result, err := BuildCA42E2EStorageDescriptor(context.Background(), CA42E2EStorageInput{
		ReleaseID: "release-e2e", ReleaseRunID: "run-e2e", AttemptID: "attempt-e2e", Architecture: runtime.GOARCH,
	})
	if err == nil || result.Raw != nil || result.Descriptor.IsParsed() || result.Tainted ||
		result.SealAttempts != 0 || result.VerifiedSealedCount != 0 {
		t.Fatalf("unattested process built E2E storage descriptor: result=%+v err=%v", result, err)
	}
}

func TestCA42E2EStorageDescriptorTwoPhaseNative(t *testing.T) {
	if err := requireCA42E2EStorageIsolationBase(); err != nil {
		t.Fatal(err)
	}
	t.Log(ca42E2EStorageBinaryMarker)
	prepareCA42E2ERuntimeMountRoots(t)

	input, first, duplicateSource, duplicateTarget := provisionCA42E2EStorageFiles(t, "preflight")
	if err := os.Remove(duplicateTarget); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(duplicateSource, duplicateTarget); err != nil {
		t.Fatal(err)
	}
	failed, err := BuildCA42E2EStorageDescriptor(context.Background(), input)
	if err == nil || failed.Tainted || failed.SealAttempts != 0 || failed.VerifiedSealedCount != 0 {
		t.Fatalf("preflight failure crossed into sealing: result=%+v err=%v", failed, err)
	}
	assertCA42E2ENotVeritySealed(t, first)
	if err := os.Remove(duplicateTarget); err != nil {
		t.Fatal(err)
	}
	writeCA42E2EFile(t, duplicateTarget, 0o400, []byte("repaired-unique-migration\n"))

	verified, err := BuildCA42E2EStorageDescriptor(context.Background(), input)
	if err != nil || verified.Tainted || verified.SealAttempts != len(verified.Descriptor.Entries) ||
		verified.VerifiedSealedCount != len(verified.Descriptor.Entries) {
		t.Fatalf("complete fs-verity descriptor fixture failed: result=%+v err=%v", verified, err)
	}
	digest := sha256.Sum256(verified.Raw)
	parsed, err := Parse(verified.Raw, digest)
	if err != nil || !parsed.IsParsed() || parsed.SHA256 != verified.Descriptor.SHA256 {
		t.Fatalf("sealed descriptor did not round-trip: parsed=%+v err=%v", parsed, err)
	}

	taintedInput, _, _, _ := provisionCA42E2EStorageFiles(t, "tainted")
	presealed := filepath.Join("/run/pandora/ca42", taintedInput.AttemptID, attemptRoles[4].name)
	file, err := os.Open(presealed)
	if err != nil {
		t.Fatal(err)
	}
	if err := enableCA42E2EVerity(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	tainted, err := BuildCA42E2EStorageDescriptor(context.Background(), taintedInput)
	if err == nil || !tainted.Tainted || tainted.SealAttempts != 5 || tainted.VerifiedSealedCount != 4 ||
		tainted.Raw != nil || tainted.Descriptor.IsParsed() {
		t.Fatalf("phase-two failure did not require media destruction: result=%+v err=%v", tainted, err)
	}
}

func requireCA42E2EStorageIsolationBase() error {
	if os.Getenv("PANDORA_CA42_E2E_ISOLATED") != "1" || unix.Geteuid() != 0 {
		return errors.New("native CA42 storage E2E requires isolated root harness")
	}
	for _, root := range []string{"/etc", "/var/lib", "/run", "/root"} {
		var filesystem unix.Statfs_t
		if err := unix.Statfs(root, &filesystem); err != nil || filesystem.Type != unix.TMPFS_MAGIC {
			return fmt.Errorf("native CA42 storage E2E root is not tmpfs: %s", root)
		}
	}
	for _, root := range []string{"/var/lib/pandora", "/run/pandora"} {
		var filesystem unix.Statfs_t
		if err := unix.Statfs(root, &filesystem); err != nil || filesystem.Type != unix.EXT4_SUPER_MAGIC {
			return fmt.Errorf("native CA42 storage E2E fixture root is not ext4: %s", root)
		}
	}
	return nil
}

func prepareCA42E2ERuntimeMountRoots(t *testing.T) {
	t.Helper()
	if err := unix.Mount("tmpfs", "/usr", "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "mode=0755"); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"/usr/bin", "/usr/lib", "/usr/lib64"} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{"/usr/lib", "/usr/lib64"} {
		if err := unix.Mount("tmpfs", directory, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "mode=0755"); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range []string{"/usr", "/lib", "/lib64"} {
		var filesystem unix.Statfs_t
		if err := unix.Statfs(root, &filesystem); err != nil || filesystem.Type != unix.TMPFS_MAGIC {
			t.Fatalf("runtime mount root is not tmpfs: %s", root)
		}
	}
}

func provisionCA42E2EStorageFiles(t *testing.T, label string) (CA42E2EStorageInput, string, string, string) {
	t.Helper()
	attemptID := "attempt-storage-" + label
	root := filepath.Join("/run/pandora/ca42", attemptID)
	if err := os.MkdirAll(filepath.Join(root, "migrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	for index, role := range attemptRoles {
		writeCA42E2EFile(t, filepath.Join(root, role.name), parseCA42E2EMode(role.mode),
			[]byte(fmt.Sprintf("%s-attempt-%02d-%s\n", label, index+1, role.role)))
	}
	for index, name := range migrationNames {
		writeCA42E2EFile(t, filepath.Join(root, "migrations", name), 0o400,
			[]byte(fmt.Sprintf("-- %s migration %02d\nSELECT %d;\n", label, index+1, index+1)))
	}
	runtimeRoot := filepath.Join("/var/lib/pandora", "ca42-storage-runtime-"+label)
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	loader := map[string]string{"amd64": "/lib64/ld-linux-x86-64.so.2", "arm64": "/lib/ld-linux-aarch64.so.1"}[runtime.GOARCH]
	runtimeFiles := []CA42E2ERuntimeFile{
		{Role: "bash", Path: "/usr/bin/bash", Mode: 0o755},
		{Role: "docker", Path: "/usr/bin/docker", Mode: 0o755},
		{Role: "runtime_loader", Path: loader, Mode: 0o755},
		{Role: "runtime_library", Path: "/usr/lib/libc.so.6", Mode: 0o644},
	}
	for index, entry := range runtimeFiles {
		source := filepath.Join(runtimeRoot, fmt.Sprintf("runtime-%02d", index+1))
		writeCA42E2EFile(t, source, entry.Mode, []byte(fmt.Sprintf("%s-runtime-%02d-%s\n", label, index+1, entry.Role)))
		if err := os.MkdirAll(filepath.Dir(entry.Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := unix.Unmount(entry.Path, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			t.Fatal(err)
		}
		writeCA42E2EFile(t, entry.Path, entry.Mode, []byte("tmpfs-bind-target\n"))
		if err := unix.Mount(source, entry.Path, "", unix.MS_BIND, ""); err != nil {
			t.Fatal(err)
		}
	}
	return CA42E2EStorageInput{ReleaseID: "release-" + label, ReleaseRunID: "run-" + label,
			AttemptID: attemptID, Architecture: runtime.GOARCH, RuntimeFiles: runtimeFiles},
		filepath.Join(root, attemptRoles[0].name),
		filepath.Join(root, "migrations", migrationNames[len(migrationNames)-2]),
		filepath.Join(root, "migrations", migrationNames[len(migrationNames)-1])
}

func writeCA42E2EFile(t *testing.T, path string, mode uint32, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, os.FileMode(mode)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, os.FileMode(mode)); err != nil {
		t.Fatal(err)
	}
}

func assertCA42E2ENotVeritySealed(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := MeasureVerity(file); err == nil {
		t.Fatalf("preflight failure sealed an earlier inventory file: %s", path)
	}
}
