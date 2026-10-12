package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestBearerParsing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		in  string
		out string
	}{
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"},
		{"BEARER  abc ", "abc"},
		{"Basic abc", ""},
		{"Bearer", ""},
		{"", ""},
	}
	for _, tc := range cases {
		rec := httptestNewRequestWithAuth(tc.in)
		got := bearerToken(rec.ctx)
		if got != tc.out {
			t.Fatalf("Authorization %q => %q want %q", tc.in, got, tc.out)
		}
	}
}

// httptestNewRequestWithAuth 构造带 Authorization 的 gin 上下文，仅用于 bearerToken 单测。
func httptestNewRequestWithAuth(auth string) struct{ ctx *gin.Context } {
	w, _ := gin.CreateTestContext(httptest.NewRecorder())
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w.Request = req
	return struct{ ctx *gin.Context }{ctx: w}
}

func TestAuthenticateInvalidToken(t *testing.T) {
	e := newEngine(&fakeAuth{}, authn.AudienceAdmin)
	rec := doJSON(e, http.MethodGet, "/v1/me", "", map[string]string{
		"Authorization": "Bearer unknown",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown bearer status %d", rec.Code)
	}

	fake := &fakeAuth{authErr: errs.New(errs.Unavailable)}
	e = newEngine(fake, authn.AudienceAdmin)
	rec = doJSON(e, http.MethodGet, "/v1/me", "", map[string]string{
		"Authorization": "Bearer any",
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status %d", rec.Code)
	}
}

func TestDomainGuard(t *testing.T) {
	uid := uuid.New()
	fake := &fakeAuth{
		principals: map[string]authn.Principal{
			"pub-tok": {UserID: uid, Audience: authn.AudiencePublic},
		},
	}
	e := newEngine(fake, authn.AudienceAdmin)
	rec := doJSON(e, http.MethodGet, "/v1/me", "", map[string]string{
		"Authorization": "Bearer pub-tok",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func newAdminProbeEngine(fake Auth) (*gin.Engine, route.Kit, *Handler) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.Errors())
	h := New(fake, nil, Options{AuthPerMin: 10, SecureCookie: true})
	e.Use(h.Authenticate(authn.AudienceAdmin), h.DomainGuard(authn.AudienceAdmin))
	k := route.Kit{
		RequireAuth:  h.RequireAuth(),
		RecentReauth: h.RecentReauth(),
	}
	v1 := e.Group("/v1")
	v1.GET("/probe/auth", k.RequireAuth, probeOK)
	v1.POST("/probe/reauth", k.RequireAuth, k.RecentReauth, probeOK)
	return e, k, h
}

// 后台不分权限：后台引擎上有效的后台会话就能过 RequireAuth；未登录与门户主体一律 401。
func TestRequireAuthAdmin(t *testing.T) {
	uid := uuid.New()
	fake := &fakeAuth{
		principals: map[string]authn.Principal{
			"adm": {UserID: uid, Audience: authn.AudienceAdmin},
			"pub": {UserID: uid, Audience: authn.AudiencePublic},
		},
	}
	e, _, _ := newAdminProbeEngine(fake)

	rec := doJSON(e, http.MethodGet, "/v1/probe/auth", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon status %d", rec.Code)
	}
	if errCode(t, rec) != string(errs.Unauthorized) {
		t.Fatalf("anon code %s", errCode(t, rec))
	}

	rec = doJSON(e, http.MethodGet, "/v1/probe/auth", "", map[string]string{"Authorization": "Bearer pub"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("public principal status %d", rec.Code)
	}

	rec = doJSON(e, http.MethodGet, "/v1/probe/auth", "", map[string]string{"Authorization": "Bearer adm"})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecentReauth(t *testing.T) {
	uid := uuid.New()
	bearer := "reauth-probe"
	fake := &fakeAuth{
		principals: map[string]authn.Principal{
			bearer: {UserID: uid, Audience: authn.AudienceAdmin, ReauthAt: time.Time{}},
		},
	}
	e, _, _ := newAdminProbeEngine(fake)
	rec := doJSON(e, http.MethodPost, "/v1/probe/reauth", "", map[string]string{
		"Authorization": "Bearer " + bearer,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("zero reauth status %d", rec.Code)
	}
	if errCode(t, rec) != string(errs.ReauthRequired) {
		t.Fatalf("code %s", errCode(t, rec))
	}

	fake.principals[bearer] = authn.Principal{
		UserID: uid, Audience: authn.AudienceAdmin, ReauthAt: time.Now(),
	}
	e, _, _ = newAdminProbeEngine(fake)
	rec = doJSON(e, http.MethodPost, "/v1/probe/reauth", "", map[string]string{
		"Authorization": "Bearer " + bearer,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("recent reauth status %d %s", rec.Code, rec.Body.String())
	}
}
