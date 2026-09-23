package panel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrapPersistsCompleteIdentity(t *testing.T) {
	configPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/bootstrap" || r.Method != http.MethodPost {
			t.Fatalf("unexpected bootstrap request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"node_id": "node-1", "serial": 1,
			"config_public_key": base64.StdEncoding.EncodeToString(configPublic),
			"config_key_id": "key-1", "runtime_token": "runtime-secret",
		})
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "identity.json")
	identity, err := Bootstrap(context.Background(), BootstrapOptions{
		Server: server.URL, Token: "bootstrap-secret", Name: "node-1", Path: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if identity.NodeID != "node-1" || loaded.RuntimeToken != "runtime-secret" || loaded.ConfigKeyID != "key-1" {
		t.Fatalf("persisted identity is incomplete: %+v", loaded)
	}
}

func TestSignedClientUsesServerCanonicalRequest(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		ts := r.Header.Get("X-Node-Ts")
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Errorf("invalid timestamp %q: %v", ts, err)
		}
		sig, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Node-Sig"))
		if err != nil {
			t.Error(err)
		}
		sum := sha256.Sum256(body)
		nonce := r.Header.Get("X-Node-Nonce")
		nonceRaw, err := base64.RawURLEncoding.DecodeString(nonce)
		if err != nil || len(nonce) != 22 || len(nonceRaw) != 16 || base64.RawURLEncoding.EncodeToString(nonceRaw) != nonce {
			t.Errorf("invalid request nonce %q", nonce)
		}
		canonical := []byte("PANDORA-NODE-REQUEST-V2\n" + r.Method + "\n" + r.URL.Path + "\nnode-1\n" + ts + "\n" + nonce + "\n" + base64.StdEncoding.EncodeToString(sum[:]))
		if r.Header.Get("X-Node-Id") != "node-1" || !ed25519.Verify(public, canonical, sig) {
			t.Error("request did not match the server canonical signature contract")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"node_status":"active","desired_config_version":2,"interval_seconds":30}`))
	}))
	defer server.Close()

	client, err := NewSignedClient(&Identity{
		Server: server.URL, NodeID: "node-1", Serial: 1,
		PrivateKey: base64.StdEncoding.EncodeToString(private),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Heartbeat(context.Background(), HeartbeatInput{AgentVersion: "test"}); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveReportIDIsStablePerReleaseAndPhase(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var reportIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReportID string `json:"report_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reportIDs = append(reportIDs, body.ReportID)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewSignedClient(&Identity{
		Server: server.URL, NodeID: "node-1", Serial: 1,
		PrivateKey: base64.StdEncoding.EncodeToString(private),
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &SignedConfig{ReleaseID: "33333333-3333-4333-8333-333333333333", Generation: 7, ContentSHA256: "hash"}
	for _, phase := range []string{"switched", "switched", "health_passed"} {
		if err := client.ReportEffectiveConfig(context.Background(), cfg, phase, ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(reportIDs) != 3 || reportIDs[0] != reportIDs[1] || reportIDs[0] == reportIDs[2] {
		t.Fatalf("report IDs are not stable per release/phase: %v", reportIDs)
	}
}

func TestBootstrapRejectsIncompleteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"node_id":"node-1","serial":1}`))
	}))
	defer server.Close()

	_, err := Bootstrap(context.Background(), BootstrapOptions{
		Server: server.URL, Token: "bootstrap-secret", Name: "node-1", Path: filepath.Join(t.TempDir(), "identity.json"),
	})
	if err == nil {
		t.Fatal("incomplete bootstrap response was accepted")
	}
}

func TestVerifyConfigRejectsUnexpectedKeyID(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"server_port":443}`)
	sum := sha256.Sum256(payload)
	expires := time.Now().Add(time.Hour).UTC()
	signature := ed25519.Sign(private, append(sum[:], []byte(expires.Format(time.RFC3339))...))
	client, err := NewSignedClient(&Identity{
		Server:          "https://panel.example.test",
		NodeID:          "node-1",
		PrivateKey:       base64.StdEncoding.EncodeToString(private),
		ConfigPublicKey:  base64.StdEncoding.EncodeToString(public),
		ConfigKeyID:      "expected-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.VerifyConfig(&SignedConfig{
		Payload: payload, Hash: base64.StdEncoding.EncodeToString(sum[:]),
		Signature: base64.StdEncoding.EncodeToString(signature),
		KeyID: "unexpected-key", ExpiresAt: expires,
	})
	if err == nil {
		t.Fatal("config signed by an unexpected key id was accepted")
	}
}

func TestValidateSignedServerRejectsRemoteHTTPAndCredentials(t *testing.T) {
	for _, raw := range []string{
		"http://panel.example.test",
		"https://user@panel.example.test",
		"https://panel.example.test/path",
		"https://panel.example.test?token=leak",
	} {
		if got, err := validateSignedServer(raw); err == nil {
			t.Fatalf("unsafe signed server accepted: %q", got)
		}
	}
	for _, raw := range []string{"https://panel.example.test", "http://127.0.0.1:9000", "http://[::1]:9000"} {
		if got, err := validateSignedServer(raw); err != nil || got == "" {
			t.Fatalf("valid signed server rejected: raw=%q got=%q err=%v", raw, got, err)
		}
	}
}
