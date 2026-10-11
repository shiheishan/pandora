package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		DBPing    int    `json:"db_ping"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("%v: %s", err, line)
	}
	if got.Msg != "access" || got.Route != "GET /v1/things/{token}" || got.Status != http.StatusCreated ||
		got.DurUS == nil || got.DBRT != 3 || got.KVRT != 1 || got.DBPing != 1 ||
		got.RequestID != w.Header().Get("X-Request-ID") || strings.Contains(line, "db_prepare") {
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

// 节点网关：成功且快的请求降到 debug，失败的、慢的照常 info。
func TestAccessLogQuietSuccess(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil)) // info 级：debug 行不落
	r := chi.NewRouter()
	r.Use(AccessLog(log, QuietSuccess(50*time.Millisecond)))
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/same", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) })
	r.Get("/bad", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	r.Get("/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(60 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	for _, p := range []string{"/ok", "/same", "/bad", "/slow"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	out := buf.String()
	if strings.Contains(out, `"GET /ok"`) || strings.Contains(out, `"GET /same"`) ||
		!strings.Contains(out, `"GET /bad"`) || !strings.Contains(out, `"GET /slow"`) {
		t.Fatalf("quiet-success lines:\n%s", out)
	}
}

// 每个路由的累计统计：到点后由下一个请求打出，直方图按上界（含）分格，往返取总和与最大值。
func TestAccessLogSummary(t *testing.T) {
	var buf bytes.Buffer
	sumLog := slog.New(slog.NewJSONHandler(&buf, nil))
	r := chi.NewRouter()
	r.Use(AccessLog(sumLog, SummaryEvery(time.Hour)))
	r.Route("/v1", func(r chi.Router) {
		r.Get("/x/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		r.Get("/y", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	})
	for _, p := range []string{"/v1/x/1", "/v1/x/2", "/v1/y"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	if strings.Contains(buf.String(), "access_summary") {
		t.Fatal("summary emitted before its interval")
	}

	// 直接驱动一份统计：到点即打，两条路由各一行
	s := newRouteStats(time.Minute)
	req := func(path string) *http.Request {
		rr := httptest.NewRequest(http.MethodGet, path, nil)
		rctx := chi.NewRouteContext()
		if !r.Match(rctx, http.MethodGet, path) {
			t.Fatalf("no route for %s", path)
		}
		return rr.WithContext(context.WithValue(rr.Context(), chi.RouteCtxKey, rctx))
	}
	a, b := s.entry(req("/v1/x/1")), s.entry(req("/v1/x/2"))
	if a != b || a.name != "GET /v1/x/{id}" {
		t.Fatalf("same route split into %p %p (%q)", a, b, a.name)
	}
	a.record(http.StatusOK, 1200*time.Microsecond, 2, 1)
	a.record(http.StatusOK, 30*time.Millisecond, 3, 0)
	c := s.entry(req("/v1/y"))
	c.record(http.StatusInternalServerError, 2*time.Second, 5, 0)
	buf.Reset()
	s.maybeFlush(context.Background(), time.Now(), sumLog)
	if buf.Len() != 0 {
		t.Fatal("flushed before the interval")
	}
	s.maybeFlush(context.Background(), time.Now().Add(2*time.Minute), sumLog)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("summary lines = %d:\n%s", len(lines), buf.String())
	}
	var got struct {
		Msg    string  `json:"msg"`
		Route  string  `json:"route"`
		N      int64   `json:"n"`
		S2     int64   `json:"s2xx"`
		S5     int64   `json:"s5xx"`
		Le     []int64 `json:"le_ms"`
		Hist   []int64 `json:"hist"`
		DBSum  int64   `json:"db_rt_sum"`
		DBMax  int64   `json:"db_rt_max"`
		KVSum  int64   `json:"kv_rt_sum"`
		DurSum int64   `json:"dur_us_sum"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	// 1.2ms 落进 ≤2，30ms 落进 ≤30
	want := make([]int64, len(accessLatencyBoundsMS)+1)
	want[1], want[7] = 1, 1
	if got.Msg != "access_summary" || got.Route != "GET /v1/x/{id}" || got.N != 2 || got.S2 != 2 ||
		len(got.Le) != len(accessLatencyBoundsMS) || fmt.Sprint(got.Hist) != fmt.Sprint(want) ||
		got.DBSum != 5 || got.DBMax != 3 || got.KVSum != 1 || got.DurSum != 31200 {
		t.Fatalf("summary = %+v", got)
	}
	if err := json.Unmarshal([]byte(lines[1]), &got); err != nil || got.Route != "GET /v1/y" || got.S5 != 1 ||
		got.Hist[len(got.Hist)-1] != 1 {
		t.Fatalf("second summary = %+v (%v)", got, err)
	}
}
