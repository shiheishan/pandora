//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
)

func TestV3ControlRoleReaderIsScopedAndRevalidated(t *testing.T) {
	fixture := newV3ControlFixture(t)
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()

	var escaped io.ReaderAt
	err = bundle.withRoleReaderAt(context.Background(), ca42controlv3.ReleaseManifestRole,
		func(_ context.Context, reader io.ReaderAt, size uint64, digest [sha256.Size]byte) error {
			escaped = reader
			content := make([]byte, int(size))
			if _, readErr := io.ReadFull(io.NewSectionReader(reader, 0, int64(size)), content); readErr != nil {
				return readErr
			}
			if string(content) != "pandora-"+ca42controlv3.ReleaseManifestRole+"\n" {
				return errors.New("unexpected retained role content")
			}
			if sha256.Sum256(content) != digest {
				return errors.New("retained role digest mismatch")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.ReadAt(make([]byte, 1), 0); !errors.Is(err, errV3ControlRoleReaderExpired) {
		t.Fatalf("escaped role reader remained usable: %v", err)
	}
	if err := bundle.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestV3ControlRoleReaderDeniesExecutableAndUnknownRoles(t *testing.T) {
	fixture := newV3ControlFixture(t)
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	operation := func(context.Context, io.ReaderAt, uint64, [sha256.Size]byte) error { return nil }
	for _, role := range []string{ca42controlv3.AttestationCoreRole, "unknown-v3-role"} {
		if err := bundle.withRoleReaderAt(context.Background(), role, operation); !errors.Is(err, errV3ControlBundleInvalid) {
			t.Fatalf("role %q was not denied: %v", role, err)
		}
	}
	if err := bundle.Revalidate(context.Background()); err != nil {
		t.Fatalf("denied role poisoned a valid bundle: %v", err)
	}
}

func TestV3ControlRoleReaderAllowsExactlySixFrozenDataRoles(t *testing.T) {
	fixture := newV3ControlFixture(t)
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	called := 0
	for _, spec := range ca42controlv3.Entries() {
		err := bundle.withRoleReaderAt(context.Background(), spec.Role,
			func(_ context.Context, reader io.ReaderAt, size uint64, digest [sha256.Size]byte) error {
				called++
				if reader == nil || size == 0 || size > spec.MaxBytes {
					return errors.New("invalid frozen role capability")
				}
				content := make([]byte, int(size))
				if _, err := io.ReadFull(io.NewSectionReader(reader, 0, int64(size)), content); err != nil {
					return err
				}
				if sha256.Sum256(content) != digest {
					return errors.New("retained frozen role digest mismatch")
				}
				return nil
			})
		if spec.Role == ca42controlv3.AttestationCoreRole {
			if !errors.Is(err, errV3ControlBundleInvalid) {
				t.Fatalf("attestation core was not denied: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("data role %q rejected: %v", spec.Role, err)
		}
	}
	if called != 6 {
		t.Fatalf("role callback count = %d, want 6", called)
	}
}

func TestV3ControlRoleReaderCancellationAndConcurrentCallsFailClosed(t *testing.T) {
	fixture := newV3ControlFixture(t)
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- bundle.withRoleReaderAt(ctx, ca42controlv3.ReleaseManifestRole,
			func(callbackContext context.Context, _ io.ReaderAt, _ uint64, _ [sha256.Size]byte) error {
				close(entered)
				if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleBusy) {
					return errors.Join(errors.New("concurrent revalidation was not busy"), err)
				}
				if err := bundle.withRoleReaderAt(context.Background(), ca42controlv3.ExpectedRole,
					func(context.Context, io.ReaderAt, uint64, [sha256.Size]byte) error { return nil }); !errors.Is(err, errV3ControlBundleBusy) {
					return errors.Join(errors.New("recursive role read was not busy"), err)
				}
				<-callbackContext.Done()
				return callbackContext.Err()
			})
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("active cancellation result = %v", err)
	}
	if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleUnavailable) {
		t.Fatalf("canceled role operation did not close bundle: %v", err)
	}
}

func TestV3ControlRoleReaderFailureAndDriftPoisonBundle(t *testing.T) {
	t.Run("callback-error", func(t *testing.T) {
		fixture := newV3ControlFixture(t)
		bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
		if err != nil {
			t.Fatal(err)
		}
		sentinel := errors.New("parser rejected role")
		err = bundle.withRoleReaderAt(context.Background(), ca42controlv3.ExecutionPlanRole,
			func(context.Context, io.ReaderAt, uint64, [sha256.Size]byte) error { return sentinel })
		if !errors.Is(err, sentinel) {
			t.Fatalf("callback error lost: %v", err)
		}
		if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleUnavailable) {
			t.Fatalf("failed role operation did not poison bundle: %v", err)
		}
	})

	t.Run("post-read-drift", func(t *testing.T) {
		fixture := newV3ControlFixture(t)
		bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
		if err != nil {
			t.Fatal(err)
		}
		spec, _ := ca42controlv3.EntryForRole(ca42controlv3.ExpectedRole)
		err = bundle.withRoleReaderAt(context.Background(), spec.Role,
			func(context.Context, io.ReaderAt, uint64, [sha256.Size]byte) error {
				path := filepath.Join(fixture.attempt, spec.Name)
				if writeErr := os.WriteFile(path, []byte("drifted-expected-role\n"), os.FileMode(spec.Mode)); writeErr != nil {
					return writeErr
				}
				return os.Chmod(path, os.FileMode(spec.Mode))
			})
		if err == nil {
			t.Fatal("post-read role drift was accepted")
		}
		if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleUnavailable) {
			t.Fatalf("drifted role did not poison bundle: %v", err)
		}
	})
}

func TestV3ControlRoleReaderPanicGoexitAndSynchronousClose(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		fixture := newV3ControlFixture(t)
		bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("role callback panic was swallowed")
				}
			}()
			_ = bundle.withRoleReaderAt(context.Background(), ca42controlv3.TrustCapsuleRole,
				func(context.Context, io.ReaderAt, uint64, [sha256.Size]byte) error { panic("parser panic") })
		}()
		if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleUnavailable) {
			t.Fatalf("panic did not close bundle: %v", err)
		}
	})

	t.Run("goexit", func(t *testing.T) {
		fixture := newV3ControlFixture(t)
		bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = bundle.withRoleReaderAt(context.Background(), ca42controlv3.AttestationRole,
				func(context.Context, io.ReaderAt, uint64, [sha256.Size]byte) error { runtime.Goexit(); return nil })
		}()
		<-done
		if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleUnavailable) {
			t.Fatalf("Goexit did not close bundle: %v", err)
		}
	})

	t.Run("synchronous-close", func(t *testing.T) {
		fixture := newV3ControlFixture(t)
		bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
		if err != nil {
			t.Fatal(err)
		}
		err = bundle.withRoleReaderAt(context.Background(), ca42controlv3.ArtifactStorageDescriptorRole,
			func(ctx context.Context, _ io.ReaderAt, _ uint64, _ [sha256.Size]byte) error {
				if closeErr := bundle.Close(); !errors.Is(closeErr, errV3ControlBundleBusy) {
					return errors.Join(errors.New("synchronous close did not report busy"), closeErr)
				}
				return ctx.Err()
			})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("synchronous close did not cancel role operation: %v", err)
		}
		if err := bundle.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
