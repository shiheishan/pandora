//go:build !race

package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/roundtrip"
)

// 往返记账器常开，代价钉死：挂计数器每请求至多多 1 次分配（accessRecord 一次装下
// context 节点、计数器、请求副本与响应包装）。请求副本落到堆上、或者又多了一层
// context，这里就红。日志那一行的分配不在此列（关掉 info 级别只量计数部分）。
// race 构建的插桩会改变分配数，不在 race 下断言。
func TestAccessLogCounterCostsAtMostOneAllocation(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if c := roundtrip.From(req.Context()); c != nil {
			c.AddDB(1)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	build := func(mw func(http.Handler) http.Handler) http.Handler {
		r := chi.NewRouter()
		if mw != nil {
			r.Use(mw)
		}
		r.Get("/v1/me", handler)
		return r
	}
	measure := func(h http.Handler) float64 {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		w := httptest.NewRecorder()
		return testing.AllocsPerRun(200, func() { h.ServeHTTP(w, req) })
	}
	quiet := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	base, counted := measure(build(nil)), measure(build(AccessLog(quiet)))
	if extra := counted - base; extra > 1 {
		t.Fatalf("round-trip counter adds %.0f allocations per request (base %.0f, with counter %.0f), want ≤ 1",
			extra, base, counted)
	}
}
