package panel

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestVerifyEffectiveConfigAndRejectTampering(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keySum := sha256.Sum256(public)
	keyID := base64.RawURLEncoding.EncodeToString(keySum[:8])
	payload := json.RawMessage(`{"protocol":"vless","server_port":443}`)
	manifest := json.RawMessage(`{"node":{"generation":7}}`)
	contentSum := sha256.Sum256(payload)
	manifestSum := sha256.Sum256(manifest)
	issued := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	cfg := &SignedConfig{
		ConfigContract: effectiveReleaseContract,
		TenantID:             "11111111-1111-4111-8111-111111111111",
		NodeID:               "22222222-2222-4222-8222-222222222222",
		ReleaseID:            "33333333-3333-4333-8333-333333333333",
		Generation:           7, Payload: payload, SourceManifest: manifest,
		ContentSHA256:        base64.StdEncoding.EncodeToString(contentSum[:]),
		SourceManifestSHA256: base64.StdEncoding.EncodeToString(manifestSum[:]),
		KeyID:                keyID, IssuedAt: issued, ExpiresAt: issued.Add(10 * time.Minute),
	}
	cfg.Hash = cfg.ContentSHA256
	preimage, err := effectiveReleasePreimage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, preimage))
	client, err := NewSignedClient(&Identity{
		Server: "https://panel.example.test",
		NodeID: cfg.NodeID, PrivateKey: base64.StdEncoding.EncodeToString(private),
		ConfigPublicKey: base64.StdEncoding.EncodeToString(public), ConfigKeyID: keyID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.VerifyConfig(cfg); err != nil {
		t.Fatalf("valid effective release rejected: %v", err)
	}

	tampered := *cfg
	tampered.SourceManifest = json.RawMessage(`{"node":{"generation":8}}`)
	if err := client.VerifyConfig(&tampered); err == nil {
		t.Fatal("tampered source manifest was accepted")
	}
}
