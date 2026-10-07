package panel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestJitterStaysWithinTenPercent(t *testing.T) {
	const base = 15 * time.Second
	low, high := base, base
	for i := 0; i < 2000; i++ {
		d := Jitter(base)
		if d < base*9/10 || d > base*11/10 {
			t.Fatalf("Jitter(%s) = %s, outside ±10%%", base, d)
		}
		low, high = min(low, d), max(high, d)
	}
	if high-low < base/10 {
		t.Fatalf("Jitter barely varies: [%s, %s]", low, high)
	}
	if Jitter(0) != 0 || Jitter(-time.Second) != -time.Second {
		t.Fatal("non-positive intervals must pass through unchanged")
	}
}

type sinceServer struct {
	mu        sync.Mutex
	applied   []string
	keyChecks int
	reply     int
}

func (s *sinceServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/v1/nodes/config-signing-key":
		s.keyChecks++
		w.WriteHeader(http.StatusNoContent)
	case "/v1/nodes/effective-config":
		s.applied = append(s.applied, r.Header.Get(AppliedEffectiveReleaseHeader))
		if s.reply == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"config_contract":"aegis-node-effective-config-release-v1","release_id":"r"}`))
	}
}

func newSinceClient(t *testing.T, srv *httptest.Server) *SignedClient {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewSignedClient(&Identity{Server: srv.URL, NodeID: "node-1", Serial: 1,
		PrivateKey: base64.StdEncoding.EncodeToString(private)})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// 带已应用版本才可能拿到 204；没带却收到 204 是面板异常，不能当成「没变化」。
func TestConfigSinceUnchangedOnlyWhenApplied(t *testing.T) {
	fake := &sinceServer{reply: http.StatusNoContent}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	client := newSinceClient(t, srv)
	ctx := context.Background()

	cfg, unchanged, err := client.ConfigSince(ctx, &AppliedRelease{ReleaseID: "11111111-1111-4111-8111-111111111111", Generation: 3})
	if err != nil || !unchanged || cfg != nil {
		t.Fatalf("204 with applied release = %+v %v %v", cfg, unchanged, err)
	}
	if _, _, err := client.ConfigSince(ctx, nil); err == nil {
		t.Fatal("204 without an applied release was accepted as a config")
	}
	fake.mu.Lock()
	applied := append([]string(nil), fake.applied...)
	fake.mu.Unlock()
	if len(applied) != 2 || applied[0] != "11111111-1111-4111-8111-111111111111/3" || applied[1] != "" {
		t.Fatalf("applied headers = %q", applied)
	}

	fake.mu.Lock()
	fake.reply = http.StatusOK
	fake.mu.Unlock()
	cfg, unchanged, err = client.ConfigSince(ctx, &AppliedRelease{ReleaseID: "11111111-1111-4111-8111-111111111111", Generation: 3})
	if err != nil || unchanged || cfg == nil || cfg.ReleaseID != "r" {
		t.Fatalf("old-panel full reply = %+v %v %v", cfg, unchanged, err)
	}
}

// 换钥检查按间隔做，不是每次拉配置都做；到点（这里直接改记录时间）才再问。
func TestConfigSigningKeyCheckedOnInterval(t *testing.T) {
	fake := &sinceServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	client := newSinceClient(t, srv)
	for i := 0; i < 4; i++ {
		if _, err := client.Config(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if fake.keyChecks != 1 {
		t.Fatalf("key checked %d times in 4 fetches, want 1", fake.keyChecks)
	}
	if client.keyCheckWait < configKeyCheckInterval*9/10 || client.keyCheckWait > configKeyCheckInterval*11/10 {
		t.Fatalf("key check interval %s is not a jittered %s", client.keyCheckWait, configKeyCheckInterval)
	}
	client.keyCheckedAt = time.Now().Add(-configKeyCheckInterval * 2)
	if _, err := client.Config(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.keyChecks != 2 {
		t.Fatalf("key not re-checked after the interval: %d", fake.keyChecks)
	}
}
