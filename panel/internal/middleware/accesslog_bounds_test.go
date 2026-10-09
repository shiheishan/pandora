package middleware

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// 方法名是客户端给的：nginx 只校验字符，任意方法名（A-Z、_、-，可到几 KB）都会转发进来，
// chi 回 405。统计表和逐请求的 access 行只认固定的几种方法，其余一律记 OTHER，
// 否则每换一个方法名就多一个永久条目，未认证就能把网关的内存与日志盘撑满（审查 #1）。
func TestAccessLogNormalizesClientMethods(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	stats := newRouteStats(time.Hour)
	r := chi.NewRouter()
	r.Use(AccessLog(log, withStats(stats)))
	r.Get("/v1/x", func(w http.ResponseWriter, _ *http.Request) {})

	pad := strings.Repeat("A", 512)
	for i := 0; i < 10000; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
		req.Method = fmt.Sprintf("%s%06d", pad, i)
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if strings.Contains(buf.String(), pad) {
		t.Fatal("a client-supplied method name reached the access log")
	}
	if !strings.Contains(buf.String(), `"route":"OTHER -"`) {
		t.Fatalf("unknown methods are not logged as OTHER: %.300s", buf.String())
	}
	if n := len(stats.byKey); n > 2 {
		t.Fatalf("10000 distinct client methods made %d route entries, want ≤ 2 (OTHER -, GET /v1/x)", n)
	}

	// 一次统计只打有界的几行
	buf.Reset()
	stats.maybeFlush(t.Context(), time.Now().Add(2*time.Hour), log)
	if lines := strings.Count(buf.String(), `"access_summary"`); lines > 2 {
		t.Fatalf("one flush wrote %d summary lines, want ≤ 2", lines)
	}
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"} {
		if got := normalizeMethod(m); got != m {
			t.Errorf("normalizeMethod(%q) = %q", m, got)
		}
	}
	for _, m := range []string{"get", "PROPFIND", "", "GET "} {
		if got := normalizeMethod(m); got != "OTHER" {
			t.Errorf("normalizeMethod(%q) = %q, want OTHER", m, got)
		}
	}
}

// 统计表有硬上限：到了上限新出现的键都并进一个溢出条目，请求数不丢（纵深防御：
// 方法归一之后键空间只剩路由器本身的模板，正常到不了上限）。
func TestRouteStatsAreCapped(t *testing.T) {
	stats := newRouteStats(time.Hour)
	stats.max = 4
	r := chi.NewRouter()
	r.Use(AccessLog(slog.New(slog.DiscardHandler), withStats(stats)))
	for i := 0; i < 10; i++ {
		r.Get(fmt.Sprintf("/r%d", i), func(w http.ResponseWriter, _ *http.Request) {})
	}
	for i := 0; i < 10; i++ {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, fmt.Sprintf("/r%d", i), nil))
	}
	if n := len(stats.byKey); n > 5 {
		t.Fatalf("route stats grew to %d entries past a cap of 4", n)
	}
	var total int64
	for _, e := range stats.byKey {
		total += e.n.Load()
	}
	if total+stats.overflow.n.Load() != 10 {
		t.Fatalf("requests counted = %d (+%d overflow), want 10", total, stats.overflow.n.Load())
	}
	if stats.overflow.n.Load() != 6 || stats.overflow.name != "* (overflow)" {
		t.Fatalf("overflow entry = %q n=%d, want 6 requests", stats.overflow.name, stats.overflow.n.Load())
	}
}
