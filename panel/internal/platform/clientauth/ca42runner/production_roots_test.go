package ca42runner

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
)

func TestProductionRootLeaseCopiesInputAndSharesClose(t *testing.T) {
	roots := productionRootFixture(t)
	lease, err := openProductionRootLeaseWith(context.Background(), func() (ca42authority.RootKeyset, error) { return roots, nil })
	if err != nil {
		t.Fatal(err)
	}
	roots.ID = "mutated"
	roots.Keys[0].ID = "mutated"
	for index := range roots.Keys[0].PublicKey {
		roots.Keys[0].PublicKey[index] ^= 0xff
	}
	if err := lease.withRootKeyset(context.Background(), func(trusted ca42authority.RootKeyset) error {
		if trusted.ID != "production-roots-v1" || trusted.Keys[0].ID != "root-a" {
			t.Fatal("caller mutation reached production root lease")
		}
		trusted.ID = "callback-mutation"
		trusted.Keys[0].PublicKey[0] ^= 0xff
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	copyOfLease := *lease
	if err := copyOfLease.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	err = lease.withRootKeyset(context.Background(), func(ca42authority.RootKeyset) error { called = true; return nil })
	if !errors.Is(err, errProductionRootLeaseClosed) || called {
		t.Fatalf("closed shallow copy remained usable: called=%v err=%v", called, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProductionRootLeaseRejectsInvalidAndCanceledInputs(t *testing.T) {
	valid := productionRootFixture(t)
	cases := []struct {
		name   string
		mutate func(*ca42authority.RootKeyset)
	}{
		{name: "bad keyset ID", mutate: func(r *ca42authority.RootKeyset) { r.ID = "UPPER" }},
		{name: "bad quorum", mutate: func(r *ca42authority.RootKeyset) { r.Quorum = 1 }},
		{name: "wrong count", mutate: func(r *ca42authority.RootKeyset) { r.Keys = r.Keys[:2] }},
		{name: "unsorted IDs", mutate: func(r *ca42authority.RootKeyset) { r.Keys[1].ID = "root-a" }},
		{name: "private key length", mutate: func(r *ca42authority.RootKeyset) {
			_, private, _ := ed25519.GenerateKey(nil)
			r.Keys[0].PublicKey = ed25519.PublicKey(private)
		}},
		{name: "duplicate public keys", mutate: func(r *ca42authority.RootKeyset) { r.Keys[1].PublicKey = append([]byte(nil), r.Keys[0].PublicKey...) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneRootFixture(valid)
			test.mutate(&candidate)
			lease, err := openProductionRootLeaseWith(context.Background(), func() (ca42authority.RootKeyset, error) { return candidate, nil })
			if lease != nil || !errors.Is(err, errProductionRootLeaseInvalid) {
				t.Fatalf("invalid root set accepted: lease=%v err=%v", lease, err)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	lease, err := openProductionRootLeaseWith(canceled, func() (ca42authority.RootKeyset, error) { called = true; return valid, nil })
	if lease != nil || !errors.Is(err, context.Canceled) || called {
		t.Fatalf("pre-canceled open called provider: lease=%v called=%v err=%v", lease, called, err)
	}
}

func TestProductionRootLeaseFailsClosedWithoutProvisioning(t *testing.T) {
	lease, err := openProductionRootLease(context.Background())
	if lease != nil || !errors.Is(err, errProductionRootLeaseInvalid) {
		t.Fatalf("source build unexpectedly provisioned production roots: lease=%v err=%v", lease, err)
	}
}

func TestProductionRootLeasePostchecksAfterOperationError(t *testing.T) {
	roots := productionRootFixture(t)
	lease, err := openProductionRootLeaseWith(context.Background(), func() (ca42authority.RootKeyset, error) { return roots, nil })
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected authority denial")
	err = lease.withRootKeyset(context.Background(), func(ca42authority.RootKeyset) error {
		lease.state.roots.ID = "mutated-after-open"
		return injected
	})
	if !errors.Is(err, injected) || !errors.Is(err, errProductionRootLeaseClosed) {
		t.Fatalf("post-operation invariant classification lost: %v", err)
	}
	called := false
	err = lease.withRootKeyset(context.Background(), func(ca42authority.RootKeyset) error { called = true; return nil })
	if !errors.Is(err, errProductionRootLeaseClosed) || called {
		t.Fatalf("corrupted root lease was revived: called=%v err=%v", called, err)
	}
}

func productionRootFixture(t *testing.T) ca42authority.RootKeyset {
	t.Helper()
	keys := make([]ca42authority.RootKey, 3)
	for index, id := range []string{"root-a", "root-b", "root-c"} {
		public, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		keys[index] = ca42authority.RootKey{ID: id, PublicKey: public}
	}
	return ca42authority.RootKeyset{ID: "production-roots-v1", Quorum: ca42authority.RequiredQuorum, Keys: keys}
}

func cloneRootFixture(source ca42authority.RootKeyset) ca42authority.RootKeyset {
	clone := ca42authority.RootKeyset{ID: source.ID, Quorum: source.Quorum, Keys: make([]ca42authority.RootKey, len(source.Keys))}
	for index := range source.Keys {
		clone.Keys[index] = ca42authority.RootKey{ID: source.Keys[index].ID, PublicKey: append([]byte(nil), source.Keys[index].PublicKey...)}
	}
	return clone
}
