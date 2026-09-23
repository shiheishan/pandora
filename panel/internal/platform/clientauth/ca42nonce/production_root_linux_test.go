//go:build linux && (amd64 || arm64)

package ca42nonce

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nonceProductionRootFixture(t *testing.T) (string, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixture")
	}
	base := os.Getenv("PANDORA_CA42_NONCE_TEST_ROOT")
	if base == "" {
		base = "/root"
	}
	if !filepath.IsAbs(base) || filepath.Clean(base) != base || (base != "/root" && !strings.HasPrefix(base, "/root/")) {
		t.Fatalf("nonce test root must be canonical beneath /root: %q", base)
	}
	parent, err := os.MkdirTemp(base, "pandora-ca42-nonce-parent.")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	rootName := "nonce-root"
	if err := os.Mkdir(filepath.Join(parent, rootName), 0700); err != nil {
		t.Fatal(err)
	}
	return parent, rootName
}

func openProductionStoreFixture(t *testing.T) (*ProductionStore, string, string) {
	t.Helper()
	parent, rootName := nonceProductionRootFixture(t)
	capability, err := openNonceRootCapability(parent, rootName)
	if err != nil {
		t.Fatal(err)
	}
	store, err := capability.openStore()
	if err != nil {
		_ = capability.close()
		t.Fatal(err)
	}
	return &ProductionStore{lease: &productionStoreLease{}, capability: capability, store: store}, parent, rootName
}

func TestNonceProductionRootCapabilityStoreAndSharedClose(t *testing.T) {
	parent, rootName := nonceProductionRootFixture(t)
	capability, err := openNonceRootCapability(parent, rootName)
	if err != nil {
		t.Fatal(err)
	}
	store, err := capability.openStore()
	if err != nil {
		_ = capability.close()
		t.Fatal(err)
	}
	if competing, err := capability.openStore(); err == nil {
		_ = competing.close()
		t.Fatal("competing production nonce store opened")
	} else if !errors.Is(err, ErrStoreBusy) {
		t.Fatalf("production nonce contention was not classified: %v", err)
	}
	production := &ProductionStore{lease: &productionStoreLease{}, capability: capability, store: store}
	if err := production.validate(); err != nil {
		t.Fatal(err)
	}
	copyProduction := *production
	if err := production.Close(); err != nil {
		t.Fatal(err)
	}
	if err := copyProduction.validate(); err == nil {
		t.Fatal("shallow-copy production store remained authoritative after close")
	}
	if err := copyProduction.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNonceProductionRootCapabilityShallowCopySharesCloseState(t *testing.T) {
	parent, rootName := nonceProductionRootFixture(t)
	capability, err := openNonceRootCapability(parent, rootName)
	if err != nil {
		t.Fatal(err)
	}
	copyCapability := *capability
	if err := copyCapability.close(); err != nil {
		t.Fatal(err)
	}
	if err := capability.validate(); err == nil {
		t.Fatal("original nonce root capability remained authoritative after shallow-copy close")
	}
	if err := capability.close(); err != nil {
		t.Fatalf("shared nonce root close was not idempotent: %v", err)
	}
}

func TestProductionStoreCloseIsBusyUntilChildSessionCloses(t *testing.T) {
	parent, rootName := nonceProductionRootFixture(t)
	capability, err := openNonceRootCapability(parent, rootName)
	if err != nil {
		t.Fatal(err)
	}
	store, err := capability.openStore()
	if err != nil {
		_ = capability.close()
		t.Fatal(err)
	}
	production := &ProductionStore{lease: &productionStoreLease{}, capability: capability, store: store}
	session, _, err := store.reserve(nonceStoreFields("active"))
	if err != nil {
		_ = production.Close()
		t.Fatal(err)
	}
	if err := production.Close(); !errors.Is(err, ErrStoreBusy) {
		t.Fatalf("production store close released authority while child session was live: %v", err)
	}
	if err := production.validate(); err != nil {
		t.Fatalf("busy production close invalidated retryable authority: %v", err)
	}
	if competing, err := capability.openStore(); err == nil {
		_ = competing.close()
		t.Fatal("competing store opened while child session was live")
	} else if !errors.Is(err, ErrStoreBusy) {
		t.Fatalf("live child contention was not classified: %v", err)
	}
	if err := session.close(); err != nil {
		t.Fatal(err)
	}
	if err := production.Close(); err != nil {
		t.Fatalf("production store did not close after child session: %v", err)
	}
}

func TestProductionMutationReserveIsAtomicAndDoesNotLeakSession(t *testing.T) {
	production, _, _ := openProductionStoreFixture(t)
	defer production.Close()
	snapshot, recovered, err := production.reserveProduction(nonceStoreFields("production"))
	if err != nil || recovered || snapshot.State() != Reserved {
		t.Fatalf("production reserve recovered=%v snapshot=%+v err=%v", recovered, snapshot, err)
	}
	if production.store.lease.activeSessions != 0 {
		t.Fatalf("production mutation leaked child session count: %d", production.store.lease.activeSessions)
	}
	retry, recovered, err := production.reserveProduction(nonceStoreFields("production"))
	if err != nil || !recovered || retry.HeadSHA256() != snapshot.HeadSHA256() {
		t.Fatalf("production exact retry recovered=%v snapshot=%+v err=%v", recovered, retry, err)
	}
}

func TestProductionMutationPostValidationFailurePoisonsStore(t *testing.T) {
	production, parent, rootName := openProductionStoreFixture(t)
	original := filepath.Join(parent, rootName)
	displaced := original + ".old"
	t.Cleanup(func() { _ = os.RemoveAll(displaced) })
	production.store.hooks.afterDirectorySync = func(operation string) error {
		if operation != "root" {
			return nil
		}
		production.store.hooks.afterDirectorySync = nil
		if err := os.Rename(original, displaced); err != nil {
			return err
		}
		return os.Mkdir(original, 0700)
	}
	if _, _, err := production.reserveProduction(nonceStoreFields("poison")); !errors.Is(err, ErrMutationAmbiguous) {
		t.Fatalf("post-mutation canonical replacement was not ambiguous: %v", err)
	}
	if production.lease.poisoned == nil {
		t.Fatal("ambiguous production store was not poisoned")
	}
	if _, _, err := production.reserveProduction(nonceStoreFields("other")); !errors.Is(err, ErrMutationAmbiguous) {
		t.Fatalf("poisoned production store accepted another mutation: %v", err)
	}
	entries, err := os.ReadDir(original)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canonical replacement received mutation: entries=%v err=%v", entries, err)
	}
	if err := production.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNonceProductionRootCapabilityRejectsBasenameReplacement(t *testing.T) {
	parent, rootName := nonceProductionRootFixture(t)
	capability, err := openNonceRootCapability(parent, rootName)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.close()
	original := filepath.Join(parent, rootName)
	displaced := original + ".old"
	if err := os.Rename(original, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	if err := capability.validate(); err == nil {
		t.Fatal("nonce root basename replacement accepted")
	}
	if store, err := capability.openStore(); err == nil {
		_ = store.close()
		t.Fatal("nonce store opened after basename replacement")
	}
}

func TestNonceProductionRootCapabilityRejectsParentReplacement(t *testing.T) {
	parent, rootName := nonceProductionRootFixture(t)
	capability, err := openNonceRootCapability(parent, rootName)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.close()
	displaced := parent + ".old"
	t.Cleanup(func() { _ = os.RemoveAll(displaced) })
	if err := os.Rename(parent, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(parent, rootName), 0700); err != nil {
		t.Fatal(err)
	}
	if err := capability.validate(); err == nil {
		t.Fatal("nonce canonical parent replacement accepted")
	}
}
