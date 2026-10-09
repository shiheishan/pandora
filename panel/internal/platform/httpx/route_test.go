package httpx

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Fail 的日志写路由模板，不写原始路径：根路由器中间件里（路由还没走）、子路由器里、
// 处理函数里、没匹配上任何路由，四种位置都只出模板或占位。
func TestFailLogsRouteTemplateNotRawPath(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	fail := func(w http.ResponseWriter, r *http.Request) {
		Fail(w, r, log, New(CodeNotFound, "x"))
	}

	r := chi.NewRouter()
	// 根中间件里拒绝：模板要从路由树里查出来
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get("X-Reject") != "" {
				fail(w, req)
				return
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/{prefix}/{token}", fail)
	r.Route("/v1", func(r chi.Router) {
		r.Post("/webhooks/telegram/{secret}", fail)
	})
	r.NotFound(fail)

	cases := []struct {
		method, path, reject, want string
	}{
		{http.MethodGet, "/0123456789ab/tok-SECRET-1", "", "/{prefix}/{token}"},
		{http.MethodGet, "/0123456789ab/tok-SECRET-2", "1", "/{prefix}/{token}"},
		{http.MethodPost, "/v1/webhooks/telegram/SECRET-3", "", "/v1/webhooks/telegram/{secret}"},
		{http.MethodPost, "/v1/webhooks/telegram/SECRET-4", "1", "/v1/webhooks/telegram/{secret}"},
		{http.MethodGet, "/a/b/SECRET-5", "", UnmatchedRoute},
		{http.MethodGet, "/v1/SECRET-6", "", "/v1/*"},
	}
	for _, c := range cases {
		logs.Reset()
		req := httptest.NewRequest(c.method, c.path, nil)
		if c.reject != "" {
			req.Header.Set("X-Reject", "1")
		}
		r.ServeHTTP(httptest.NewRecorder(), req)
		out := logs.String()
		if strings.Contains(out, "SECRET") {
			t.Fatalf("%s %s: raw path in log: %s", c.method, c.path, out)
		}
		if !strings.Contains(out, `"route":"`+c.want+`"`) {
			t.Fatalf("%s %s: want route %q in log: %s", c.method, c.path, c.want, out)
		}
	}

	// 不经 chi（没有路由上下文）时写占位
	logs.Reset()
	fail(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x/SECRET-7", nil))
	if out := logs.String(); strings.Contains(out, "SECRET") || !strings.Contains(out, `"route":"-"`) {
		t.Fatalf("without a route context: %s", out)
	}
}
