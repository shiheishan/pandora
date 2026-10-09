package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/roundtrip"
)

func TestAccessLogRecordsRoutePatternAndRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(AccessLog(log))
	r.Route("/v1", func(r chi.Router) {
		r.Get("/things/{token}", func(w http.ResponseWriter, req *http.Request) {
			c := roundtrip.From(req.Context())
			if c == nil {
				t.Fatal("handler sees no round-trip counter")
			}
			c.AddDB(2)
			c.AddPing()
			c.AddKV()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("ok"))
		})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/things/secret-token-value?sig=abc", nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d", w.Code)
	}
	line := buf.String()
	if strings.Contains(line, "secret-token-value") || strings.Contains(line, "sig=abc") {
		t.Fatalf("access log leaked the raw path: %s", line)
	}
	var got struct {
		Msg       string `json:"msg"`
		Route     string `json:"route"`
		Status    int    `json:"status"`
		DurUS     *int64 `json:"dur_us"`
		DBRT      int    `json:"db_rt"`
		KVRT      int    `json:"kv_rt"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("%v: %s", err, line)
	}
	if got.Msg != "access" || got.Route != "GET /v1/things/{token}" || got.Status != http.StatusCreated ||
		got.DurUS == nil || got.DBRT != 3 || got.KVRT != 1 || got.RequestID != w.Header().Get("X-Request-ID") {
		t.Fatalf("access line = %+v", got)
	}

	buf.Reset()
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope/x", nil))
	if !strings.Contains(buf.String(), `"route":"GET -"`) || !strings.Contains(buf.String(), `"status":404`) {
		t.Fatalf("unmatched request line = %s", buf.String())
	}
}

// 事件流处理函数断言 http.Flusher，并经 ResponseController 解除写超时：包装不能挡住这两样。
func TestAccessLogKeepsFlusherAndUnwrap(t *testing.T) {
	h := AccessLog(slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			if _, ok := w.(http.Flusher); !ok {
				t.Fatal("wrapped writer lost http.Flusher")
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Fatalf("ResponseController cannot reach the underlying writer: %v", err)
			}
		}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if !w.Flushed {
		t.Fatal("flush did not reach the recorder")
	}
}

// 基准：计数部分（日志级别关掉，只剩挂计数器）每请求 1 次分配；整行日志另算。
//
//	go test ./internal/middleware -run '^$' -bench AccessLog -benchmem
func BenchmarkAccessLog(b *testing.B) {
	noop := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if c := roundtrip.From(req.Context()); c != nil {
			c.AddDB(1)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	run := func(b *testing.B, h http.Handler) {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		w := httptest.NewRecorder()
		b.ReportAllocs()
		for b.Loop() {
			h.ServeHTTP(w, req)
		}
	}
	routed := func(mw func(http.Handler) http.Handler) http.Handler {
		r := chi.NewRouter()
		if mw != nil {
			r.Use(mw)
		}
		r.Get("/v1/me", noop)
		return r
	}
	b.Run("baseline", func(b *testing.B) { run(b, routed(nil)) })
	quiet := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	b.Run("counter-only", func(b *testing.B) { run(b, routed(AccessLog(quiet))) })
	b.Run("counter+json-line", func(b *testing.B) {
		run(b, routed(AccessLog(slog.New(slog.NewJSONHandler(io.Discard, nil)))))
	})
}
