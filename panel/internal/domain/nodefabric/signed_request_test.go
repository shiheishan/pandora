package nodefabric

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecodeNodeRequestNonceRequiresCanonical128Bits(t *testing.T) {
	raw := []byte("0123456789abcdef")
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	got, err := DecodeNodeRequestNonce(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("decoded nonce = %x, want %x", got, raw)
	}
	for _, invalid := range []string{"", encoded + "=", base64.StdEncoding.EncodeToString(raw), encoded[:21], "!!!!!!!!!!!!!!!!!!!!!!"} {
		if _, err := DecodeNodeRequestNonce(invalid); err == nil {
			t.Fatalf("accepted non-canonical nonce %q", invalid)
		}
	}
}

func TestNodeRequestNonceMigrationFreezesTenantScopedReplayLedger(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "00056_node_request_nonce_replay.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"PRIMARY KEY (tenant_id, node_id, nonce)",
		"REFERENCES nodes (tenant_id, id) ON DELETE CASCADE",
		"CHECK (octet_length(nonce) = 16)",
		"CHECK (octet_length(request_fingerprint) = 32)",
		"app.enable_tenant_rls('node_request_nonces')",
		"GRANT SELECT, INSERT, DELETE ON node_request_nonces TO aegis_app",
		"REVOKE UPDATE, TRUNCATE ON node_request_nonces FROM aegis_app",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
}

func TestCanonicalPayloadV2BindsNonceAndDomain(t *testing.T) {
	bodyHash := sha256.Sum256([]byte(`{"status":"running"}`))
	nonceA := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
	nonceB := base64.RawURLEncoding.EncodeToString([]byte("fedcba9876543210"))
	a := CanonicalPayloadV2("POST", "/v1/nodes/heartbeat", "node-1", "2026-08-10T00:00:00Z", nonceA, bodyHash[:])
	b := CanonicalPayloadV2("POST", "/v1/nodes/heartbeat", "node-1", "2026-08-10T00:00:00Z", nonceB, bodyHash[:])
	if string(a) == string(b) {
		t.Fatal("nonce was not bound into the canonical payload")
	}
	if string(a[:len(nodeRequestSignatureDomainV2)]) != nodeRequestSignatureDomainV2 {
		t.Fatal("V2 domain separator is missing")
	}
	legacy := CanonicalPayload("POST", "/v1/nodes/heartbeat", "node-1", "2026-08-10T00:00:00Z", bodyHash[:])
	if string(a) == string(legacy) {
		t.Fatal("V2 payload aliases the legacy signature contract")
	}
}

func TestSignedRequestNonceRetentionCoversFutureTimestampWindow(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	futureRequest := now.Add(SignedRequestAcceptanceWindow)
	expires := futureRequest.Add(SignedRequestAcceptanceWindow)
	if !expires.Equal(now.Add(2 * SignedRequestAcceptanceWindow)) {
		t.Fatalf("future request nonce expires at %v", expires)
	}
	// ClaimSignedRequest uses PostgreSQL now()+11 minutes as a second floor,
	// so cleanup remains safe even when the API and DB clocks differ slightly.
}
