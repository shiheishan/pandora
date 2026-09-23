//go:build !linux

package ca42storage

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestVerifyFDFailsClosedOutsideLinux(t *testing.T) {
	if err := VerifyFD(context.Background(), BoundEntry{}, nil, time.Now().UTC()); err == nil {
		t.Fatal("non-Linux fs-verity verification accepted")
	}
	if _, err := MeasureVerity(nil); err == nil {
		t.Fatal("non-Linux fs-verity measurement accepted")
	}
}

func TestRetainVerifiedFDFailsClosedOutsideLinux(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "ca42-nonlinux-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lease, err := RetainVerifiedFD(context.Background(), BoundEntry{}, file, time.Now().UTC())
	if lease != nil || !errors.Is(err, ErrFSVerityUnsupported) {
		t.Fatalf("non-Linux retained lease did not fail closed: lease=%v err=%v", lease, err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("non-Linux failure closed caller source: %v", err)
	}
}

func TestRetainInventoryFailsClosedOutsideLinux(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "ca42-inventory-nonlinux-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lease, err := RetainInventory(context.Background(), BoundDescriptor{}, []*os.File{file})
	if lease != nil || !errors.Is(err, ErrFSVerityUnsupported) {
		t.Fatalf("non-Linux inventory lease did not fail closed: lease=%v err=%v", lease, err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("non-Linux inventory failure closed caller source: %v", err)
	}
}
