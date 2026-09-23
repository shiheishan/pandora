package nodefabric

import (
	"bytes"
	"testing"
	"time"

	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/google/uuid"
)

func TestConfigSigningKeyTransitionIsOldKeyAuthenticated(t *testing.T) {
	oldSigner, err := platformcrypto.NewSigner(bytes.Repeat([]byte{0x21}, 32))
	if err != nil {
		t.Fatal(err)
	}
	newSigner, err := platformcrypto.NewSigner(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, newSigner)
	if err := service.SetPreviousConfigSigner(oldSigner); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	nodeID := uuid.NewString()
	transition, err := service.ConfigSigningKeyTransition(nodeID, oldSigner.KeyID(), now)
	if err != nil {
		t.Fatal(err)
	}
	if transition == nil || transition.FromKeyID != oldSigner.KeyID() || transition.ToKeyID != newSigner.KeyID() {
		t.Fatalf("unexpected transition: %+v", transition)
	}
	if err := verifyConfigKeyTransition(oldSigner.PublicKey(), *transition, now); err != nil {
		t.Fatalf("old key did not authenticate transition: %v", err)
	}
	tampered := *transition
	tampered.NodeID = uuid.NewString()
	if err := verifyConfigKeyTransition(oldSigner.PublicKey(), tampered, now); err == nil {
		t.Fatal("node-bound transition accepted after tampering")
	}
	current, err := service.ConfigSigningKeyTransition(nodeID, newSigner.KeyID(), now)
	if err != nil || current != nil {
		t.Fatalf("current key should be a no-op: transition=%+v err=%v", current, err)
	}
	if _, err := service.ConfigSigningKeyTransition(nodeID, "unknown", now); err == nil {
		t.Fatal("unknown pinned key received a transition")
	}
}
