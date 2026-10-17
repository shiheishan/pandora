package jwtx

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func testKey(seed byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return b
}

func TestSignVerifyOK(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	iss, err := NewIssuer(Public, testKey(1), nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tok, exp, err := iss.Sign("user-1", "sess-1", []string{"pwd"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(time.Hour)) {
		t.Fatalf("exp mismatch: %v", exp)
	}
	claims, err := iss.Verify(tok, now)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-1" || claims.SessionID != "sess-1" {
		t.Fatalf("claims: sub=%s sid=%s", claims.Subject, claims.SessionID)
	}
	if len(claims.AMR) != 1 || claims.AMR[0] != "pwd" {
		t.Fatalf("amr: %v", claims.AMR)
	}
	if claims.Issuer != "pandora-public" {
		t.Fatalf("iss: %s", claims.Issuer)
	}
}

func TestVerifyExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	iss, _ := NewIssuer(Public, testKey(2), nil, time.Minute)
	tok, _, _ := iss.Sign("u", "s", nil, now)
	later := now.Add(2 * time.Minute)
	_, err := iss.Verify(tok, later)
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestAdminRejectsPublicToken(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	pub, _ := NewIssuer(Public, testKey(10), nil, time.Hour)
	adm, _ := NewIssuer(Admin, testKey(11), nil, time.Hour)
	tok, _, _ := pub.Sign("u", "s", nil, now)
	_, err := adm.Verify(tok, now)
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestVerifyAlgNone(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	iss, _ := NewIssuer(Public, testKey(3), nil, time.Hour)
	// 伪造 alg=none 的令牌
	claims := jwt.MapClaims{
		"sub": "u",
		"sid": "s",
		"aud": []string{"public"},
		"iss": "pandora-public",
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tok.Header["typ"] = "at+jwt"
	tok.Header["kid"] = "deadbeefdeadbeef"
	raw, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	_, err = iss.Verify(raw, now)
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestVerifyHS512Rejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	key := testKey(4)
	iss, _ := NewIssuer(Public, key, nil, time.Hour)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "u",
			Audience:  jwt.ClaimStrings{"public"},
			Issuer:    "pandora-public",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		SessionID: "s",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	tok.Header["typ"] = "at+jwt"
	tok.Header["kid"] = keyID(key)
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = iss.Verify(raw, now)
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestVerifyUnknownKid(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	key := testKey(5)
	iss, _ := NewIssuer(Public, key, nil, time.Hour)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "u",
			Audience:  jwt.ClaimStrings{"public"},
			Issuer:    "pandora-public",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		SessionID: "s",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["typ"] = "at+jwt"
	tok.Header["kid"] = "0000000000000000"
	raw, _ := tok.SignedString(key)
	_, err := iss.Verify(raw, now)
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestVerifyWrongTyp(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	key := testKey(6)
	iss, _ := NewIssuer(Public, key, nil, time.Hour)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "u",
			Audience:  jwt.ClaimStrings{"public"},
			Issuer:    "pandora-public",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		SessionID: "s",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["typ"] = "JWT"
	tok.Header["kid"] = keyID(key)
	raw, _ := tok.SignedString(key)
	_, err := iss.Verify(raw, now)
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestVerifyPreviousKeyStillValid(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	oldK, newK := testKey(20), testKey(21)
	oldIss, _ := NewIssuer(Public, oldK, nil, time.Hour)
	tok, _, _ := oldIss.Sign("u", "s", nil, now)
	rotated, _ := NewIssuer(Public, newK, oldK, time.Hour)
	_, err := rotated.Verify(tok, now)
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerifyTamperedPayload(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	iss, _ := NewIssuer(Public, testKey(7), nil, time.Hour)
	tok, _, _ := iss.Sign("u", "s", nil, now)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatal("bad jwt shape")
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if len(payload) == 0 {
		t.Fatal("empty payload")
	}
	payload[0] ^= 0x01
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	tampered := strings.Join(parts, ".")
	_, err := iss.Verify(tampered, now)
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestVerifyEmptySidRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	iss, _ := NewIssuer(Public, testKey(8), nil, time.Hour)
	tok, _, _ := iss.Sign("u", "", nil, now)
	_, err := iss.Verify(tok, now)
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

// exp 边界：golang-jwt v5 在 now 等于 exp 时已判过期，早 1 纳秒仍有效。
func TestVerifyExp边界_exp等于now即过期(t *testing.T) {
	ttl := time.Hour
	start := time.Unix(1_700_000_000, 0).UTC()
	iss, _ := NewIssuer(Public, testKey(9), nil, ttl)
	tok, expAt, _ := iss.Sign("u", "s", nil, start)
	if !expAt.Equal(start.Add(ttl)) {
		t.Fatalf("unexpected expAt %v", expAt)
	}
	if _, err := iss.Verify(tok, expAt.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("1ns before exp expected valid, got %v", err)
	}
	if _, err := iss.Verify(tok, expAt); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("at exact exp expected ErrInvalid, got %v", err)
	}
}
