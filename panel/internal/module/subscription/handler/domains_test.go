package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// fakeDomainSvc 是后台订阅域名分组与探测接口的替身；其余方法走嵌入的 nil 接口，被调到会 panic。
type fakeDomainSvc struct {
	Service
	cfg     service.DomainConfigView
	saved   []service.DomainGroupInput
	saveErr error

	ticket          string
	ticketErr       error
	frameDomains    []string
	frameErr        error
	gotFrameTicket  string
	reported        []service.ProbeResult
	gotReportTicket string
	reportErr       error
	origin          string
}

func (f *fakeDomainSvc) AdminDomainConfig(context.Context) (service.DomainConfigView, error) {
	return f.cfg, nil
}

func (f *fakeDomainSvc) AdminSaveDomainConfig(_ context.Context, in []service.DomainGroupInput) (service.DomainConfigView, error) {
	f.saved = in
	if f.saveErr != nil {
		return service.DomainConfigView{}, f.saveErr
	}
	return f.cfg, nil
}

func (f *fakeDomainSvc) IssueProbeTicket(context.Context) (string, error) {
	if f.ticketErr != nil {
		return "", f.ticketErr
	}
	return f.ticket, nil
}

func (f *fakeDomainSvc) ProbeFrameDomains(_ context.Context, ticket string) ([]string, error) {
	f.gotFrameTicket = ticket
	if f.frameErr != nil {
		return nil, f.frameErr
	}
	return f.frameDomains, nil
}

func (f *fakeDomainSvc) ReportProbeResults(_ context.Context, ticket string, results []service.ProbeResult) error {
	f.gotReportTicket = ticket
	f.reported = results
	return f.reportErr
}

func (f *fakeDomainSvc) PortalOrigin() string { return f.origin }

// domainEngine 建引擎；auth 为 nil 时不带认证（未登录），reauth 为 nil 时放行。
func domainEngine(svc Service, auth, reauth gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	if auth == nil {
		auth = func(c *gin.Context) {
			httpx.Fail(c, errs.New(errs.Unauthorized))
		}
	}
	if reauth == nil {
		reauth = func(c *gin.Context) { c.Next() }
	}
	kit := route.Kit{
		RequireAuth:  auth,
		RecentReauth: reauth,
		Idempotency:  func(string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
		Switch:       func(string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
	}
	New(svc, nil, 0).RegisterAdmin(e.Group("/v1"), kit)
	return e
}

func passAuth(c *gin.Context) { c.Next() }

func TestAdminDomainsUnauthenticated(t *testing.T) {
	f := &fakeDomainSvc{}
	e := domainEngine(f, nil, nil)
	w := do(e, http.MethodGet, "/v1/subscription-domains", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminDomainsShape(t *testing.T) {
	gid, ugID := uuid.New(), uuid.New()
	f := &fakeDomainSvc{cfg: service.DomainConfigView{
		Groups: []service.DomainGroupView{{
			ID: gid, Name: "缺省", IsDefault: true,
			Domains: []service.DomainView{
				{BaseURL: "https://sub-a.example.com", Weight: 2, Score: 1.556, EffectiveWeight: 3.112},
				{BaseURL: "https://sub-b.example.com", Weight: 1, Score: 1, EffectiveWeight: 1},
			},
			UserGroupIDs: []uuid.UUID{ugID},
		}},
		ScoresAvailable: true,
	}}
	e := domainEngine(f, passAuth, nil)
	w := do(e, http.MethodGet, "/v1/subscription-domains", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	got := decMap(t, w)
	if got["scores_available"] != true {
		t.Fatalf("scores_available: %v", got["scores_available"])
	}
	groups := got["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("groups len %d", len(groups))
	}
	g := groups[0].(map[string]any)
	if g["id"] != gid.String() || g["name"] != "缺省" || g["is_default"] != true {
		t.Fatalf("group: %v", g)
	}
	ug := g["user_group_ids"].([]any)
	if len(ug) != 1 || ug[0] != ugID.String() {
		t.Fatalf("user_group_ids: %v", ug)
	}
	doms := g["domains"].([]any)
	first := doms[0].(map[string]any)
	if first["base_url"] != "https://sub-a.example.com" || first["weight"] != float64(2) {
		t.Fatalf("first: %v", first)
	}
	if first["score"] != 1.56 {
		t.Fatalf("score 应四舍五入到 1.56: %v", first["score"])
	}
	if first["effective_weight"] != 3.11 {
		t.Fatalf("effective_weight 应四舍五入到两位: %v", first["effective_weight"])
	}
	limits := got["limits"].(map[string]any)
	if limits["groups"] != float64(service.DomainGroupsMax) ||
		limits["domains_per_group"] != float64(service.DomainsPerGroupMax) ||
		limits["weight_max"] != float64(service.DomainWeightMax) {
		t.Fatalf("limits: %v", limits)
	}
}

func TestAdminDomainsEmptySlicesNotNull(t *testing.T) {
	f := &fakeDomainSvc{cfg: service.DomainConfigView{
		Groups: []service.DomainGroupView{{
			Name:    "空",
			Domains: []service.DomainView{},
		}},
	}}
	e := domainEngine(f, passAuth, nil)
	w := do(e, http.MethodGet, "/v1/subscription-domains", "")
	got := decMap(t, w)
	groups := got["groups"].([]any)
	g := groups[0].(map[string]any)
	if doms, ok := g["domains"].([]any); !ok || len(doms) != 0 {
		t.Fatalf("domains 应为 [] 而不是 null: %v", g["domains"])
	}
	if ug, ok := g["user_group_ids"].([]any); !ok || len(ug) != 0 {
		t.Fatalf("user_group_ids 应为 [] 而不是 null: %v", g["user_group_ids"])
	}
}

func TestAdminSaveDomainsRequiresReauth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reauth := func(c *gin.Context) {
		if c.GetHeader("X-Test-Reauth") != "1" {
			httpx.Fail(c, errs.New(errs.ReauthRequired))
			return
		}
		c.Next()
	}
	f := &fakeDomainSvc{}
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	kit := route.Kit{
		RequireAuth:  passAuth,
		RecentReauth: reauth,
		Idempotency:  func(string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
		Switch:       func(string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
	}
	New(f, nil, 0).RegisterAdmin(e.Group("/v1"), kit)

	body := `{"groups":[]}`
	w := do(e, http.MethodPut, "/v1/subscription-domains", body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("无重认证应 403: %d body=%s", w.Code, w.Body.String())
	}
	if errObj := decMap(t, w)["error"].(map[string]any); errObj["code"] != "reauth_required" {
		t.Fatalf("错误码应为 reauth_required: %v", errObj["code"])
	}
	if f.saved != nil {
		t.Fatalf("替身不应被调用")
	}
}

func TestAdminSaveDomainsOK(t *testing.T) {
	f := &fakeDomainSvc{}
	e := domainEngine(f, passAuth, nil)
	ug := uuid.New().String()
	body := `{"groups":[{"name":"缺省","is_default":true,"domains":[{"base_url":"https://sub-a.example.com","weight":2}],"user_group_ids":["` + ug + `"]}]}`
	w := do(e, http.MethodPut, "/v1/subscription-domains", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if len(f.saved) != 1 {
		t.Fatalf("saved len %d", len(f.saved))
	}
	g := f.saved[0]
	if g.Name != "缺省" || !g.IsDefault {
		t.Fatalf("group: %+v", g)
	}
	if len(g.Domains) != 1 || g.Domains[0].BaseURL != "https://sub-a.example.com" || g.Domains[0].Weight != 2 {
		t.Fatalf("domains: %+v", g.Domains)
	}
	if len(g.UserGroupIDs) != 1 || g.UserGroupIDs[0].String() != ug {
		t.Fatalf("user_group_ids: %v", g.UserGroupIDs)
	}
}

func TestAdminSaveDomainsEmptyGroups(t *testing.T) {
	f := &fakeDomainSvc{}
	e := domainEngine(f, passAuth, nil)
	w := do(e, http.MethodPut, "/v1/subscription-domains", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if f.saved == nil || len(f.saved) != 0 {
		t.Fatalf("groups 缺省应收到空切片: %+v", f.saved)
	}
}

func TestAdminSaveDomainsBadUserGroupID(t *testing.T) {
	f := &fakeDomainSvc{}
	e := domainEngine(f, passAuth, nil)
	body := `{"groups":[{"name":"缺省","domains":[{"base_url":"https://sub-a.example.com"}],"user_group_ids":["x"]}]}`
	w := do(e, http.MethodPut, "/v1/subscription-domains", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	errObj := decMap(t, w)["error"].(map[string]any)
	fields := errObj["fields"].(map[string]any)
	if fields["groups"] != "用户组 id 格式不正确" {
		t.Fatalf("fields: %v", fields)
	}
	if f.saved != nil {
		t.Fatalf("替身不应被调用")
	}
}

func TestAdminSaveDomainsUnknownField(t *testing.T) {
	f := &fakeDomainSvc{}
	e := domainEngine(f, passAuth, nil)
	w := do(e, http.MethodPut, "/v1/subscription-domains", `{"unknown":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应 400: %d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminSaveDomainsServiceError(t *testing.T) {
	f := &fakeDomainSvc{saveErr: errs.Invalid(map[string]string{"groups": "只能有一个缺省组"})}
	e := domainEngine(f, passAuth, nil)
	w := do(e, http.MethodPut, "/v1/subscription-domains", `{"groups":[]}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	errObj := decMap(t, w)["error"].(map[string]any)
	fields := errObj["fields"].(map[string]any)
	if fields["groups"] != "只能有一个缺省组" {
		t.Fatalf("fields: %v", fields)
	}
}

// probeEngine 建门户探测路由引擎；auth 为 nil 时不带认证（未登录）。
func probeEngine(svc Service, lim *ratelimit.Limiter, auth gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors(), httpx.SecurityHeaders(false))
	if auth == nil {
		auth = func(c *gin.Context) {
			httpx.Fail(c, errs.New(errs.Unauthorized))
		}
	}
	kit := route.Kit{
		RequireAuth:  auth,
		RecentReauth: func(c *gin.Context) { c.Next() },
		Idempotency:  func(string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
		Switch:       func(string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
	}
	New(svc, lim, 0).RegisterPublic(e.Group("/v1"), kit)
	return e
}

func doProbe(e *gin.Engine, method, path, body string, hdr func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if hdr != nil {
		hdr(req)
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func TestProbeTicket(t *testing.T) {
	f := &fakeDomainSvc{ticket: "tk"}
	e := probeEngine(f, nil, passAuth)
	w := doProbe(e, http.MethodPost, "/v1/sub-probe/ticket", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("已登录应 200: %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ticket":"tk"`) {
		t.Fatalf("body=%s", w.Body.String())
	}
	e2 := probeEngine(f, nil, nil)
	w2 := doProbe(e2, http.MethodPost, "/v1/sub-probe/ticket", "", nil)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %d", w2.Code)
	}
}

func TestProbeFrameCSP(t *testing.T) {
	f := &fakeDomainSvc{frameDomains: []string{
		"https://a.example.test",
		"https://b.example.test",
		`https://bad.example.test/x"`,
	}}
	e := probeEngine(f, nil, passAuth)
	w := doProbe(e, http.MethodGet, "/v1/sub-probe/frame?t=tk", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type: %q", ct)
	}
	if f.gotFrameTicket != "tk" {
		t.Fatalf("gotFrameTicket=%q", f.gotFrameTicket)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' https://a.example.test https://b.example.test;") {
		t.Fatalf("CSP connect-src: %q", csp)
	}
	if strings.Contains(csp, "bad.example.test") {
		t.Fatalf("CSP 不应含 bad: %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Fatalf("CSP: %q", csp)
	}
	if !strings.Contains(csp, "script-src 'sha256-") {
		t.Fatalf("CSP: %q", csp)
	}
	if w.Header().Get("X-Frame-Options") != "" {
		t.Fatalf("X-Frame-Options 应被去掉: %q", w.Header().Get("X-Frame-Options"))
	}
	body := w.Body.String()
	if !strings.Contains(body, `"domains":["https://a.example.test","https://b.example.test"]`) {
		t.Fatalf("body domains: %s", body)
	}
	if !strings.Contains(body, probeFrameScript) {
		t.Fatalf("body 应含脚本原文")
	}
	sum := sha256.Sum256([]byte(probeFrameScript))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if probeFrameScriptSrc != want {
		t.Fatalf("probeFrameScriptSrc=%q want %q", probeFrameScriptSrc, want)
	}
}

func TestProbeFrameRejects(t *testing.T) {
	f := &fakeDomainSvc{}
	e := probeEngine(f, nil, passAuth)
	w := doProbe(e, http.MethodGet, "/v1/sub-probe/frame", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("不带 t 应 401: %d", w.Code)
	}
	long := strings.Repeat("a", 600)
	w2 := doProbe(e, http.MethodGet, "/v1/sub-probe/frame?t="+long, "", nil)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("过长 t 应 401: %d", w2.Code)
	}
	if f.gotFrameTicket != "" {
		t.Fatalf("过长 t 不应进 service: got %q", f.gotFrameTicket)
	}
	f.frameErr = errs.Newf(errs.Unauthorized, "探测票据无效或已过期")
	w3 := doProbe(e, http.MethodGet, "/v1/sub-probe/frame?t=tk", "", nil)
	if w3.Code != http.StatusUnauthorized {
		t.Fatalf("service 拒票据应 401: %d", w3.Code)
	}
}

func TestProbeReport(t *testing.T) {
	f := &fakeDomainSvc{}
	e := probeEngine(f, nil, passAuth)
	body := `{"ticket":"tk","results":[{"base_url":"https://a.example.test","ok":true},{"base_url":"https://b.example.test","ok":false}]}`
	w := doProbe(e, http.MethodPost, "/v1/sub-probe/report", body, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if f.gotReportTicket != "tk" {
		t.Fatalf("gotReportTicket=%q", f.gotReportTicket)
	}
	if len(f.reported) != 2 || f.reported[0].BaseURL != "https://a.example.test" || !f.reported[0].OK ||
		f.reported[1].BaseURL != "https://b.example.test" || f.reported[1].OK {
		t.Fatalf("reported: %+v", f.reported)
	}
	var many strings.Builder
	many.WriteString(`{"ticket":"tk","results":[`)
	for i := 0; i < 11; i++ {
		if i > 0 {
			many.WriteByte(',')
		}
		many.WriteString(`{"base_url":"https://a.example.test","ok":true}`)
	}
	many.WriteString(`]}`)
	w2 := doProbe(e, http.MethodPost, "/v1/sub-probe/report", many.String(), nil)
	if w2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("11 条应 422: %d body=%s", w2.Code, w2.Body.String())
	}
	w3 := doProbe(e, http.MethodPost, "/v1/sub-probe/report", `{"ticket":"tk","unknown":1,"results":[]}`, nil)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应 400: %d body=%s", w3.Code, w3.Body.String())
	}
}

func TestSubProbeCORS(t *testing.T) {
	portal := "https://portal.example.test"
	f := &fakeDomainSvc{origin: portal}
	e := probeEngine(f, nil, passAuth)
	w := doProbe(e, http.MethodGet, "/v1/sub-probe", "", func(req *http.Request) {
		req.Header.Set("Origin", portal)
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != portal {
		t.Fatalf("ACAO: %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if !strings.Contains(w.Header().Get("Vary"), "Origin") {
		t.Fatalf("Vary: %q", w.Header().Get("Vary"))
	}
	w2 := doProbe(e, http.MethodGet, "/v1/sub-probe", "", func(req *http.Request) {
		req.Header.Set("Origin", "https://evil.example.test")
	})
	if w2.Code != http.StatusNoContent || w2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("evil: status=%d ACAO=%q", w2.Code, w2.Header().Get("Access-Control-Allow-Origin"))
	}
	w3 := doProbe(e, http.MethodGet, "/v1/sub-probe", "", nil)
	if w3.Code != http.StatusNoContent || w3.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("无 Origin: ACAO=%q", w3.Header().Get("Access-Control-Allow-Origin"))
	}
	w4 := doProbe(e, http.MethodHead, "/v1/sub-probe", "", nil)
	if w4.Code != http.StatusNoContent {
		t.Fatalf("HEAD status %d", w4.Code)
	}
}

func TestSubProbeRateLimitedKeepsCORS(t *testing.T) {
	portal := "https://portal.example.test"
	f := &fakeDomainSvc{origin: portal}
	_, rdb := testkit.Redis(t)
	lim := ratelimit.New(rdb)
	e := probeEngine(f, lim, passAuth)
	path := "/v1/sub-probe"
	var last *httptest.ResponseRecorder
	for i := 0; i < 61; i++ {
		last = doProbe(e, http.MethodGet, path, "", func(req *http.Request) {
			req.RemoteAddr = "203.0.113.9:1234"
			req.Header.Set("Origin", portal)
		})
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("第 61 次应 429: %d", last.Code)
	}
	if last.Header().Get("Access-Control-Allow-Origin") != portal {
		t.Fatalf("429 也应带 ACAO: %q", last.Header().Get("Access-Control-Allow-Origin"))
	}
}
