package nodesim

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// 两个通道的每个请求都带节点自己的 X-Real-IP；清单没给地址时不加这个头。
func TestNodeClientsCarryRealIP(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("X-Real-IP")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	uni := newUniClient(srv.URL, "n1", "shadowsocks", "tok", "203.0.113.7", time.Second, nil)
	if _, err := uni.http.Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	if ip := <-got; ip != "203.0.113.7" {
		t.Fatalf("uniproxy X-Real-IP = %q", ip)
	}
	uni.http.CloseIdleConnections()

	plain := newUniClient(srv.URL, "n2", "shadowsocks", "tok", "", time.Second, nil)
	if _, err := plain.http.Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	if ip := <-got; ip != "" {
		t.Fatalf("no real_ip in manifest but X-Real-IP = %q", ip)
	}

	signed, err := newSignedClient(srv.URL, ltkit.ManifestNode{ID: "n3", PrivateKey: testPrivateKeyB64(t), RealIP: "203.0.113.9"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signed.http.Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	if ip := <-got; ip != "203.0.113.9" {
		t.Fatalf("signed X-Real-IP = %q", ip)
	}
}

func testPrivateKeyB64(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(priv)
}
