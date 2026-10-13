package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// fakeSvc 只实现用例用到的方法；其余方法走嵌入的 nil 接口，被调到会 panic（说明路由接错了）。
type fakeSvc struct {
	Service
	start      service.RegistrationStart
	lastFilter service.UserFilter
	rows       []service.UserRow
	changed    bool
}

func (f *fakeSvc) StartRegistration(context.Context, string, string) (service.RegistrationStart, error) {
	return f.start, nil
}

func (f *fakeSvc) Profile(_ context.Context, id uuid.UUID) (service.Profile, error) {
	return service.Profile{ID: id, Email: "me@example.test", Status: "active",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 8*3600))}, nil
}

func (f *fakeSvc) ChangePassword(context.Context, string, string) error {
	f.changed = true
	return nil
}

func (f *fakeSvc) ListUsers(_ context.Context, fl service.UserFilter, _, _ int) ([]service.UserRow, int64, error) {
	f.lastFilter = fl
	return f.rows, int64(len(f.rows)), nil
}

func (f *fakeSvc) ListGroups(context.Context) ([]service.Group, error) { return nil, nil }

func (f *fakeSvc) UserDetail(_ context.Context, id uuid.UUID) (service.UserDetail, error) {
	return service.UserDetail{UserRow: service.UserRow{ID: id, Email: "u@example.test"}, ReferralCount: 3}, nil
}

// newEngine 建一个挂好 identity 路由的引擎；principal 非 nil 时模拟已登录。
func newEngine(svc Service, admin bool, p *authn.Principal) *gin.Engine {
	return newEngineWith(New(svc, nil, 0), admin, p)
}

func newEngineWith(h *Handler, admin bool, p *authn.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	if p != nil {
		e.Use(func(c *gin.Context) { authn.Set(c, *p); c.Next() })
	}
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: pass,
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	if admin {
		h.RegisterAdmin(e.Group("/v1"), kit)
	} else {
		h.RegisterPublic(e.Group("/v1"), kit)
	}
	return e
}

func do(e *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func routesOf(e *gin.Engine) []string {
	var out []string
	for _, r := range e.Routes() {
		out = append(out, r.Method+" "+r.Path)
	}
	sort.Strings(out)
	return out
}

func TestRoutes(t *testing.T) {
	pub := routesOf(newEngine(&fakeSvc{}, false, nil))
	wantPub := []string{
		"GET /v1/me", "GET /v1/me/invite",
		"POST /v1/auth/password-reset/complete", "POST /v1/auth/password-reset/start",
		"POST /v1/auth/register/complete", "POST /v1/auth/register/start",
		"POST /v1/me/password",
	}
	if strings.Join(pub, "\n") != strings.Join(wantPub, "\n") {
		t.Fatalf("门户路由:\n%s", strings.Join(pub, "\n"))
	}
	adm := routesOf(newEngine(&fakeSvc{}, true, nil))
	wantAdm := []string{
		"DELETE /v1/user-groups/:id",
		"GET /v1/access-log", "GET /v1/ip-clusters",
		"GET /v1/user-groups", "GET /v1/users", "GET /v1/users/:id", "GET /v1/users/:id/profile",
		"GET /v1/users/bulk/export", "GET /v1/users/bulk/generate/jobs",
		"GET /v1/users/bulk/generate/jobs/:id", "GET /v1/users/bulk/generate/jobs/:id/result",
		"POST /v1/ip-clusters/:key/disable-accounts", "POST /v1/ip-clusters/:key/review",
		"POST /v1/me/password", "POST /v1/user-groups", "POST /v1/user-groups/:id",
		"POST /v1/users/:id/group", "POST /v1/users/:id/reset-password", "POST /v1/users/:id/status",
		"POST /v1/users/bulk/generate", "POST /v1/users/bulk/preview",
	}
	if strings.Join(adm, "\n") != strings.Join(wantAdm, "\n") {
		t.Fatalf("后台路由:\n%s", strings.Join(adm, "\n"))
	}
}

// 后台不分权限，但每条后台路由都必须挂 RequireAuth：未登录（或门户主体，由引擎级 DomainGuard 拦成未登录）一律 401。
func TestAdminRoutesRequireAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	deny := func(c *gin.Context) { httpx.Fail(c, errs.New(errs.Unauthorized)) }
	New(&fakeSvc{}, nil, 0).RegisterAdmin(e.Group("/v1"), route.Kit{
		RequireAuth:  deny,
		RecentReauth: pass,
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	})
	for _, r := range e.Routes() {
		path := strings.ReplaceAll(r.Path, ":id", uuid.NewString())
		w := do(e, r.Method, path, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录 status %d", r.Method, r.Path, w.Code)
		}
	}
}

func TestRegisterStartShape(t *testing.T) {
	exp := time.Date(2026, 5, 6, 7, 8, 9, 0, time.FixedZone("x", 8*3600))
	f := &fakeSvc{start: service.RegistrationStart{Token: "tok", ExpiresAt: exp, VerificationRequired: true}}
	w := do(newEngine(f, false, nil), http.MethodPost, "/v1/auth/register/start", `{"email":"a@b.co"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["registration_token"] != "tok" || got["expires_at"] != "2026-05-05T23:08:09Z" || got["verification_required"] != true {
		t.Fatalf("响应 %v", got)
	}
	if _, ok := got["dev_code"]; ok {
		t.Fatalf("dev_code 为空时不应输出: %v", got)
	}
	if got["message"] == "" {
		t.Fatal("缺 message")
	}
}

func TestMeShapeAndAuth(t *testing.T) {
	if w := do(newEngine(&fakeSvc{}, false, nil), http.MethodGet, "/v1/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 status %d", w.Code)
	}
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudiencePublic}
	w := do(newEngine(&fakeSvc{}, false, p), http.MethodGet, "/v1/me", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Contains(body, `"permissions"`) || !strings.Contains(body, `"created_at":"2026-01-01T19:04:05Z"`) ||
		!strings.Contains(body, `"user_id":"`+p.UserID.String()+`"`) {
		t.Fatalf("响应 %s", body)
	}
}

func TestChangePasswordAdminAsksReauth(t *testing.T) {
	p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}
	f := &fakeSvc{}
	w := do(newEngine(f, true, p), http.MethodPost, "/v1/me/password", `{"old_password":"a","new_password":"b"}`)
	if w.Code != http.StatusOK || !f.changed || !strings.Contains(w.Body.String(), `"reauthenticate":true`) {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	p.Audience = authn.AudiencePublic
	w = do(newEngine(f, false, p), http.MethodPost, "/v1/me/password", `{"old_password":"a","new_password":"b"}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "reauthenticate") {
		t.Fatalf("门户 status %d %s", w.Code, w.Body)
	}
}

func TestListUsersQuery(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, nil)
	w := do(e, http.MethodGet, "/v1/users?status=active,%20banned,,&group_id=none&q=abc", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"users":[]`) {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if strings.Join(f.lastFilter.Statuses, "|") != "active|banned" || !f.lastFilter.NoGroup || f.lastFilter.Query != "abc" {
		t.Fatalf("筛选 %+v", f.lastFilter)
	}
	if w := do(e, http.MethodGet, "/v1/users?group_id=bogus", ""); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏 group_id status %d", w.Code)
	}
	if w := do(e, http.MethodGet, "/v1/users/not-a-uuid", ""); w.Code != http.StatusNotFound {
		t.Fatalf("坏 id status %d", w.Code)
	}
	if w := do(e, http.MethodGet, "/v1/user-groups", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"groups":[]`) {
		t.Fatalf("用户组 status %d %s", w.Code, w.Body)
	}
}

// 改密接口按账号、按 IP 限流：每分钟 authPerMin 次，超出 429，门户与后台都挂。
func TestMePasswordRateLimited(t *testing.T) {
	for _, admin := range []bool{false, true} {
		_, rdb := testkit.Redis(t)
		aud := authn.AudiencePublic
		if admin {
			aud = authn.AudienceAdmin
		}
		p := &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: aud}
		e := newEngineWith(New(&fakeSvc{}, ratelimit.New(rdb), 2), admin, p)
		for i := 0; i < 2; i++ {
			if w := do(e, http.MethodPost, "/v1/me/password", `{"old_password":"a","new_password":"b"}`); w.Code != http.StatusOK {
				t.Fatalf("admin=%v 第 %d 次 status %d %s", admin, i+1, w.Code, w.Body)
			}
		}
		if w := do(e, http.MethodPost, "/v1/me/password", `{"old_password":"a","new_password":"b"}`); w.Code != http.StatusTooManyRequests {
			t.Fatalf("admin=%v 第 3 次应 429，实际 %d", admin, w.Code)
		}
	}
}

func TestUserDetailStats(t *testing.T) {
	e := newEngine(&fakeSvc{}, true, nil)
	w := do(e, http.MethodGet, "/v1/users/"+uuid.NewString(), "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"stats":{"referral_count":3}`) {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
}
