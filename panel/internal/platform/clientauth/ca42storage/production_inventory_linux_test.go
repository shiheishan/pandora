//go:build linux && (amd64 || arm64)

package ca42storage

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanonicalInventoryRelativeRejectsNamespaceEscape(t *testing.T) {
	for _, invalid := range []string{"", "/", "relative", "/a//b", "/a/../b", "/a/./b", "/a\\b", "/a\x00b"} {
		if _, err := canonicalInventoryRelative(invalid); err == nil {
			t.Fatalf("invalid path accepted: %q", invalid)
		}
	}
	if got, err := canonicalInventoryRelative("/run/pandora/ca42/attempt/file"); err != nil || got != "run/pandora/ca42/attempt/file" {
		t.Fatalf("canonical path rejected: got=%q err=%v", got, err)
	}
}

func TestProductionInventoryBinderRebindsExactPathIdentity(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned production inventory test requires root")
	}
	directory := productionInventoryTestDir(t)
	path := filepath.Join(directory, "artifact")
	if err := os.WriteFile(path, []byte("bound artifact"), 0o400); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	entry := productionBoundEntryForPath(t, path, path, now)
	binder, err := openProductionInventoryBinder()
	if err != nil {
		t.Fatal(err)
	}
	defer binder.close()
	opened, err := binder.openEntry(entry, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(directory, "artifact.old")
	if err := os.Rename(path, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bound artifact"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := binder.rebind(context.Background(), entry, time.Now().UTC()); err == nil {
		t.Fatal("replacement inode passed production path rebind")
	}
}

func TestProductionInventoryBinderRejectsSymlinkAndCanceledContext(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned production inventory test requires root")
	}
	directory := productionInventoryTestDir(t)
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("target"), 0o400); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	entry := productionBoundEntryForPath(t, target, link, now)
	binder, err := openProductionInventoryBinder()
	if err != nil {
		t.Fatal(err)
	}
	defer binder.close()
	if _, err := binder.openEntry(entry, now); err == nil {
		t.Fatal("symlink inventory path accepted")
	}
	parentLink := filepath.Join(directory, "parent-link")
	if err := os.Symlink(directory, parentLink); err != nil {
		t.Fatal(err)
	}
	parentEntry := productionBoundEntryForPath(t, target, filepath.Join(parentLink, "target"), now)
	if _, err := binder.openEntry(parentEntry, now); err == nil {
		t.Fatal("symlink inventory ancestor accepted")
	}
	retained, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	magicPath := fmt.Sprintf("/proc/self/fd/%d", retained.Fd())
	magicEntry := productionBoundEntryForPath(t, target, magicPath, now)
	if _, err := binder.openEntry(magicEntry, now); err == nil {
		t.Fatal("magic-link inventory path accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := binder.validate(ctx); err == nil {
		t.Fatal("canceled production inventory validation accepted")
	}
}

func TestProductionInventoryBinderRejectsWritableAndReplacedAncestor(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned production inventory test requires root")
	}
	directory := productionInventoryTestDir(t)
	writable := filepath.Join(directory, "writable")
	if err := os.Mkdir(writable, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatal(err)
	}
	writableArtifact := filepath.Join(writable, "artifact")
	if err := os.WriteFile(writableArtifact, []byte("bound artifact"), 0o400); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	binder, err := openProductionInventoryBinder()
	if err != nil {
		t.Fatal(err)
	}
	defer binder.close()
	writableEntry := productionBoundEntryForPath(t, writableArtifact, writableArtifact, now)
	if _, err := binder.openEntry(writableEntry, now); err == nil {
		t.Fatal("group/world-writable ancestor was accepted")
	}

	trusted := filepath.Join(directory, "trusted")
	if err := os.Mkdir(trusted, 0o700); err != nil {
		t.Fatal(err)
	}
	trustedArtifact := filepath.Join(trusted, "artifact")
	if err := os.WriteFile(trustedArtifact, []byte("bound artifact"), 0o400); err != nil {
		t.Fatal(err)
	}
	entry := productionBoundEntryForPath(t, trustedArtifact, trustedArtifact, now)
	opened, err := binder.openEntry(entry, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	replaced := trusted + ".old"
	if err := os.Rename(trusted, replaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(trusted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(replaced, "artifact"), filepath.Join(trusted, "artifact")); err != nil {
		t.Fatal(err)
	}
	if err := binder.rebind(context.Background(), entry, time.Now().UTC()); err == nil {
		t.Fatal("replacement ancestor with the same leaf inode was accepted")
	}
}

func productionInventoryTestDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/root", "ca42-production-inventory-test-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove production inventory fixture: %v", err)
		}
	})
	return directory
}

func productionBoundEntryForPath(t *testing.T, identityPath, boundPath string, now time.Time) BoundEntry {
	t.Helper()
	file, err := os.Open(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stat, mountID, err := fdIdentity(file)
	if err != nil {
		t.Fatal(err)
	}
	entry := Entry{Ordinal: 1, Scope: "attempt", Role: "fixture", Kind: "data", Path: filepath.ToSlash(boundPath),
		Mode: fmt.Sprintf("%04o", stat.Mode&0o7777), UID: uint64(stat.Uid), GID: uint64(stat.Gid),
		NLink: uint64(stat.Nlink), Size: uint64(stat.Size), Device: uint64(stat.Dev), Inode: stat.Ino, MountID: mountID,
		ContentSHA256: strings.Repeat("a", 64), VeritySHA256: strings.Repeat("b", 64)}
	descriptorSHA := sha256.Sum256([]byte("production-binder-descriptor"))
	planSHA := sha256.Sum256([]byte("production-binder-plan"))
	result := BoundEntry{entry: entry, descriptorSHA: descriptorSHA, planSHA: planSHA,
		planNotBefore: now.Add(-time.Minute), planNotAfter: now.Add(time.Minute), index: 0, bound: true}
	result.seal = sealBoundEntry(entry, 0, descriptorSHA, planSHA, result.planNotBefore, result.planNotAfter)
	return result
}
