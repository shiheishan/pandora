package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAdminClientReauthTokenUsed(t *testing.T) {
	var loginTok, reauthTok string
	loginTok = "login-only"
	reauthTok = "after-reauth"
	var seenAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": loginTok})
		case "/v1/auth/reauth":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": reauthTok})
		case "/v1/ping":
			seenAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"ok": "1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	cli := newAdminClient(srv.URL, "admin@example.com", "secret-pass-xyz")
	if err := cli.auth(context.Background()); err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := cli.do(context.Background(), http.MethodGet, "/v1/ping", nil, "", http.StatusOK, &out); err != nil {
		t.Fatal(err)
	}
	if seenAuth != "Bearer "+reauthTok {
		t.Fatalf("后续请求令牌应为 reauth 返回的，got %q", seenAuth)
	}
}

func TestAPIErrorFromHeaderReqID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "hdr-req-1")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"code":    "conflict",
				"message": "已存在",
			},
		})
	}))
	t.Cleanup(srv.Close)

	cli := newAdminClient(srv.URL, "a@example.com", "pw")
	cli.token = "t"
	err := cli.doOnce(context.Background(), http.MethodPost, "/v1/servers", nil, "", http.StatusCreated, nil)
	var ae *apiError
	if !errorsAsAPI(err, &ae) {
		t.Fatalf("want apiError, got %v", err)
	}
	if ae.Status != 409 || ae.Code != "conflict" || ae.Message != "已存在" || ae.ReqID != "hdr-req-1" {
		t.Fatalf("unexpected apiError: %+v", ae)
	}
	s := ae.Error()
	if !strings.Contains(s, "POST /v1/servers") || strings.Contains(s, "pw") {
		t.Fatalf("Error() 形状不对或含口令: %s", s)
	}
}

func TestAPIErrorReqIDFromBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"code":       "validation_failed",
				"message":    "无效",
				"request_id": "body-req-9",
			},
		})
	}))
	t.Cleanup(srv.Close)

	cli := newAdminClient(srv.URL, "a@example.com", "nope-secret")
	cli.token = "t"
	err := cli.doOnce(context.Background(), "GET", "/v1/x", nil, "", 200, nil)
	var ae *apiError
	if !errorsAsAPI(err, &ae) || ae.ReqID != "body-req-9" {
		t.Fatalf("ReqID from body: %+v err=%v", ae, err)
	}
	if strings.Contains(ae.Error(), "nope-secret") {
		t.Fatal("Error() 不得含口令")
	}
}

func TestAdminClient429Retry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ok" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := hits.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "rate_limited", "message": "慢点"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(srv.Close)

	cli := newAdminClient(srv.URL, "a@example.com", "p")
	cli.token = "t"
	var out struct {
		Status string `json:"status"`
	}
	if err := cli.doOnce(context.Background(), "GET", "/v1/ok", nil, "", 200, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "ok" || hits.Load() != 2 {
		t.Fatalf("want retry success, hits=%d out=%+v", hits.Load(), out)
	}
}

func TestAdminClient401ReauthOnce(t *testing.T) {
	var apiCalls atomic.Int32
	reauthTok := "fresh-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "login"})
		case "/v1/auth/reauth":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": reauthTok})
		case "/v1/resource":
			n := apiCalls.Add(1)
			auth := r.Header.Get("Authorization")
			if n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{"code": "unauthorized", "message": "过期"},
				})
				return
			}
			if auth != "Bearer "+reauthTok {
				t.Fatalf("second call auth=%q", auth)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	cli := newAdminClient(srv.URL, "a@example.com", "pass")
	cli.token = "stale"
	cli.authedAt = cli.authedAt.Add(-1) // 有 token，ensureFresh 不强制 auth
	var out struct {
		ID string `json:"id"`
	}
	if err := cli.do(context.Background(), "GET", "/v1/resource", nil, "", 200, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "1" || apiCalls.Load() != 2 {
		t.Fatalf("want one 401 then success, calls=%d", apiCalls.Load())
	}
}
