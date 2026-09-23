//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
	"golang.org/x/sys/unix"
)

type v3ControlFixture struct {
	root, attempt, attemptID string
}

func newV3ControlFixture(t *testing.T) v3ControlFixture {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("V3 control bundle ownership contract requires root")
	}
	root := t.TempDir()
	if err := os.Chown(root, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt-control-v3"
	attempt := filepath.Join(root, attemptID)
	if err := os.Mkdir(attempt, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(attempt, 0, 0); err != nil {
		t.Fatal(err)
	}
	for _, spec := range ca42controlv3.Entries() {
		writeV3ControlEntry(t, attempt, spec, []byte("pandora-"+spec.Role+"\n"))
	}
	return v3ControlFixture{root: root, attempt: attempt, attemptID: attemptID}
}

func writeV3ControlEntry(t *testing.T, attempt string, spec ca42controlv3.Entry, content []byte) {
	t.Helper()
	path := filepath.Join(attempt, spec.Name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, os.FileMode(spec.Mode)); err != nil {
		t.Fatal(err)
	}
}

func (fixture v3ControlFixture) ops() v3ControlBundleOps {
	return v3ControlBundleOps{openRoot: func() (*os.File, error) { return os.Open(fixture.root) }}
}

func TestV3ControlBundleRetainsRevalidatesAndClosesSharedState(t *testing.T) {
	fixture := newV3ControlFixture(t)
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	if bundle.state.rootMountID == 0 || bundle.state.attemptMountID != bundle.state.rootMountID ||
		bundle.state.mountNSFD < 3 || bundle.state.userNSFD < 3 {
		t.Fatal("V3 control directory or namespace binding missing")
	}
	fds := []int{int(bundle.state.root.Fd()), int(bundle.state.attempt.Fd()), bundle.state.mountNSFD, bundle.state.userNSFD}
	for index, entry := range bundle.state.entries {
		if entry == nil || entry.spec != ca42controlv3.Entries()[index] || entry.file.Fd() < 3 ||
			entry.mountID != bundle.state.attemptMountID {
			t.Fatalf("retained entry %d invalid", index)
		}
		fds = append(fds, int(entry.file.Fd()))
	}
	if err := bundle.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	shallow := *bundle
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := shallow.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleUnavailable) {
		t.Fatalf("shallow copy revived closed bundle: %v", err)
	}
	if err := shallow.Close(); err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Fatalf("owned descriptor %d remained open: %v", fd, err)
		}
	}
}

func TestV3ControlBundleRejectsNonExactOrInvalidEntries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, v3ControlFixture)
	}{
		{name: "missing", mutate: func(t *testing.T, fixture v3ControlFixture) {
			if err := os.Remove(filepath.Join(fixture.attempt, ca42controlv3.ReleaseManifestName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "extra", mutate: func(t *testing.T, fixture v3ControlFixture) {
			writeV3ControlEntry(t, fixture.attempt, ca42controlv3.Entry{Name: "unexpected", Mode: 0o400}, []byte("extra"))
		}},
		{name: "wrong-mode", mutate: func(t *testing.T, fixture v3ControlFixture) {
			if err := os.Chmod(filepath.Join(fixture.attempt, ca42controlv3.AttestationCoreName), 0o400); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, fixture v3ControlFixture) {
			source := filepath.Join(fixture.attempt, ca42controlv3.ExpectedName)
			if err := os.Link(source, filepath.Join(fixture.root, "expected-hardlink")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, fixture v3ControlFixture) {
			path := filepath.Join(fixture.attempt, ca42controlv3.ExpectedName)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(ca42controlv3.ReleaseManifestName, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fifo", mutate: func(t *testing.T, fixture v3ControlFixture) {
			path := filepath.Join(fixture.attempt, ca42controlv3.ExpectedName)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(path, 0o400); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "non-root-group", mutate: func(t *testing.T, fixture v3ControlFixture) {
			if err := os.Chown(filepath.Join(fixture.attempt, ca42controlv3.ExpectedName), 0, 1); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newV3ControlFixture(t)
			test.mutate(t, fixture)
			bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
			if err == nil || bundle != nil {
				if bundle != nil {
					_ = bundle.Close()
				}
				t.Fatal("invalid V3 control layout was accepted")
			}
			assertNoV3ControlFDTargets(t, fixture.root)
		})
	}
}

func TestV3ControlBundleCanonicalLeafReplacementPoisons(t *testing.T) {
	fixture := newV3ControlFixture(t)
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	spec := ca42controlv3.Entries()[0]
	path := filepath.Join(fixture.attempt, spec.Name)
	old := path + ".old"
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	writeV3ControlEntry(t, fixture.attempt, spec, content)
	if err := bundle.Revalidate(context.Background()); err == nil {
		t.Fatal("canonical leaf replacement was accepted")
	}
	if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleUnavailable) {
		t.Fatalf("poisoned bundle revived: %v", err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestV3ControlBundleCanonicalDirectoryReplacementPoisons(t *testing.T) {
	for _, replaceRoot := range []bool{false, true} {
		name := map[bool]string{false: "attempt", true: "root"}[replaceRoot]
		t.Run(name, func(t *testing.T) {
			fixture := newV3ControlFixture(t)
			bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
			if err != nil {
				t.Fatal(err)
			}
			if replaceRoot {
				oldRoot := fixture.root + ".old"
				if err := os.Rename(fixture.root, oldRoot); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(oldRoot) })
				if err := os.Mkdir(fixture.root, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chown(fixture.root, 0, 0); err != nil {
					t.Fatal(err)
				}
				newAttempt := filepath.Join(fixture.root, fixture.attemptID)
				if err := os.Mkdir(newAttempt, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, spec := range ca42controlv3.Entries() {
					writeV3ControlEntry(t, newAttempt, spec, []byte("pandora-"+spec.Role+"\n"))
				}
			} else {
				oldAttempt := fixture.attempt + ".old"
				if err := os.Rename(fixture.attempt, oldAttempt); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(fixture.attempt, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, spec := range ca42controlv3.Entries() {
					writeV3ControlEntry(t, fixture.attempt, spec, []byte("pandora-"+spec.Role+"\n"))
				}
			}
			if err := bundle.Revalidate(context.Background()); err == nil {
				t.Fatal("canonical directory replacement was accepted")
			}
			if err := bundle.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestV3ControlBundleSiblingAttemptPublicationDoesNotPoison(t *testing.T) {
	fixture := newV3ControlFixture(t)
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(fixture.root, "attempt-sibling-v3")
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(sibling, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Revalidate(context.Background()); err != nil {
		t.Fatalf("unrelated sibling attempt poisoned retained bundle: %v", err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestV3ControlBundleAcquisitionCancellationRollsBack(t *testing.T) {
	stages := []struct {
		stage   string
		ordinal int
	}{
		{stage: "namespaces"}, {stage: "root"}, {stage: "attempt"},
		{stage: "entry", ordinal: 1}, {stage: "entry", ordinal: 2}, {stage: "entry", ordinal: 3},
		{stage: "entry", ordinal: 4}, {stage: "entry", ordinal: 5}, {stage: "entry", ordinal: 6},
		{stage: "entry", ordinal: 7}, {stage: "before-publish"},
	}
	for _, target := range stages {
		name := target.stage
		if target.ordinal != 0 {
			name += string(rune('0' + target.ordinal))
		}
		t.Run(name, func(t *testing.T) {
			fixture := newV3ControlFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			beforeNamespaces := countV3ControlNamespaceFDs(t)
			ops := fixture.ops()
			ops.afterAcquire = func(_ context.Context, stage string, ordinal int) error {
				if stage == target.stage && ordinal == target.ordinal {
					cancel()
					return ctx.Err()
				}
				return nil
			}
			if target.stage == "before-publish" {
				ops.beforePublish = func(context.Context) error { cancel(); return ctx.Err() }
			}
			bundle, err := openV3ControlBundleWithOps(ctx, fixture.attemptID, ops)
			if bundle != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled acquisition published a bundle: bundle=%v err=%v", bundle, err)
			}
			assertNoV3ControlFDTargets(t, fixture.root)
			if after := countV3ControlNamespaceFDs(t); after != beforeNamespaces {
				t.Fatalf("namespace descriptors leaked: before=%d after=%d", beforeNamespaces, after)
			}
		})
	}
}

func TestV3ControlBundleBeforePublishReplacementIsRejected(t *testing.T) {
	fixture := newV3ControlFixture(t)
	ops := fixture.ops()
	ops.beforePublish = func(context.Context) error {
		spec := ca42controlv3.Entries()[0]
		path := filepath.Join(fixture.attempt, spec.Name)
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.Rename(path, path+".old"); err != nil {
			return err
		}
		writeV3ControlEntry(t, fixture.attempt, spec, content)
		return nil
	}
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, ops)
	if err == nil || bundle != nil {
		if bundle != nil {
			_ = bundle.Close()
		}
		t.Fatal("before-publish replacement escaped final binding")
	}
	assertNoV3ControlFDTargets(t, fixture.root)
}

func TestV3ControlBundleCloseCancelsActiveRevalidation(t *testing.T) {
	fixture := newV3ControlFixture(t)
	var armed atomic.Bool
	entered := make(chan struct{})
	ops := fixture.ops()
	ops.beforeEntryHash = func(ctx context.Context, index int) error {
		if index == 0 && armed.Load() {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, ops)
	if err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	verifyDone := make(chan error, 1)
	go func() { verifyDone <- bundle.Revalidate(context.Background()) }()
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- bundle.Close() }()
	if err := <-verifyDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("active revalidation was not canceled: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestV3ControlBundleConcurrentRevalidateIsBusyWithoutPoison(t *testing.T) {
	fixture := newV3ControlFixture(t)
	var armed atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	ops := fixture.ops()
	ops.beforeEntryHash = func(ctx context.Context, index int) error {
		if index == 0 && armed.Load() {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, ops)
	if err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	done := make(chan error, 1)
	go func() { done <- bundle.Revalidate(context.Background()) }()
	<-entered
	if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleBusy) {
		t.Fatalf("second revalidation was not busy: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	armed.Store(false)
	if err := bundle.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertNoV3ControlFDTargets(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Clean(root) + string(os.PathSeparator)
	for _, entry := range entries {
		target, readErr := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if readErr != nil {
			continue
		}
		target = strings.TrimSuffix(target, " (deleted)")
		if strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), root) {
			t.Fatalf("V3 control descriptor leaked: fd=%s target=%s", entry.Name(), target)
		}
	}
}

func countV3ControlNamespaceFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		target, readErr := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if readErr == nil && (strings.HasPrefix(target, "mnt:[") || strings.HasPrefix(target, "user:[")) {
			count++
		}
	}
	return count
}
