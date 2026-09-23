package token

import (
	"testing"
	"time"
)

func TestIssuerTTLMatchesConfiguredLoginLifetime(t *testing.T) {
	want := 30 * 24 * time.Hour
	issuer := NewIssuer("public", []byte("01234567890123456789012345678901"), want)
	if got := issuer.TTL(); got != want {
		t.Fatalf("TTL() = %s, want %s", got, want)
	}

	raw, err := issuer.Issue(Claims{Subject: "user", TenantID: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := issuer.Verify(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := time.Unix(claims.ExpiresAt, 0).Sub(time.Unix(claims.IssuedAt, 0))
	if got != want {
		t.Fatalf("signed lifetime = %s, want %s", got, want)
	}
}
