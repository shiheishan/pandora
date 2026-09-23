//go:build !linux

package ca42artifactsv2

import (
	"context"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

func TestArtifactSetInventoryFailsClosedWithoutNativeLinuxFSVerity(t *testing.T) {
	fixture := newGraphFixture(t, "amd64", "a")
	set, err := New(fixture.inputs, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := set.RetainInventoryAt(context.Background(), nil, fixture.now)
	if lease != nil || !errors.Is(err, ca42storage.ErrFSVerityUnsupported) {
		t.Fatalf("non-Linux aggregate inventory got lease=%v err=%v", lease, err)
	}
}
