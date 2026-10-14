package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/reqstats"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestHTTPMetricsExposed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := NewRegistry()
	h := NewHTTP(reg)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.AccessLog(), h.Middleware("public"))
	e.GET("/v1/items/:id", func(c *gin.Context) {
		st := reqstats.From(c.Request.Context())
		st.AddSQL(time.Millisecond)
		st.AddSQL(time.Millisecond)
		st.AddRedis(1)
		c.Status(http.StatusOK)
	})
	e.NoRoute(func(c *gin.Context) { c.Status(http.StatusNotFound) })

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/items/tok-abc123", nil)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
	}
	req := httptest.NewRequest(http.MethodGet, "/nope/tok-xyz789", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	req = httptest.NewRequest("BREW", "/v1/items/1", nil)
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)

	rec := httptest.NewRecorder()
	NewServer("127.0.0.1:0", reg).Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	mustContain(t, body, `pandora_http_requests_total{method="GET",route="/v1/items/:id",server="public",status="200"} 2`)
	mustContain(t, body, `pandora_http_sql_queries_total{route="/v1/items/:id",server="public"} 4`)
	mustContain(t, body, `pandora_http_redis_commands_total{route="/v1/items/:id",server="public"} 2`)
	mustContain(t, body, `route="-"`)
	mustContain(t, body, `method="OTHER"`)
	mustContain(t, body, `pandora_http_request_duration_seconds_bucket`)
	mustContain(t, body, `go_goroutines`)
	if strings.Contains(body, "tok-abc123") || strings.Contains(body, "tok-xyz789") {
		t.Fatalf("metrics body must not contain raw path tokens")
	}
}

func mustContain(t *testing.T, body, sub string) {
	t.Helper()
	if !strings.Contains(body, sub) {
		t.Fatalf("body missing %q\n---\n%s", sub, body)
	}
}

func TestNewRegistryIndependent(t *testing.T) {
	for i := 0; i < 2; i++ {
		reg := NewRegistry()
		NewHTTP(reg)
	}
}

func TestMethodLabel(t *testing.T) {
	if methodLabel("GET") != "GET" {
		t.Fatalf("GET")
	}
	if methodLabel("BREW") != "OTHER" {
		t.Fatalf("BREW")
	}
	if methodLabel("get") != "OTHER" {
		t.Fatalf("get")
	}
}
