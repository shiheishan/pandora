package nodefabric

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
)

func effectiveReleaseVector(t *testing.T) (*platformcrypto.Signer, EffectiveReleaseSignatureFields) {
	t.Helper()
	signer, err := platformcrypto.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	content := sha256.Sum256([]byte("pandora-effective-release-content-v1"))
	sources := sha256.Sum256([]byte("pandora-effective-release-sources-v1"))
	return signer, EffectiveReleaseSignatureFields{
		TenantID:           "11111111-1111-4111-8111-111111111111",
		NodeID:             "22222222-2222-4222-8222-222222222222",
		ReleaseID:          "33333333-3333-4333-8333-333333333333",
		Generation:         42,
		ContentHash:        base64.StdEncoding.EncodeToString(content[:]),
		SourceManifestHash: base64.StdEncoding.EncodeToString(sources[:]),
		KeyID:              signer.KeyID(),
		IssuedAt:           time.Date(2026, 8, 1, 9, 10, 11, 123456000, time.UTC),
		ExpiresAt:          time.Date(2026, 8, 1, 9, 20, 11, 123456000, time.UTC),
	}
}

func TestEffectiveReleaseSignatureGoldenVector(t *testing.T) {
	signer, fields := effectiveReleaseVector(t)
	preimage, err := EffectiveReleaseSignaturePreimage(fields)
	if err != nil {
		t.Fatal(err)
	}
	wantPreimage := EffectiveReleaseContract + "\n" +
		"tenant_id=11111111-1111-4111-8111-111111111111\n" +
		"node_id=22222222-2222-4222-8222-222222222222\n" +
		"release_id=33333333-3333-4333-8333-333333333333\n" +
		"generation=42\n" +
		"content_sha256=h7ztu4+oyTOxerfX0n9BWkTEL7TQNQdl4XdfFMK/SWw=\n" +
		"source_manifest_sha256=xp9tKNR1Wj/P0PU0E/wQxdFL2ixVeUKMukRMYlmEILA=\n" +
		"key_id=-3lwIZ4m0fQ\n" +
		"issued_at=2026-08-01T09:10:11.123456Z\n" +
		"expires_at=2026-08-01T09:20:11.123456Z\n"
	if string(preimage) != wantPreimage {
		t.Fatalf("preimage mismatch\nwant=%q\n got=%q", wantPreimage, preimage)
	}
	const wantPreimageSHA256 = "C31519518C5D291439A5800F57B859D23EEC66142B335084BFE181920200DC77"
	if got := sha256.Sum256(preimage); strings.ToUpper(fmt.Sprintf("%x", got)) != wantPreimageSHA256 {
		t.Fatalf("preimage sha256=%x", got)
	}
	signature, err := SignEffectiveRelease(signer, fields)
	if err != nil {
		t.Fatal(err)
	}
	const wantSignature = "vM3ueFMKbIkn/nYgyUOP7N9J7b2n3AwlzEEu4W5nQ0RUli7Pe39k8Qbhv8STlqlNX07X3w/YdAHPBCids6r+CQ=="
	if signature != wantSignature {
		t.Fatalf("signature=%q", signature)
	}
	if err := VerifyEffectiveReleaseSignature(signer.PublicKey(), fields.KeyID, fields, signature, fields.IssuedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveReleaseSignatureRejectsEveryBoundFieldMutation(t *testing.T) {
	signer, fields := effectiveReleaseVector(t)
	signature, err := SignEffectiveRelease(signer, fields)
	if err != nil {
		t.Fatal(err)
	}
	now := fields.IssuedAt.Add(time.Second)
	mutations := map[string]func(*EffectiveReleaseSignatureFields){
		"tenant":     func(v *EffectiveReleaseSignatureFields) { v.TenantID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" },
		"node":       func(v *EffectiveReleaseSignatureFields) { v.NodeID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" },
		"release":    func(v *EffectiveReleaseSignatureFields) { v.ReleaseID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc" },
		"generation": func(v *EffectiveReleaseSignatureFields) { v.Generation++ },
		"content":    func(v *EffectiveReleaseSignatureFields) { v.ContentHash = strings.Repeat("A", 43) + "=" },
		"sources":    func(v *EffectiveReleaseSignatureFields) { v.SourceManifestHash = strings.Repeat("A", 43) + "=" },
		"key":        func(v *EffectiveReleaseSignatureFields) { v.KeyID = "AQ" },
		"issued_at":  func(v *EffectiveReleaseSignatureFields) { v.IssuedAt = v.IssuedAt.Add(time.Nanosecond) },
		"expires_at": func(v *EffectiveReleaseSignatureFields) { v.ExpiresAt = v.ExpiresAt.Add(-time.Nanosecond) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := fields
			mutate(&changed)
			if err := VerifyEffectiveReleaseSignature(signer.PublicKey(), fields.KeyID, changed, signature, now); err == nil {
				t.Fatal("mutated signed field was accepted")
			}
		})
	}
}

func TestEffectiveReleaseSignatureRejectsNoncanonicalAndTimeBounds(t *testing.T) {
	signer, fields := effectiveReleaseVector(t)
	cases := map[string]func(*EffectiveReleaseSignatureFields){
		"uppercase_uuid":         func(v *EffectiveReleaseSignatureFields) { v.TenantID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" },
		"zero_generation":        func(v *EffectiveReleaseSignatureFields) { v.Generation = 0 },
		"generation_over_bigint": func(v *EffectiveReleaseSignatureFields) { v.Generation = uint64(math.MaxInt64) + 1 },
		"nil_uuid":               func(v *EffectiveReleaseSignatureFields) { v.NodeID = uuid.Nil.String() },
		"padded_key_id":          func(v *EffectiveReleaseSignatureFields) { v.KeyID += "=" },
		"short_key_id":           func(v *EffectiveReleaseSignatureFields) { v.KeyID = "AQ" },
		"short_hash": func(v *EffectiveReleaseSignatureFields) {
			v.ContentHash = base64.StdEncoding.EncodeToString(make([]byte, 31))
		},
		"empty_window": func(v *EffectiveReleaseSignatureFields) { v.ExpiresAt = v.IssuedAt },
		"long_window": func(v *EffectiveReleaseSignatureFields) {
			v.ExpiresAt = v.IssuedAt.Add(EffectiveReleaseMaxDeliveryWindow + time.Nanosecond)
		},
		"submicrosecond_issued": func(v *EffectiveReleaseSignatureFields) { v.IssuedAt = v.IssuedAt.Add(time.Nanosecond) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := fields
			mutate(&changed)
			if _, err := EffectiveReleaseSignaturePreimage(changed); err == nil {
				t.Fatal("invalid fields were accepted")
			}
		})
	}

	signature, err := SignEffectiveRelease(signer, fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyEffectiveReleaseSignature(signer.PublicKey(), fields.KeyID, fields, signature, fields.IssuedAt.Add(-time.Nanosecond)); err == nil {
		t.Fatal("future release was accepted")
	}
	if err := VerifyEffectiveReleaseSignature(signer.PublicKey(), fields.KeyID, fields, signature, fields.ExpiresAt); err == nil {
		t.Fatal("expired release was accepted")
	}
	if err := VerifyEffectiveReleaseSignature(signer.PublicKey(), fields.KeyID, fields, signature+"=", fields.IssuedAt); err == nil {
		t.Fatal("noncanonical signature was accepted")
	}
	changedKey := fields
	changedKey.KeyID = base64.RawURLEncoding.EncodeToString([]byte("badkeyid"))
	changedPreimage, err := EffectiveReleaseSignaturePreimage(changedKey)
	if err != nil {
		t.Fatal(err)
	}
	changedSignature := base64.StdEncoding.EncodeToString(signer.Sign(changedPreimage))
	if err := VerifyEffectiveReleaseSignature(signer.PublicKey(), fields.KeyID, changedKey, changedSignature, fields.IssuedAt); err == nil {
		t.Fatal("re-signed changed key_id was accepted")
	}
}

func TestParseEffectiveReleaseTimestamp(t *testing.T) {
	if _, err := ParseEffectiveReleaseTimestamp("2026-08-01T09:10:11.123456Z"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"2026-08-01T09:10:11.123456789Z",
		"2026-08-01T09:10:11.123456+00:00",
		"2026-08-01t09:10:11.123456Z",
	} {
		if _, err := ParseEffectiveReleaseTimestamp(value); err == nil {
			t.Fatalf("noncanonical timestamp %q was accepted", value)
		}
	}
}
