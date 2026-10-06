package seed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// nginx limit_req 回的 503 HTML 与 429 都重放，直到拿到应用的应答；应用自己的 503 JSON 不重放。
func TestNodeClientRetriesEdgeRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("<html>503 Service Temporarily Unavailable</html>"))
		case 2:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	c := newNodeClient(srv.URL, "v", "")
	out, err := c.do(context.Background(), http.MethodPost, "/v1/nodes/enrollments", []byte(`{}`), nil, http.StatusCreated)
	if err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true || calls.Load() != 3 {
		t.Fatalf("out=%v calls=%d, want ok after 3 calls", out, calls.Load())
	}
}

func TestNodeClientDoesNotRetryApplication503(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"unavailable"}}`))
	}))
	defer srv.Close()
	c := newNodeClient(srv.URL, "v", "")
	if _, err := c.do(context.Background(), http.MethodPost, "/x", []byte(`{}`), nil, http.StatusCreated); err == nil {
		t.Fatal("want error")
	}
	if calls.Load() != 1 {
		t.Fatalf("application 503 replayed %d times", calls.Load())
	}
}
