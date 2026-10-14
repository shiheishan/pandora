package crypto

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func mustMasterB64(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestDeriveStableAndSeparated(t *testing.T) {
	master := mustMasterB64(32)
	k, err := NewKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	a1 := k.Derive(InfoSeal, 32)
	a2 := k.Derive(InfoSeal, 32)
	if !bytes.Equal(a1, a2) {
		t.Fatal("same info should derive same bytes")
	}
	b := k.Derive(InfoHMACIP, 32)
	if bytes.Equal(a1, b) {
		t.Fatal("different info should derive different bytes")
	}
}

func TestNewKeysTooShort(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	_, err := NewKeys(short)
	if err == nil {
		t.Fatal("expected error for short master")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	k, err := NewKeys(mustMasterB64(32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := k.Sealer()
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("hello pandora")
	aad := []byte("ctx")
	sealed, err := sealer.Seal(plain, aad)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sealer.Open(sealed, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("plain mismatch: %q vs %q", got, plain)
	}
}

func TestOpenWrongAAD(t *testing.T) {
	k, _ := NewKeys(mustMasterB64(32))
	sealer, _ := k.Sealer()
	sealed, _ := sealer.Seal([]byte("x"), []byte("aad1"))
	_, err := sealer.Open(sealed, []byte("aad2"))
	if err != ErrOpen {
		t.Fatalf("expected ErrOpen, got %v", err)
	}
}

func TestOpenTamper(t *testing.T) {
	k, _ := NewKeys(mustMasterB64(32))
	sealer, _ := k.Sealer()
	sealed, _ := sealer.Seal([]byte("x"), nil)
	sealed[len(sealed)-1] ^= 0xff
	_, err := sealer.Open(sealed, nil)
	if err != ErrOpen {
		t.Fatalf("expected ErrOpen, got %v", err)
	}
}

func TestOpenWrongKeyID(t *testing.T) {
	k, _ := NewKeys(mustMasterB64(32))
	sealer, _ := k.Sealer()
	sealed, _ := sealer.Seal([]byte("x"), nil)
	sealed[1] = 2
	_, err := sealer.Open(sealed, nil)
	if err != ErrOpen {
		t.Fatalf("expected ErrOpen, got %v", err)
	}
}

func TestSealRandomized(t *testing.T) {
	k, _ := NewKeys(mustMasterB64(32))
	sealer, _ := k.Sealer()
	plain := []byte("same")
	s1, _ := sealer.Seal(plain, nil)
	s2, _ := sealer.Seal(plain, nil)
	if bytes.Equal(s1, s2) {
		t.Fatal("two seals of same plaintext should differ")
	}
}
