//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42release"
	"github.com/aegispanel/aegis/internal/platform/releasejournal"
)

func TestVerificationSessionBundleDoesNotAliasMutableAuthorityKey(t *testing.T) {
	originalKey := ed25519.PublicKey(bytes.Repeat([]byte{0x5a}, ed25519.PublicKeySize))
	session := &VerificationSession{state: &verificationSessionState{bundle: VerifiedBundle{
		Authority: ca42authority.Descriptor{ReleaseSignerKey: originalKey},
	}}}

	first, err := session.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	first.Authority.ReleaseSignerKey[0] ^= 0xff
	second, err := session.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	if second.Authority.ReleaseSignerKey[0] != 0x5a || session.state.bundle.Authority.ReleaseSignerKey[0] != 0x5a {
		t.Fatal("Bundle exposed the session's mutable release signer key")
	}
}

func TestVerificationSessionShallowCopySharesCloseStateAndDoesNotCloseReusedFD(t *testing.T) {
	retained, err := os.CreateTemp(t.TempDir(), "retained-")
	if err != nil {
		t.Fatal(err)
	}
	oldFD := retained.Fd()
	session := &VerificationSession{state: &verificationSessionState{retained: []*os.File{retained}}}
	copySession := *session
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	reused, err := os.OpenFile(t.TempDir()+"/reused", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer reused.Close()
	if reused.Fd() != oldFD {
		t.Skipf("kernel did not immediately reuse descriptor: old=%d new=%d", oldFD, reused.Fd())
	}
	if err := copySession.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reused.Write([]byte("descriptor remains owned by its new file")); err != nil {
		t.Fatalf("closing a shallow copy closed a reused descriptor: %v", err)
	}
	if _, err := copySession.Bundle(); err == nil {
		t.Fatal("shallow copy remained usable after shared close")
	}
}

func TestNilVerificationSessionMethodsFailClosed(t *testing.T) {
	var session *VerificationSession
	if _, err := session.Bundle(); err == nil {
		t.Fatal("nil session Bundle accepted")
	}
	if _, err := session.JournalReceipt(); err == nil {
		t.Fatal("nil session JournalReceipt accepted")
	}
	if err := session.ReserveAndAdvanceAdmission(context.Background(), nil); err == nil {
		t.Fatal("nil session admission accepted")
	}
}

func TestReserveAdmissionIsDisabledBeforeAnyDurableBoundary(t *testing.T) {
	opened := time.Unix(1_700_000_100, 0).UTC()
	ledgerPath := t.TempDir()
	markerPath := ledgerPath + "/authority-ledger.v1"
	marker := []byte("must-remain-byte-identical\n")
	if err := os.WriteFile(markerPath, marker, 0600); err != nil {
		t.Fatal(err)
	}
	ledgerRoot, err := os.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ledgerRoot.Close()
	session := &VerificationSession{state: &verificationSessionState{
		clock:            func() time.Time { return opened },
		verificationTime: opened,
		journal:          &releasejournal.Session{},
		ledgerRoot:       ledgerRoot,
		bundle: VerifiedBundle{
			Authority: ca42authority.Descriptor{ClockFloor: opened.Add(-time.Minute), NotBefore: opened.Add(-time.Minute), NotAfter: opened.Add(time.Minute)},
			Release:   ca42release.Manifest{NotBefore: opened.Add(-time.Minute), NotAfter: opened.Add(time.Minute)},
			Execution: ca42execution.Plan{NotBefore: opened.Add(-time.Minute), NotAfter: opened.Add(time.Minute)},
		},
	}}
	err = session.ReserveAndAdvanceAdmission(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "admission disabled") {
		t.Fatalf("incomplete session did not fail before admission: %v", err)
	}
	after, err := os.ReadFile(markerPath)
	if err != nil || !bytes.Equal(after, marker) {
		t.Fatalf("disabled admission changed ledger bytes: bytes=%q err=%v", after, err)
	}
	entries, err := os.ReadDir(ledgerPath)
	if err != nil || len(entries) != 1 || entries[0].Name() != "authority-ledger.v1" {
		t.Fatalf("disabled admission changed ledger directory: entries=%v err=%v", entries, err)
	}
}
