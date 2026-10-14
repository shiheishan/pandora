package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func newTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(RequestID(zap.NewNop()), AccessLog(), Recovery(), Errors(), SecurityHeaders(false))
	e.NoRoute(NoRoute)
	return e
}

type envelope struct {
	Error struct {
		Code      string            `json:"code"`
		Message   string            `json:"message"`
		Fields    map[string]string `json:"fields"`
		RequestID string            `json:"request_id"`
	} `json:"error"`
}

func do(t *testing.T, e *gin.Engine, method, path, body string, hdr map[string]string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	var env envelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

func TestRequestID(t *testing.T) {
	e := newTestEngine()
	e.GET("/x", func(c *gin.Context) { OK(c, gin.H{"id": reqid.From(c.Request.Context())}) })

	w, _ := do(t, e, http.MethodGet, "/x", "", nil)
	if !reqid.Valid(w.Header().Get(reqid.Header)) {
		t.Fatalf("missing generated request id: %q", w.Header().Get(reqid.Header))
	}
	given := reqid.New()
	w, _ = do(t, e, http.MethodGet, "/x", "", map[string]string{reqid.Header: given})
	if w.Header().Get(reqid.Header) != given {
		t.Fatalf("valid id not echoed")
	}
	if !strings.Contains(w.Body.String(), given) {
		t.Fatalf("id not in ctx: %s", w.Body.String())
	}
	w, _ = do(t, e, http.MethodGet, "/x", "", map[string]string{reqid.Header: "evil\nvalue"})
	if got := w.Header().Get(reqid.Header); got == "evil\nvalue" || !reqid.Valid(got) {
		t.Fatalf("invalid id not replaced: %q", got)
	}
}

func TestFailWritesEnvelope(t *testing.T) {
	e := newTestEngine()
	e.GET("/conflict", func(c *gin.Context) { Fail(c, errs.New(errs.Conflict)) })
	w, env := do(t, e, http.MethodGet, "/conflict", "", nil)
	if w.Code != http.StatusConflict || env.Error.Code != "conflict" {
		t.Fatalf("got %d %+v", w.Code, env)
	}
	if env.Error.RequestID == "" || env.Error.RequestID != w.Header().Get(reqid.Header) {
		t.Fatalf("request_id mismatch: %q vs %q", env.Error.RequestID, w.Header().Get(reqid.Header))
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers missing")
	}
}

func TestPlainErrorIsInternalAndHidden(t *testing.T) {
	e := newTestEngine()
	e.GET("/boom", func(c *gin.Context) { Fail(c, errors.New("boom secret detail")) })
	w, env := do(t, e, http.MethodGet, "/boom", "", nil)
	if w.Code != http.StatusInternalServerError || env.Error.Code != "internal_error" {
		t.Fatalf("got %d %+v", w.Code, env)
	}
	if strings.Contains(w.Body.String(), "boom") {
		t.Fatalf("internal detail leaked: %s", w.Body.String())
	}
}

func TestPanicRecovered(t *testing.T) {
	e := newTestEngine()
	e.GET("/panic", func(c *gin.Context) { panic("kaboom") })
	w, env := do(t, e, http.MethodGet, "/panic", "", nil)
	if w.Code != http.StatusInternalServerError || env.Error.Code != "internal_error" {
		t.Fatalf("got %d %+v", w.Code, env)
	}
}

func TestNoRouteIs404Envelope(t *testing.T) {
	w, env := do(t, newTestEngine(), http.MethodGet, "/nope", "", nil)
	if w.Code != http.StatusNotFound || env.Error.Code != "not_found" {
		t.Fatalf("got %d %+v", w.Code, env)
	}
}

func TestRetryAfterHeader(t *testing.T) {
	e := newTestEngine()
	e.GET("/rl", func(c *gin.Context) {
		err := errs.New(errs.RateLimited)
		err.RetryAfterSeconds = 30
		Fail(c, err)
	})
	w, _ := do(t, e, http.MethodGet, "/rl", "", nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "30" {
		t.Fatalf("got %d retry-after %q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestConcurrencyRejectsWhenFull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(RequestID(zap.NewNop()), Errors(), Concurrency(1, nil))
	entered := make(chan struct{})
	release := make(chan struct{})
	e.GET("/slow", func(c *gin.Context) {
		close(entered)
		<-release
		NoContent(c)
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		do(t, e, http.MethodGet, "/slow", "", nil)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never entered")
	}
	w, env := do(t, e, http.MethodGet, "/slow", "", nil)
	close(release)
	wg.Wait()
	if w.Code != http.StatusServiceUnavailable || env.Error.Code != "service_unavailable" {
		t.Fatalf("got %d %+v", w.Code, env)
	}
}

type bindReq struct {
	Name  string `json:"name" binding:"required"`
	Count int    `json:"count" binding:"max=10"`
}

func bindEngine() *gin.Engine {
	e := newTestEngine()
	e.POST("/bind", func(c *gin.Context) {
		var req bindReq
		if err := BindJSON(c, &req); err != nil {
			Fail(c, err)
			return
		}
		OK(c, req)
	})
	return e
}

func TestBindJSON(t *testing.T) {
	e := bindEngine()
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"ok", `{"name":"a","count":1}`, http.StatusOK, ""},
		{"unknown field", `{"name":"a","extra":1}`, http.StatusBadRequest, "bad_request"},
		{"two values", `{"name":"a"}{"name":"b"}`, http.StatusBadRequest, "bad_request"},
		{"too large", `{"name":"` + strings.Repeat("x", MaxBodyBytes) + `"}`, http.StatusBadRequest, "bad_request"},
		{"missing required", `{"count":1}`, http.StatusUnprocessableEntity, "validation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, env := do(t, e, http.MethodPost, "/bind", tc.body, map[string]string{"Content-Type": "application/json"})
			if w.Code != tc.status || env.Error.Code != tc.code {
				t.Fatalf("got %d %+v", w.Code, env)
			}
		})
	}
	_, env := do(t, e, http.MethodPost, "/bind", `{"count":11}`, nil)
	if env.Error.Fields["name"] != "必填" || env.Error.Fields["count"] != "超出上限" {
		t.Fatalf("fields should use json names: %+v", env.Error.Fields)
	}
}

func TestTimeoutSetsDeadlineUnlessExempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(Timeout(time.Second, func(c *gin.Context) bool { return strings.HasSuffix(c.Request.URL.Path, "/events") }))
	e.GET("/a", func(c *gin.Context) {
		_, ok := c.Request.Context().Deadline()
		OK(c, gin.H{"deadline": ok})
	})
	e.GET("/v1/events", func(c *gin.Context) {
		_, ok := c.Request.Context().Deadline()
		OK(c, gin.H{"deadline": ok})
	})
	w, _ := do(t, e, http.MethodGet, "/a", "", nil)
	if !strings.Contains(w.Body.String(), "true") {
		t.Fatalf("expected deadline: %s", w.Body.String())
	}
	w, _ = do(t, e, http.MethodGet, "/v1/events", "", nil)
	if !strings.Contains(w.Body.String(), "false") {
		t.Fatalf("stream should be exempt: %s", w.Body.String())
	}
}

func TestRequestIDSetsReqMeta(t *testing.T) {
	e := newTestEngine()
	e.GET("/m", func(c *gin.Context) {
		m := reqmeta.From(c.Request.Context())
		OK(c, gin.H{"ip": m.IP, "ua": m.UserAgent})
	})
	w, _ := do(t, e, http.MethodGet, "/m", "", map[string]string{"User-Agent": "probe-ua"})
	if !strings.Contains(w.Body.String(), "probe-ua") || !strings.Contains(w.Body.String(), "192.0.2.1") {
		t.Fatalf("reqmeta missing: %s", w.Body.String())
	}
}

func TestConcurrencyExemptBypassesLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(RequestID(zap.NewNop()), Errors(), Concurrency(1, func(c *gin.Context) bool {
		return strings.HasSuffix(c.Request.URL.Path, "/events")
	}))
	entered := make(chan struct{})
	release := make(chan struct{})
	e.GET("/slow", func(c *gin.Context) {
		close(entered)
		<-release
		NoContent(c)
	})
	e.GET("/v1/events", func(c *gin.Context) { NoContent(c) })

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		do(t, e, http.MethodGet, "/slow", "", nil)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never entered")
	}
	w, _ := do(t, e, http.MethodGet, "/v1/events", "", nil)
	close(release)
	wg.Wait()
	if w.Code != http.StatusNoContent {
		t.Fatalf("exempt request got %d", w.Code)
	}
}

func TestSecurityHeadersHSTSOnlyWhenAsked(t *testing.T) {
	for _, hsts := range []bool{true, false} {
		e := gin.New()
		e.Use(SecurityHeaders(hsts))
		e.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		h := w.Header()
		for k, want := range map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"X-Frame-Options":              "DENY",
			"Referrer-Policy":              "no-referrer",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-origin",
			"Permissions-Policy":           permissionsPolicy,
			"Cache-Control":                "no-store",
		} {
			if got := h.Get(k); got != want {
				t.Fatalf("hsts=%v %s = %q，要 %q", hsts, k, got, want)
			}
		}
		if got := h.Get("Strict-Transport-Security"); (got != "") != hsts || (hsts && got != hstsValue) {
			t.Fatalf("hsts=%v Strict-Transport-Security = %q", hsts, got)
		}
	}
}
