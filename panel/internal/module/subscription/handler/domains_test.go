package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// fakeDomainSvc 是后台订阅域名分组接口的替身；其余方法走嵌入的 nil 接口，被调到会 panic。
type fakeDomainSvc struct {
	Service
	cfg     service.DomainConfigView
	saved   []service.DomainGroupInput
	saveErr error
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
