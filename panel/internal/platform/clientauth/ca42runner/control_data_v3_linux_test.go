//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
)

func TestV3ControlDataScopesSixExactRolesAndZeroesEscape(t *testing.T) {
	fixture := newV3ControlFixture(t)
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	var escaped *v3ControlData
	err = withV3ControlDataFromRetained(context.Background(), bundle, func(_ context.Context, data *v3ControlData) error {
		escaped = data
		checks := []struct {
			role  string
			datum v3ControlDatum
		}{
			{ca42controlv3.ReleaseManifestRole, data.release},
			{ca42controlv3.ExecutionPlanRole, data.plan},
			{ca42controlv3.TrustCapsuleRole, data.capsule},
			{ca42controlv3.AttestationRole, data.attestation},
			{ca42controlv3.ExpectedRole, data.expected},
			{ca42controlv3.ArtifactStorageDescriptorRole, data.storage},
		}
		for _, check := range checks {
			if string(check.datum.bytes) != "pandora-"+check.role+"\n" || check.datum.digest == ([32]byte{}) {
				t.Fatalf("control role %q data mismatch", check.role)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if escaped == nil || escaped.release.bytes != nil || escaped.plan.bytes != nil || escaped.capsule.bytes != nil ||
		escaped.attestation.bytes != nil || escaped.expected.bytes != nil || escaped.storage.bytes != nil {
		t.Fatal("escaped control data was not zeroed and revoked")
	}
	if err := bundle.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestV3ControlDataOperationFailureClosesBundle(t *testing.T) {
	fixture := newV3ControlFixture(t)
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("parser rejected graph")
	err = withV3ControlDataFromRetained(context.Background(), bundle, func(context.Context, *v3ControlData) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("parser failure lost: %v", err)
	}
	if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleUnavailable) {
		t.Fatalf("parser failure left control bundle usable: %v", err)
	}
}

func TestV3ControlDataZeroesOwnedBuffersAfterHeaderDetachment(t *testing.T) {
	fixture := newV3ControlFixture(t)
	bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	var escaped []byte
	if err := withV3ControlDataFromRetained(context.Background(), bundle, func(_ context.Context, data *v3ControlData) error {
		escaped = data.release.bytes
		data.release.bytes = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, value := range escaped {
		if value != 0 {
			t.Fatal("detached control buffer was not zeroed")
		}
	}
}

func TestV3ControlDataPanicAndGoexitCloseBundle(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		fixture := newV3ControlFixture(t)
		bundle, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("parser panic swallowed")
				}
			}()
			_ = withV3ControlDataFromRetained(context.Background(), bundle, func(context.Context, *v3ControlData) error {
				panic("parser panic")
			})
		}()
		if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleUnavailable) {
			t.Fatalf("parser panic left bundle usable: %v", err)
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
			_ = withV3ControlDataFromRetained(context.Background(), bundle, func(context.Context, *v3ControlData) error {
				runtime.Goexit()
				return nil
			})
		}()
		<-done
		if err := bundle.Revalidate(context.Background()); !errors.Is(err, errV3ControlBundleUnavailable) {
			t.Fatalf("parser Goexit left bundle usable: %v", err)
		}
	})
}
