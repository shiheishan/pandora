package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type errEnvelope struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

type claimCall struct {
	scope, key string
	fp         service.Fingerprint
}

type fakeService struct {
	claimResult  *service.Claim
	replayResult *service.Response
	claimErr     error
	completeErr  error

	claimCalls                []claimCall
	completeCalls             []service.Response
	completeUnreplayableCalls []int
	releaseCalls              []int
}

func (f *fakeService) Claim(ctx context.Context, scope, key string, fp service.Fingerprint) (*service.Claim, *service.Response, error) {
	f.claimCalls = append(f.claimCalls, claimCall{scope: scope, key: key, fp: fp})
	if f.claimErr != nil {
		return nil, nil, f.claimErr
	}
	return f.claimResult, f.replayResult, nil
}

func (f *fakeService) Complete(ctx context.Context, c *service.Claim, r service.Response) error {
	f.completeCalls = append(f.completeCalls, r)
	return f.completeErr
}

func (f *fakeService) CompleteUnreplayable(ctx context.Context, c *service.Claim, status int) error {
	f.completeUnreplayableCalls = append(f.completeUnreplayableCalls, status)
	return nil
}

func (f *fakeService) Release(ctx context.Context, c *service.Claim, status int) error {
	f.releaseCalls = append(f.releaseCalls, status)
	return nil
}

func withActor(actor uuid.UUID) gin.HandlerFunc {
	return func(c *gin.Context) {
		if actor != uuid.Nil {
			authn.Set(c, authn.Principal{UserID: actor})
		}
		c.Next()
	}
}

func newTestEngine(h *Handler, actor uuid.UUID, biz gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(httpx.Errors(), httpx.SecurityHeaders())
	r.Use(withActor(actor))
	r.POST("/v1/things", h.Middleware("thing_create"), biz)
	return r
}

func post(t *testing.T, e *gin.Engine, path, body string, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set(service.HeaderKey, key)
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env errEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析错误体: %v body=%q", err, w.Body.String())
	}
	return env.Error.Code
}

func defaultClaim() *service.Claim {
	return &service.Claim{
		ID:         uuid.New(),
		Scope:      "thing_create",
		Generation: 1,
	}
}

func TestMiddlewareMissingOrInvalidKey(t *testing.T) {
	actor := uuid.New()
	bizHits := 0
	fake := &fakeService{claimResult: defaultClaim()}
	e := newTestEngine(New(fake), actor, func(c *gin.Context) { bizHits++ })

	w := post(t, e, "/v1/things", "{}", "")
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "bad_request" {
		t.Fatalf("缺 key: %d %s", w.Code, w.Body.String())
	}
	if len(fake.claimCalls) != 0 || bizHits != 0 {
		t.Fatalf("不应调用 Claim 或业务: claims=%d biz=%d", len(fake.claimCalls), bizHits)
	}

	longKey := strings.Repeat("a", 256)
	w = post(t, e, "/v1/things", "{}", longKey)
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "bad_request" {
		t.Fatalf("256 字节 key: %d", w.Code)
	}

	w = post(t, e, "/v1/things", "{}", "ok\x7fkey")
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "bad_request" {
		t.Fatalf("含 0x7f key: %d", w.Code)
	}
}

func TestMiddlewareUnauthorized(t *testing.T) {
	fake := &fakeService{claimResult: defaultClaim()}
	e := newTestEngine(New(fake), uuid.Nil, func(c *gin.Context) { c.Status(http.StatusCreated) })
	w := post(t, e, "/v1/things", "{}", "k1")
	if w.Code != http.StatusUnauthorized || errorCode(t, w) != "unauthorized" {
		t.Fatalf("未登录: %d %s", w.Code, w.Body.String())
	}
	if len(fake.claimCalls) != 0 {
		t.Fatal("不应调用 Claim")
	}
}

func TestMiddlewareClaimErrors(t *testing.T) {
	actor := uuid.New()
	bizHits := 0
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"reuse", errs.Newf(errs.IdempotencyReuse, "该 Idempotency-Key 已用于内容不同的请求"), "idempotency_key_reuse"},
		{"conflict", errs.Newf(errs.Conflict, "同一请求正在处理或需要人工核对，请稍后重试"), "conflict"},
	}
	for _, tc := range cases {
		fake := &fakeService{claimErr: tc.err}
		e := newTestEngine(New(fake), actor, func(c *gin.Context) { bizHits++ })
		w := post(t, e, "/v1/things", "{}", "k1")
		if w.Code != http.StatusConflict || errorCode(t, w) != tc.code {
			t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
	if bizHits != 0 {
		t.Fatalf("业务不应被调用: %d", bizHits)
	}
}

func TestMiddlewareBodyTooLarge(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {})
	body := strings.Repeat("x", int(service.MaxBodyBytes)+1)
	w := post(t, e, "/v1/things", body, "k1")
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "bad_request" {
		t.Fatalf("超大 body: %d", w.Code)
	}
	if len(fake.claimCalls) != 0 {
		t.Fatal("不应 Claim")
	}
}

func TestMiddlewareFingerprintAndBodyRestore(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	var readBody []byte
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {
		readBody, _ = io.ReadAll(c.Request.Body)
		c.JSON(http.StatusCreated, gin.H{"ok": true})
	})
	body := `{"n":1}`
	w := post(t, e, "/v1/things?a=1", body, "k1")
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d", w.Code)
	}
	if len(fake.claimCalls) != 1 {
		t.Fatalf("Claim 次数 %d", len(fake.claimCalls))
	}
	wantFP := service.NewFingerprint("POST", "/v1/things?a=1", []byte(body))
	if fake.claimCalls[0].fp != wantFP {
		t.Fatal("指纹与 NewFingerprint 不一致")
	}
	if !bytes.Equal(readBody, []byte(body)) {
		t.Fatalf("业务读到的 body: %q", readBody)
	}
}

func TestMiddlewareClaimInContext(t *testing.T) {
	actor := uuid.New()
	claim := defaultClaim()
	fake := &fakeService{claimResult: claim}
	var got *service.Claim
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {
		got, _ = service.ClaimFrom(c.Request.Context())
		c.Status(http.StatusCreated)
	})
	post(t, e, "/v1/things", "{}", "k1")
	if got != claim {
		t.Fatal("ClaimFrom 应得到 fake 返回的同一指针")
	}
}

func TestMiddlewareReplay(t *testing.T) {
	actor := uuid.New()
	replay := &service.Response{
		Status: 201,
		Body:   []byte(`{"id":"r"}`),
		Header: service.Header{
			ContentType: "application/json; charset=utf-8",
			Location:    "/v1/things/r",
		},
	}
	fake := &fakeService{replayResult: replay}
	bizHits := 0
	e := newTestEngine(New(fake), actor, func(c *gin.Context) { bizHits++ })
	w := post(t, e, "/v1/things", "{}", "k1")
	if w.Code != 201 || w.Body.String() != `{"id":"r"}` {
		t.Fatalf("body %q", w.Body.String())
	}
	if w.Header().Get(service.HeaderReplayed) != "true" {
		t.Fatal("缺少 Idempotency-Replayed")
	}
	if w.Header().Get("Location") != "/v1/things/r" {
		t.Fatalf("Location %q", w.Header().Get("Location"))
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("Cache-Control 应保留安全头")
	}
	if bizHits != 0 || len(fake.completeCalls) != 0 || len(fake.releaseCalls) != 0 {
		t.Fatal("重放不应调业务或 Complete/Release")
	}
}

func TestMiddlewareCompleteSuccess(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {
		c.Header("ETag", `"v1"`)
		c.Header("X-Custom", "1")
		c.JSON(201, gin.H{"id": "a"})
	})
	w := post(t, e, "/v1/things", "{}", "k1")
	if w.Code != 201 {
		t.Fatalf("status %d", w.Code)
	}
	if len(fake.completeCalls) != 1 {
		t.Fatalf("Complete 次数 %d", len(fake.completeCalls))
	}
	r := fake.completeCalls[0]
	if r.Status != 201 || r.Body == nil || string(r.Body) != `{"id":"a"}` {
		t.Fatalf("Complete body %q", r.Body)
	}
	if r.Header.ContentType != "application/json; charset=utf-8" || r.Header.ETag != `"v1"` || r.Header.CacheControl != "no-store" {
		t.Fatalf("头 %+v", r.Header)
	}
	if r.Header.Location != "" || r.Header.ContentLanguage != "" {
		t.Fatal("白名单外头不应进 Header 结构")
	}
}

func TestMiddlewareComplete204(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	e := newTestEngine(New(fake), actor, func(c *gin.Context) { c.Status(204) })
	w := post(t, e, "/v1/things", "{}", "k1")
	if w.Code != 204 {
		t.Fatalf("status %d", w.Code)
	}
	if len(fake.completeCalls) != 1 {
		t.Fatal("应 Complete")
	}
	r := fake.completeCalls[0]
	if r.Status != 204 || len(r.Body) != 0 {
		t.Fatalf("204 响应 %+v", r)
	}
}

func TestMiddlewareFailRelease(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {
		httpx.Fail(c, errs.New(errs.ValidationFailed))
	})
	w := post(t, e, "/v1/things", "{}", "k1")
	if w.Code != 422 || errorCode(t, w) != "validation_failed" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(fake.releaseCalls) != 1 || fake.releaseCalls[0] != 422 {
		t.Fatalf("Release %v", fake.releaseCalls)
	}
	if len(fake.completeCalls) != 0 {
		t.Fatal("不应 Complete")
	}
}

func TestMiddlewarePanicReleases(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.Recovery(), httpx.Errors(), withActor(actor))
	e.POST("/v1/things", New(fake).Middleware("thing_create"), func(c *gin.Context) { panic("boom") })
	w := post(t, e, "/v1/things", "{}", "k1")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(fake.releaseCalls) != 1 || fake.releaseCalls[0] != http.StatusInternalServerError {
		t.Fatalf("Release %v", fake.releaseCalls)
	}
}

func TestMiddlewareJSON500Release(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {
		c.JSON(500, gin.H{"err": "x"})
	})
	w := post(t, e, "/v1/things", "{}", "k1")
	if w.Code != 500 {
		t.Fatalf("status %d", w.Code)
	}
	if len(fake.releaseCalls) != 1 || fake.releaseCalls[0] != 500 {
		t.Fatalf("Release %v", fake.releaseCalls)
	}
}

func TestMiddlewareContentEncodingUnreplayable(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {
		c.Header("Content-Encoding", "gzip")
		c.Data(200, "application/octet-stream", []byte("x"))
	})
	post(t, e, "/v1/things", "{}", "k1")
	if len(fake.completeUnreplayableCalls) != 1 || fake.completeUnreplayableCalls[0] != 200 {
		t.Fatalf("CompleteUnreplayable %v", fake.completeUnreplayableCalls)
	}
	if len(fake.completeCalls) != 0 {
		t.Fatal("不应 Complete")
	}
}

func TestMiddlewareOversizedResponseUnreplayable(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	big := strings.Repeat("b", service.MaxBodyBytes+1)
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {
		c.String(200, big)
	})
	w := post(t, e, "/v1/things", "{}", "k1")
	if w.Code != 200 || w.Body.Len() != len(big) {
		t.Fatalf("客户端应收到完整 body len=%d", w.Body.Len())
	}
	if len(fake.completeUnreplayableCalls) != 1 || fake.completeUnreplayableCalls[0] != 200 {
		t.Fatalf("CompleteUnreplayable %v", fake.completeUnreplayableCalls)
	}
}

func TestMiddlewareDuplicateLocationUnreplayable(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {
		c.Writer.Header().Add("Location", "/a")
		c.Writer.Header().Add("Location", "/b")
		c.Status(201)
	})
	post(t, e, "/v1/things", "{}", "k1")
	if len(fake.completeUnreplayableCalls) != 1 {
		t.Fatalf("应 CompleteUnreplayable %v", fake.completeUnreplayableCalls)
	}
}

func TestMiddlewareDetectContentTypeOnComplete(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{claimResult: defaultClaim()}
	html := "<html></html>"
	wantCT := http.DetectContentType([]byte(html))
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {
		c.Writer.WriteHeader(200)
		_, _ = c.Writer.Write([]byte(html))
	})
	post(t, e, "/v1/things", "{}", "k1")
	if len(fake.completeCalls) != 1 {
		t.Fatal("应 Complete")
	}
	if fake.completeCalls[0].Header.ContentType != wantCT {
		t.Fatalf("ContentType 应为 %q，得到 %q", wantCT, fake.completeCalls[0].Header.ContentType)
	}
}

func TestMiddlewareCompleteErrorDoesNotChangeClient(t *testing.T) {
	actor := uuid.New()
	fake := &fakeService{
		claimResult: defaultClaim(),
		completeErr: errors.New("db down"),
	}
	e := newTestEngine(New(fake), actor, func(c *gin.Context) {
		c.JSON(201, gin.H{"id": "a"})
	})
	w := post(t, e, "/v1/things", "{}", "k1")
	if w.Code != 201 || !strings.Contains(w.Body.String(), "a") {
		t.Fatalf("客户端仍应 201: %s", w.Body.String())
	}
}

func TestMiddlewareInvalidScopePanics(t *testing.T) {
	h := New(&fakeService{})
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		_ = h.Middleware("Bad-Scope")
	}()
	if !panicked {
		t.Fatal("非法 scope 应 panic")
	}
}
