//go:build ca42e2e && linux && (amd64 || arm64)

package ca42runner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
	"github.com/aegispanel/aegis/internal/platform/releasejournal"
	"golang.org/x/sys/unix"
)

const (
	ca42E2EMigrationPath = "/root/00042_client_auth_expand.sql"
	ca42E2EAttemptID     = "attempt-ca42-e2e-v3"
	ca42E2EReleaseID     = "release-ca42-e2e-v3"
	ca42E2EReleaseRunID  = "run-ca42-e2e-v3"
)

var ca42E2EFixedRoots = [...]string{
	TrustRootPath,
	LedgerRootPath,
	ca42controlv3.RootPath,
	"/var/lib/pandora/release-journal-v3",
	"/run/pandora/ca42",
}

type ca42E2EProvisionedFixture struct {
	descriptor ca42authority.Descriptor
	graph      ca42artifactsv2.CA42E2EGraphResult
}

type ca42E2EFDIdentity struct {
	target                     string
	device, inode              uint64
	mode                       uint32
	openFlags, descriptorFlags int
}

func requireCA42E2EIsolation(t *testing.T) {
	t.Helper()
	if os.Getenv("PANDORA_CA42_E2E_ISOLATED") != "1" {
		t.Fatal("CA42 production composer E2E requires the isolated namespace harness")
	}
	if unix.Geteuid() != 0 {
		t.Fatal("CA42 production composer E2E is not running as root")
	}
	for _, namespace := range []string{"mnt", "pid", "user"} {
		processOne, err := os.Readlink("/proc/1/ns/" + namespace)
		if err != nil {
			t.Fatal(err)
		}
		current, err := os.Readlink("/proc/thread-self/ns/" + namespace)
		if err != nil {
			t.Fatal(err)
		}
		if processOne != current {
			t.Fatalf("CA42 E2E %s namespace is not isolated with PID 1: pid1=%q current=%q", namespace, processOne, current)
		}
	}
	for _, fixedRoot := range ca42E2EFixedRoots {
		if _, err := os.Lstat(fixedRoot); !os.IsNotExist(err) {
			t.Fatalf("CA42 E2E fixed root is not initially absent: path=%q err=%v", fixedRoot, err)
		}
	}
	if err := requireCA42E2EAttestedNamespace(); err != nil {
		t.Fatal(err)
	}
	roots, err := CompiledRootKeyset()
	if err != nil || roots.ID != "pandora-ca42-e2e-roots-v1" {
		t.Fatalf("CA42 E2E public roots are unavailable: roots=%+v err=%v", roots, err)
	}
}

func TestOpenProductionV3VerificationSessionIsolatedProvisionedRoot(t *testing.T) {
	requireCA42E2EIsolation(t)
	fixture := provisionCA42E2EProductionRoot(t)
	fdBaseline := captureCA42E2EFDs(t)
	ctx := context.Background()
	session, err := OpenProductionV3VerificationSession(ctx, ca42E2EAttemptID)
	if err != nil || session == nil {
		t.Fatalf("production V3 opener rejected canonical isolated fixture: session=%v err=%v", session, err)
	}
	if err := session.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent session close failed: %v", err)
	}
	assertCA42E2EFDsMatch(t, fdBaseline)
	assertCA42E2ELedger(t, fixture)
}

func TestOpenProductionV3VerificationSessionJournalFailureRollsBackOwnership(t *testing.T) {
	requireCA42E2EIsolation(t)
	fixture := provisionCA42E2EProductionRoot(t)
	if err := releasejournal.CleanupCA42E2EJournal(); err != nil {
		t.Fatal(err)
	}
	fdBaseline := captureCA42E2EFDs(t)
	ctx := context.Background()
	for attempt := 0; attempt < 2; attempt++ {
		session, err := OpenProductionV3VerificationSession(ctx, ca42E2EAttemptID)
		if session != nil || err == nil {
			if session != nil {
				_ = session.Close()
			}
			t.Fatalf("missing Journal unexpectedly opened production session on attempt %d: %v", attempt+1, err)
		}
		assertCA42E2EFDsMatch(t, fdBaseline)
		assertCA42E2ELedger(t, fixture)
	}
}

func provisionCA42E2EProductionRoot(t *testing.T) ca42E2EProvisionedFixture {
	t.Helper()
	mountCA42E2ERuntimeRoots(t)
	now := time.Now().UTC().Truncate(time.Second)
	migration := readCA42E2EMigration(t)
	raw, err := ca42artifactsv2.PrepareCA42E2ERawInventory(ca42artifactsv2.CA42E2ERawInput{
		Now: now, Label: "native-v3", ReleaseID: ca42E2EReleaseID, ReleaseRunID: ca42E2EReleaseRunID,
		AttemptID: ca42E2EAttemptID, FrozenMigration42: migration,
	})
	if err != nil {
		t.Fatal(err)
	}
	controlRoot := filepath.Join(ca42controlv3.RootPath, ca42E2EAttemptID)
	requireCA42E2EDirectory(t, controlRoot, ca42controlv3.DirectoryMode)
	coreEntry, err := ca42controlv3.EntryForRole(ca42controlv3.AttestationCoreRole)
	if err != nil {
		t.Fatal(err)
	}
	writeCA42E2EFile(t, filepath.Join(controlRoot, coreEntry.Name), raw.AttestationCore, coreEntry.Mode)

	runtimeSpecs := make([]ca42storage.CA42E2ERuntimeFile, len(raw.Runtime))
	for index := range raw.Runtime {
		runtimeSpecs[index] = raw.Runtime[index].Spec
	}
	layout, err := ca42storage.CA42E2ERequiredInventoryFiles(ca42E2EAttemptID, runtime.GOARCH, runtimeSpecs)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range layout {
		if file.Scope == "system" {
			continue
		}
		data, ok := raw.Files[file.Path]
		if !ok {
			t.Fatalf("CA42 E2E raw inventory omitted %s", file.Path)
		}
		writeCA42E2EFile(t, file.Path, data, file.Mode)
	}
	for index := range raw.Runtime {
		bindCA42E2ERuntime(t, raw.Runtime[index], index)
	}
	// Stabilize every /var/lib/pandora ancestor link count before computing
	// absolute path-chain digests. The tagged Journal producer adopts only the
	// exact empty root below; production code receives no such bypass.
	requireCA42E2EDirectory(t, LedgerRootPath, 0o700)
	requireCA42E2EDirectory(t, "/var/lib/pandora/release-journal-v3", 0o700)

	pathtrust := ca42E2ELayoutPath(t, layout, "pathtrust_binary")
	measurements := ca42artifactsv2.CA42E2EMeasurements{}
	measurements.PathtrustDevice, measurements.PathtrustChainSHA256 = measureCA42E2EPath(t, pathtrust)
	measurements.AttestationCoreDevice, measurements.AttestationCoreChain = measureCA42E2EPath(t, filepath.Join(controlRoot, coreEntry.Name))
	measurements.BashDevice, measurements.BashChainSHA256 = measureCA42E2EPath(t, "/usr/bin/bash")
	measurements.DockerDevice, measurements.DockerChainSHA256 = measureCA42E2EPath(t, "/usr/bin/docker")
	storage, err := ca42storage.BuildCA42E2EStorageDescriptor(context.Background(), ca42storage.CA42E2EStorageInput{
		ReleaseID: ca42E2EReleaseID, ReleaseRunID: ca42E2EReleaseRunID, AttemptID: ca42E2EAttemptID,
		Architecture: runtime.GOARCH, RuntimeFiles: runtimeSpecs,
	})
	if err != nil || storage.Tainted {
		t.Fatalf("CA42 E2E storage build failed or tainted disposable media: tainted=%v attempts=%d verified=%d err=%v",
			storage.Tainted, storage.SealAttempts, storage.VerifiedSealedCount, err)
	}

	hostIdentity := []byte("pandora-ca42-e2e-host-identity-v1\n")
	hostSHA := sha256.Sum256(hostIdentity)
	runnerSHA := hashCA42E2EFile(t, "/proc/self/exe")
	roots, err := CompiledRootKeyset()
	if err != nil {
		t.Fatal(err)
	}
	private := ca42E2ERootPrivate(t, roots)
	placeholder := sha256.Sum256([]byte("ca42-e2e-provisional-release:" + ca42E2EAttemptID))
	provisionalRaw, provisionalDescriptor, err := ca42authority.BuildCA42E2EAuthorityDescriptor(ca42authority.CA42E2EAuthorityInput{
		Roots: roots, RootPrivate: private, ReleasePublic: raw.ReleasePublic, Architecture: runtime.GOARCH,
		HostIdentity: hostSHA, RootRunner: runnerSHA, ReleaseManifest: placeholder, ReleaseID: ca42E2EReleaseID,
		ReleaseRunID: ca42E2EReleaseRunID, AttemptID: ca42E2EAttemptID, Now: now,
	})
	if err != nil || len(provisionalRaw) == 0 {
		t.Fatal(err)
	}
	provisionalBinding := ca42E2EAuthorityBinding(provisionalDescriptor)
	pre, err := ca42artifactsv2.BuildCA42E2EPreJournalGraph(raw, storage, measurements, provisionalBinding)
	if err != nil {
		t.Fatal(err)
	}
	controllerSHA := hex.EncodeToString(runnerSHA[:])
	journal, err := releasejournal.ProvisionCA42E2ELayoutSwitched(releasejournal.CA42E2EJournalInput{
		JournalID: releasejournal.SHA256Bytes([]byte("ca42-e2e-journal:" + ca42E2EAttemptID)), AttemptID: ca42E2EAttemptID,
		ReleaseContractCoreSHA256: pre.ReleaseContractCoreSHA256, ControllerSHA256: controllerSHA, CreatedAtEpoch: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := ca42artifactsv2.FinalizeCA42E2EGraph(pre, journal.HeadSHA256(), journal.ManifestSHA256())
	if err != nil {
		t.Fatal(err)
	}
	finalAuthority, finalDescriptor, err := ca42authority.BuildCA42E2EAuthorityDescriptor(ca42authority.CA42E2EAuthorityInput{
		Roots: roots, RootPrivate: private, ReleasePublic: raw.ReleasePublic, Architecture: runtime.GOARCH,
		HostIdentity: hostSHA, RootRunner: runnerSHA, ReleaseManifest: graph.ReleaseManifestSHA256, ReleaseID: ca42E2EReleaseID,
		ReleaseRunID: ca42E2EReleaseRunID, AttemptID: ca42E2EAttemptID, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if provisionalDescriptor.BindingSHA256 != finalDescriptor.BindingSHA256 || bytes.Equal(provisionalRaw, finalAuthority) {
		t.Fatal("CA42 E2E authority provisional/final binding or signature invariant failed")
	}
	for _, datum := range graph.Control {
		path := filepath.Join(controlRoot, datum.Entry.Name)
		if datum.Entry.Role == ca42controlv3.AttestationCoreRole {
			if digest := hashCA42E2EFile(t, path); digest != sha256.Sum256(datum.Data) {
				t.Fatal("CA42 E2E attestation core changed before final publication")
			}
			continue
		}
		writeCA42E2EFile(t, path, datum.Data, datum.Entry.Mode)
	}
	requireCA42E2EDirectory(t, TrustRootPath, 0o700)
	writeCA42E2EFile(t, filepath.Join(TrustRootPath, HostIdentityPath), hostIdentity, 0o400)
	writeCA42E2EFile(t, filepath.Join(TrustRootPath, AuthorityPath), finalAuthority, 0o400)
	return ca42E2EProvisionedFixture{descriptor: finalDescriptor, graph: graph}
}

func mountCA42E2ERuntimeRoots(t *testing.T) {
	t.Helper()
	if err := unix.Mount("tmpfs", "/usr", "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "mode=0755"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/usr/bin", "/usr/lib", "/usr/lib64"} {
		requireCA42E2EDirectory(t, path, 0o755)
	}
	for _, path := range []string{"/lib", "/lib64"} {
		var filesystem unix.Statfs_t
		if err := unix.Statfs(path, &filesystem); err == nil && filesystem.Type == unix.TMPFS_MAGIC {
			continue
		}
		if err := unix.Mount("tmpfs", path, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "mode=0755"); err != nil {
			t.Fatalf("CA42 E2E runtime root mount failed for %s: %v", path, err)
		}
	}
}

func readCA42E2EMigration(t *testing.T) []byte {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Lstat(ca42E2EMigrationPath, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Mode&0o7777 != 0o400 || stat.Size <= 0 || stat.Size > 1<<20 {
		t.Fatalf("CA42 E2E frozen migration identity invalid: %+v err=%v", stat, err)
	}
	data, err := os.ReadFile(ca42E2EMigrationPath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func requireCA42E2EDirectory(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func writeCA42E2EFile(t *testing.T, path string, data []byte, mode uint32) {
	t.Helper()
	requireCA42E2EDirectory(t, filepath.Dir(path), 0o700)
	if len(data) == 0 {
		t.Fatalf("refused empty CA42 E2E file: %s", path)
	}
	if err := os.WriteFile(path, data, os.FileMode(mode)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, os.FileMode(mode)); err != nil {
		t.Fatal(err)
	}
}

func bindCA42E2ERuntime(t *testing.T, runtimeFile ca42artifactsv2.CA42E2ERuntimeContent, ordinal int) {
	t.Helper()
	source := fmt.Sprintf("/var/lib/pandora/ca42-e2e-runtime/%03d-%s", ordinal+1, runtimeFile.Spec.Role)
	writeCA42E2EFile(t, source, runtimeFile.Data, runtimeFile.Spec.Mode)
	requireCA42E2EDirectory(t, filepath.Dir(runtimeFile.Spec.Path), 0o755)
	writeCA42E2EFile(t, runtimeFile.Spec.Path, []byte("bind-target\n"), runtimeFile.Spec.Mode)
	if err := unix.Mount(source, runtimeFile.Spec.Path, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
}

func ca42E2ELayoutPath(t *testing.T, layout []ca42storage.CA42E2ERequiredFile, role string) string {
	t.Helper()
	for _, file := range layout {
		if file.Role == role {
			return file.Path
		}
	}
	t.Fatalf("CA42 E2E layout role missing: %s", role)
	return ""
}

func measureCA42E2EPath(t *testing.T, path string) (uint64, string) {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		t.Fatal("CA42 E2E retained measurement file unavailable")
	}
	defer file.Close()
	identity, err := retainedStat(fd)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := absoluteArtifactChainDigest(context.Background(), file, identity, path)
	if err != nil {
		t.Fatal(err)
	}
	return identity.dev, hex.EncodeToString(digest[:])
}

func hashCA42E2EFile(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		t.Fatal(err)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest
}

func ca42E2ERootPrivate(t *testing.T, roots ca42authority.RootKeyset) [ca42authority.RequiredRootCount]ed25519.PrivateKey {
	t.Helper()
	var private [ca42authority.RequiredRootCount]ed25519.PrivateKey
	for index := range private {
		private[index] = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(index + 21)}, ed25519.SeedSize))
		if !private[index].Public().(ed25519.PublicKey).Equal(roots.Keys[index].PublicKey) {
			t.Fatalf("CA42 E2E compiled root mismatch at %d", index)
		}
	}
	return private
}

func ca42E2EAuthorityBinding(descriptor ca42authority.Descriptor) ca42releasev3.AuthorityBinding {
	return ca42releasev3.AuthorityBinding{
		PublicKey: append(ed25519.PublicKey(nil), descriptor.ReleaseSignerKey...), ManifestSHA256: descriptor.ReleaseManifestSHA256,
		SignerSHA256: descriptor.ReleaseSignerSHA256, Epoch: descriptor.AuthorityEpoch, Sequence: descriptor.AuthoritySequence,
		BindingSHA256: descriptor.BindingSHA256, ReleaseSignerKeyID: descriptor.ReleaseSignerKeyID,
	}
}

func assertCA42E2ELedger(t *testing.T, fixture ca42E2EProvisionedFixture) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(LedgerRootPath, LedgerPath))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ca42authority.ParseLedger(data)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.DescriptorSHA256 != fixture.descriptor.SHA256 || ledger.LastManifestSHA256 != fixture.graph.ReleaseManifestSHA256 ||
		ledger.LastAttemptID != ca42E2EAttemptID || ledger.AuthorityEpoch != 1 || ledger.AuthoritySequence != 1 {
		t.Fatal("CA42 E2E durable ledger does not match final authority")
	}
}

func captureCA42E2EFDs(t *testing.T) map[string]ca42E2EFDIdentity {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]ca42E2EFDIdentity, len(entries))
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); errors.Is(err, unix.EBADF) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		openFlags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil {
			t.Fatal(err)
		}
		descriptorFlags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = ca42E2EFDIdentity{target: target, device: uint64(stat.Dev), inode: stat.Ino,
			mode: stat.Mode, openFlags: openFlags, descriptorFlags: descriptorFlags}
	}
	return result
}

func assertCA42E2EFDsMatch(t *testing.T, expected map[string]ca42E2EFDIdentity) {
	t.Helper()
	actual := captureCA42E2EFDs(t)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("CA42 E2E descriptor ownership did not return to baseline: before=%v after=%v", expected, actual)
	}
}
