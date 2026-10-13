package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		allow   bool
		wantErr string
	}{
		{"https ok", "https://hooks.example.com/x", false, ""},
		{"http blocked prod", "http://hooks.example.com", false, "必须使用 https"},
		{"ftp", "ftp://x.example.com", false, "只支持 http/https"},
		{"empty", "", false, "回调地址必填"},
		{"spaces", "   ", false, "回调地址必填"},
		{"userinfo", "https://user:pw@hooks.example.com", false, "地址里不能带用户名或口令"},
		{"no host", "https:///path", false, "地址里没有主机名"},
		{"http dev", "http://127.0.0.1:8080/x", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseEndpoint(tc.raw, tc.allow)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want err containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateEndpointRejectsPrivate(t *testing.T) {
	ctx := context.Background()
	blocked := []string{
		"https://127.0.0.1",
		"https://10.0.0.1",
		"https://192.168.1.1",
		"https://169.254.169.254",
		"https://100.64.0.1",
		"https://0.0.0.0",
		"https://[::1]",
		"https://[fe80::1]",
		"https://[fd00::1]",
		"https://localhost",
	}
	for _, raw := range blocked {
		if err := validateEndpoint(ctx, raw, false); err == nil {
			t.Fatalf("expected error for %s", raw)
		}
	}
	if err := validateEndpoint(ctx, "https://203.0.113.10", false); err != nil {
		t.Fatalf("203.0.113.10 should pass: %v", err)
	}
}

func TestCheckDialAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1:443",
		"[::ffff:10.0.0.1]:443",
		"169.254.169.254:80",
		"[64:ff9b::a00:1]:443",
	}
	for _, addr := range blocked {
		if err := checkDialAddr(addr); err == nil {
			t.Fatalf("expected block %s", addr)
		}
	}
	if err := checkDialAddr("203.0.113.10:443"); err != nil {
		t.Fatalf("public should pass: %v", err)
	}
	if err := checkDialAddr("not-an-addr"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestDialGuardBlocksLoopback(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	url := srv.URL + "/secret/path?token=abc"
	ctx := context.Background()
	body := []byte(`{}`)
	now := time.Unix(1, 0)

	_, dur, err := postSigned(ctx, newHTTPClient(false), url, "e", "d", body, "", 5*time.Second, now)
	if err == nil {
		t.Fatal("expected dial error")
	}
	if called.Load() {
		t.Fatal("handler should not run")
	}
	if dur == nil {
		t.Fatal("durationMS should be set")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "/secret/path") || strings.Contains(errStr, "token=abc") {
		t.Fatalf("error leaks path/query: %s", errStr)
	}

	called.Store(false)
	code, _, err := postSigned(ctx, newHTTPClient(true), url, "e", "d", body, "", 5*time.Second, now)
	if err != nil {
		t.Fatalf("allow private: %v", err)
	}
	if code != 200 || !called.Load() {
		t.Fatalf("want 200 and handler called, code=%d called=%v", code, called.Load())
	}
}

func TestNoRedirectFollow(t *testing.T) {
	var bHit atomic.Bool
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bHit.Store(true)
	}))
	defer b.Close()

	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, b.URL, http.StatusFound)
	}))
	defer a.Close()

	code, _, err := postSigned(context.Background(), newHTTPClient(true), a.URL, "e", "d", []byte("{}"), "", 5*time.Second, time.Now())
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if code != 302 {
		t.Fatalf("want 302, got %d", code)
	}
	if bHit.Load() {
		t.Fatal("redirect target should not be called")
	}
}

func TestPostSignedHeaders(t *testing.T) {
	var got map[string]string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = map[string]string{
			"Content-Type":        r.Header.Get("Content-Type"),
			"User-Agent":          r.Header.Get("User-Agent"),
			"X-Pandora-Event":     r.Header.Get("X-Pandora-Event"),
			"X-Pandora-Delivery":  r.Header.Get("X-Pandora-Delivery"),
			"X-Pandora-Timestamp": r.Header.Get("X-Pandora-Timestamp"),
			"X-Pandora-Signature": r.Header.Get("X-Pandora-Signature"),
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	body := []byte(`{"a":1}`)
	now := time.Unix(1700000000, 0)
	_, _, err := postSigned(context.Background(), newHTTPClient(true), srv.URL, "order.paid", "d-1", body, "whsec_test", 5*time.Second, now)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if got["Content-Type"] != "application/json" {
		t.Fatalf("Content-Type: %s", got["Content-Type"])
	}
	if got["User-Agent"] != "PandoraPanel-Hook/1" {
		t.Fatalf("User-Agent: %s", got["User-Agent"])
	}
	if got["X-Pandora-Event"] != "order.paid" || got["X-Pandora-Delivery"] != "d-1" {
		t.Fatalf("event/delivery headers: %v", got)
	}
	if got["X-Pandora-Timestamp"] != "1700000000" {
		t.Fatalf("timestamp: %s", got["X-Pandora-Timestamp"])
	}
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write([]byte("1700000000"))
	mac.Write([]byte("."))
	mac.Write(body)
	wantSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got["X-Pandora-Signature"] != wantSig {
		t.Fatalf("signature: got %s want %s", got["X-Pandora-Signature"], wantSig)
	}
	if string(gotBody) != string(body) {
		t.Fatalf("body mismatch")
	}

	got = nil
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Pandora-Signature") != "" {
			t.Error("signature should be absent when secret empty")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()
	_, _, err = postSigned(context.Background(), newHTTPClient(true), srv2.URL, "e", "d", body, "", 5*time.Second, now)
	if err != nil {
		t.Fatalf("post no secret: %v", err)
	}
}

func TestSign(t *testing.T) {
	secret := "whsec_test"
	ts := "1700000000"
	body := []byte(`{"a":1}`)
	got := Sign(secret, ts, body)
	if !strings.HasPrefix(got, "sha256=") {
		t.Fatalf("prefix: %s", got)
	}
	hexPart := strings.TrimPrefix(got, "sha256=")
	if len(hexPart) != 64 {
		t.Fatalf("hex len %d", len(hexPart))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestHostOf(t *testing.T) {
	if hostOf("https://hooks.example.com:8443/a/b?c=d") != "https://hooks.example.com:8443" {
		t.Fatal("hostOf mismatch")
	}
}
