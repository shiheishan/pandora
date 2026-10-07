package panel

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func signedTestRelease(t *testing.T, issued time.Time) (*SignedConfig, *SignedClient) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keySum := sha256.Sum256(public)
	keyID := base64.RawURLEncoding.EncodeToString(keySum[:8])
	payload := json.RawMessage(`{"protocol":"vless","server_port":443}`)
	manifest := json.RawMessage(`{"node":{"generation":7}}`)
	contentSum, manifestSum := sha256.Sum256(payload), sha256.Sum256(manifest)
	cfg := &SignedConfig{
		ConfigContract: effectiveReleaseContract,
		TenantID:       "11111111-1111-4111-8111-111111111111",
		NodeID:         "22222222-2222-4222-8222-222222222222",
		ReleaseID:      "33333333-3333-4333-8333-333333333333",
		Generation:     7, Payload: payload, SourceManifest: manifest,
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
		Server: "https://panel.example.test", NodeID: cfg.NodeID, PrivateKey: base64.StdEncoding.EncodeToString(private),
		ConfigPublicKey: base64.StdEncoding.EncodeToString(public), ConfigKeyID: keyID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, client
}

// 落盘缓存里的签名配置读回时照验签名，只不查投递窗口：一份早已过了投递窗口
// 的发布照样装得回去；改过任何一个字节、或换了节点身份，一律拒绝。
func TestVerifyCachedConfigIgnoresDeliveryWindowOnly(t *testing.T) {
	cfg, client := signedTestRelease(t, time.Now().UTC().Truncate(time.Microsecond).Add(-48*time.Hour))
	if err := client.VerifyConfig(cfg); err == nil {
		t.Fatal("过了投递窗口的发布在网络通道上居然通过了")
	}
	if err := client.VerifyCachedConfig(cfg); err != nil {
		t.Fatalf("过了投递窗口的缓存被拒：%v", err)
	}
	tampered := *cfg
	tampered.Payload = json.RawMessage(`{"protocol":"vless","server_port":444}`)
	if err := client.VerifyCachedConfig(&tampered); err == nil {
		t.Fatal("改过内容的缓存通过了")
	}
	other := *cfg
	other.NodeID = "44444444-4444-4444-8444-444444444444"
	if err := client.VerifyCachedConfig(&other); err == nil {
		t.Fatal("别的节点的缓存通过了")
	}
}

func TestIsRejection(t *testing.T) {
	for code, want := range map[int]bool{
		http.StatusBadRequest: true, http.StatusUnauthorized: true, http.StatusNotFound: true,
		http.StatusRequestTimeout: false, http.StatusTooManyRequests: false,
		http.StatusInternalServerError: false, http.StatusBadGateway: false,
	} {
		if got := IsRejection(&StatusError{Code: code}); got != want {
			t.Errorf("IsRejection(%d) = %v，期望 %v", code, got, want)
		}
	}
	if IsRejection(errors.New("dial tcp: connection refused")) || IsRejection(nil) {
		t.Fatal("传输错误不是面板的拒绝")
	}
}
