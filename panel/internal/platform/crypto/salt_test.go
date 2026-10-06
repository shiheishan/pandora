package crypto

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestDerivedSaltsAreDomainSeparated(t *testing.T) {
	master := bytes.Repeat([]byte{0x5a}, 32)
	notify := NotifyRecipientSalt(master)
	want := sha256.Sum256(append([]byte("aegis/notify/recipient-salt/v1"), master...))
	if !bytes.Equal(notify, want[:]) {
		t.Fatal("notify recipient salt formula changed; existing recipient hashes would stop matching")
	}
	if !bytes.Equal(notify, NotifyRecipientSalt(master)) {
		t.Fatal("notify recipient salt must be deterministic")
	}
	sub := SubscriptionAuditSalt(master)
	for name, other := range map[string][]byte{"master key": master, "subscription audit salt": sub} {
		if bytes.Equal(notify, other) {
			t.Fatalf("notify recipient salt equals the %s", name)
		}
	}
	if bytes.Equal(notify, NotifyRecipientSalt(bytes.Repeat([]byte{0x5b}, 32))) {
		t.Fatal("notify recipient salt must depend on the master key")
	}
}
