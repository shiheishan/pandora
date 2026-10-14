package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/auth/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type fakeAuth struct {
	principals map[string]authn.Principal

	loginAud   string
	loginEmail string
	loginPass  string
	loginOut   service.Tokens
	loginErr   error

	refreshAud   string
	refreshRaw   string
	refreshCalls int
	refreshOut   service.Tokens
	refreshErr   error

	logoutAud string
	logoutRaw string
	logoutErr error

	quickConsumeToken string
	quickConsumeOut   service.Tokens
	quickConsumeErr   error

	listSessionsOut []service.SessionInfo
	listSessionsErr error

	revokeID  string
	revokeErr error

	issueQuickOut service.QuickLoginLink
	issueQuickErr error

	reauthPass string
	reauthOut  service.Tokens
	reauthErr  error

	adminProfile service.AdminProfile
	adminProfErr error

	authErr error
}

func (f *fakeAuth) Login(ctx context.Context, audience, email, password string) (service.Tokens, error) {
	f.loginAud, f.loginEmail, f.loginPass = audience, email, password
	if f.loginErr != nil {
		return service.Tokens{}, f.loginErr
	}
	return f.loginOut, nil
}

func (f *fakeAuth) ConsumeQuickLogin(ctx context.Context, token string) (service.Tokens, error) {
	f.quickConsumeToken = token
	if f.quickConsumeErr != nil {
		return service.Tokens{}, f.quickConsumeErr
	}
	return f.quickConsumeOut, nil
}

func (f *fakeAuth) Refresh(ctx context.Context, audience, raw string) (service.Tokens, error) {
	f.refreshCalls++
	f.refreshAud, f.refreshRaw = audience, raw
	if f.refreshErr != nil {
		return service.Tokens{}, f.refreshErr
	}
	return f.refreshOut, nil
}

func (f *fakeAuth) Logout(ctx context.Context, audience, rawRefresh string) error {
	f.logoutAud, f.logoutRaw = audience, rawRefresh
	return f.logoutErr
}

func (f *fakeAuth) Authenticate(ctx context.Context, audience, bearer string) (authn.Principal, error) {
	if f.authErr != nil {
		return authn.Principal{}, f.authErr
	}
	if f.principals == nil {
		return authn.Principal{}, errs.New(errs.Unauthorized)
	}
	p, ok := f.principals[bearer]
	if !ok {
		return authn.Principal{}, errs.New(errs.Unauthorized)
	}
	return p, nil
}

func (f *fakeAuth) AdminProfile(ctx context.Context) (service.AdminProfile, error) {
	if f.adminProfErr != nil {
		return service.AdminProfile{}, f.adminProfErr
	}
	return f.adminProfile, nil
}

func (f *fakeAuth) ListSessions(ctx context.Context) ([]service.SessionInfo, error) {
	if f.listSessionsErr != nil {
		return nil, f.listSessionsErr
	}
	return f.listSessionsOut, nil
}

func (f *fakeAuth) RevokeSession(ctx context.Context, sid string) error {
	f.revokeID = sid
	return f.revokeErr
}

func (f *fakeAuth) IssueQuickLogin(ctx context.Context) (service.QuickLoginLink, error) {
	if f.issueQuickErr != nil {
		return service.QuickLoginLink{}, f.issueQuickErr
	}
	return f.issueQuickOut, nil
}

func (f *fakeAuth) Reauth(ctx context.Context, password string) (service.Tokens, error) {
	f.reauthPass = password
	if f.reauthErr != nil {
		return service.Tokens{}, f.reauthErr
	}
	return f.reauthOut, nil
}

func (f *fakeAuth) StartMailLogin(ctx context.Context, email string) error {
	return nil
}

func (f *fakeAuth) ConsumeMailLogin(ctx context.Context, token string) (service.Tokens, error) {
	return service.Tokens{}, nil
}

func newEngine(f *fakeAuth, aud string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.Errors())
	h := New(f, nil, Options{AuthPerMin: 10, SecureCookie: true})
	e.Use(h.Authenticate(aud), h.DomainGuard(aud))
	k := route.Kit{
		RequireAuth:  h.RequireAuth(),
		RecentReauth: h.RecentReauth(),
	}
	v1 := e.Group("/v1")
	if aud == authn.AudiencePublic {
		h.RegisterPublic(v1, k)
	} else {
		h.RegisterAdmin(v1, k)
		v1.POST("/probe/reauth", k.RequireAuth, k.RecentReauth, probeOK)
	}
	return e
}

func probeOK(c *gin.Context) {
	httpx.OK(c, gin.H{"ok": true})
}

func doJSON(e http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

func errMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error.Message
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error.Code
}

func findSetCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, h := range rec.Result().Cookies() {
		if h.Name == name {
			return h
		}
	}
	for _, line := range rec.Result().Header.Values("Set-Cookie") {
		if c, err := http.ParseSetCookie(line); err == nil && c.Name == name {
			return c
		}
	}
	return nil
}

func TestPublicLoginShape(t *testing.T) {
	uid := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	exp := time.Now().UTC().Add(time.Hour)
	fake := &fakeAuth{
		loginOut: service.Tokens{
			AccessToken:    "at",
			ExpiresIn:      900,
			UserID:         uid,
			Refresh:        "sid.secret",
			RefreshExpires: exp,
		},
	}
	e := newEngine(fake, authn.AudiencePublic)
	rec := doJSON(e, http.MethodPost, "/v1/auth/login",
		`{"email":"u@example.com","password":"secret"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantKeys := []string{"access_token", "token_type", "expires_in", "user_id"}
	if len(m) != len(wantKeys) {
		t.Fatalf("keys %v want exactly %v", mapKeys(m), wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing key %q", k)
		}
	}
	if fake.loginAud != authn.AudiencePublic {
		t.Fatalf("audience %q", fake.loginAud)
	}
	c := findSetCookie(rec, publicRefreshCookie)
	if c == nil {
		t.Fatal("no pandora_rt cookie")
	}
	if c.Path != refreshCookiePath || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie attrs: path=%q httpOnly=%v secure=%v sameSite=%v", c.Path, c.HttpOnly, c.Secure, c.SameSite)
	}
}

// assertKeys 断言 JSON 对象恰好有这些键，返回解出的对象。
func assertKeys(t *testing.T, raw []byte, want ...string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(m) != len(want) {
		t.Fatalf("keys %v want exactly %v", mapKeys(m), want)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing key %q (have %v)", k, mapKeys(m))
		}
	}
	return m
}

func mapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestAdminLoginShape(t *testing.T) {
	uid := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	fake := &fakeAuth{
		loginOut: service.Tokens{
			AccessToken:    "at",
			ExpiresIn:      900,
			UserID:         uid,
			Refresh:        "sid.secret",
			RefreshExpires: time.Now().UTC().Add(time.Hour),
		},
	}
	e := newEngine(fake, authn.AudienceAdmin)
	rec := doJSON(e, http.MethodPost, "/v1/auth/login",
		`{"email":"a@example.com","password":"secret"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	// 后台不分权限：登录响应与门户同形，不带 permissions
	m := assertKeys(t, rec.Body.Bytes(), "access_token", "token_type", "expires_in", "user_id")
	if string(m["token_type"]) != `"Bearer"` || string(m["user_id"]) != `"`+uid.String()+`"` {
		t.Fatalf("body %s", rec.Body.String())
	}
	if findSetCookie(rec, adminRefreshCookie) == nil {
		t.Fatal("no pandora_admin_rt cookie")
	}
}

func TestLoginValidation(t *testing.T) {
	e := newEngine(&fakeAuth{}, authn.AudiencePublic)
	rec := doJSON(e, http.MethodPost, "/v1/auth/login", `{"email":"u@example.com"}`, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing password status %d", rec.Code)
	}
	rec = doJSON(e, http.MethodPost, "/v1/auth/login",
		`{"email":"a\u0000@x.com","password":"p"}`, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("nul email status %d", rec.Code)
	}
	rec = doJSON(e, http.MethodPost, "/v1/auth/login",
		`{"email":"u@example.com","password":"p","extra":1}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status %d", rec.Code)
	}
}

func TestLoginErrorPassthrough(t *testing.T) {
	fake := &fakeAuth{
		loginErr: errs.Newf(errs.Unauthorized, "邮箱或密码不正确"),
	}
	e := newEngine(fake, authn.AudiencePublic)
	rec := doJSON(e, http.MethodPost, "/v1/auth/login",
		`{"email":"u@example.com","password":"bad"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
	if msg := errMessage(t, rec); msg != "邮箱或密码不正确" {
		t.Fatalf("message %q", msg)
	}
	if findSetCookie(rec, publicRefreshCookie) != nil {
		t.Fatal("unexpected Set-Cookie")
	}
}

func TestRefresh(t *testing.T) {
	exp := time.Now().UTC().Add(time.Hour)
	e := newEngine(&fakeAuth{}, authn.AudiencePublic)

	rec := doJSON(e, http.MethodPost, "/v1/auth/refresh", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie status %d", rec.Code)
	}

	fake := &fakeAuth{
		refreshOut: service.Tokens{
			AccessToken:    "new-at",
			ExpiresIn:      900,
			Refresh:        "sid.newsecret",
			RefreshExpires: exp,
		},
	}
	e = newEngine(fake, authn.AudiencePublic)
	rec = doJSON(e, http.MethodPost, "/v1/auth/refresh", "", map[string]string{
		"Cookie": publicRefreshCookie + "=old.raw",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh ok status %d body %s", rec.Code, rec.Body.String())
	}
	if c := findSetCookie(rec, publicRefreshCookie); c == nil || c.Value != "sid.newsecret" {
		t.Fatalf("cookie not updated: %v", c)
	}

	fake = &fakeAuth{
		refreshOut: service.Tokens{
			AccessToken: "at",
			ExpiresIn:   900,
			Refresh:     "",
		},
	}
	e = newEngine(fake, authn.AudiencePublic)
	rec = doJSON(e, http.MethodPost, "/v1/auth/refresh", "", map[string]string{
		"Cookie": publicRefreshCookie + "=old.raw",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("grace status %d", rec.Code)
	}
	if findSetCookie(rec, publicRefreshCookie) != nil {
		t.Fatal("grace should not Set-Cookie")
	}

	fake = &fakeAuth{refreshErr: errs.New(errs.Unauthorized)}
	e = newEngine(fake, authn.AudiencePublic)
	rec = doJSON(e, http.MethodPost, "/v1/auth/refresh", "", map[string]string{
		"Cookie": publicRefreshCookie + "=bad",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("401 status %d", rec.Code)
	}
	c := findSetCookie(rec, publicRefreshCookie)
	if c == nil || c.MaxAge != -1 {
		t.Fatalf("clear cookie: %v", c)
	}

	fake = &fakeAuth{}
	e = newEngine(fake, authn.AudiencePublic)
	rec = doJSON(e, http.MethodPost, "/v1/auth/refresh", "", map[string]string{
		"Sec-Fetch-Site": "cross-site",
		"Origin":         "https://evil.example",
		"Cookie":         publicRefreshCookie + "=x",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site status %d", rec.Code)
	}
	if fake.refreshCalls != 0 {
		t.Fatalf("refresh called %d times", fake.refreshCalls)
	}
}

func TestLogout(t *testing.T) {
	uid := uuid.New()
	fake := &fakeAuth{
		principals: map[string]authn.Principal{
			"admin-bearer": {UserID: uid, Audience: authn.AudiencePublic},
		},
	}
	e := newEngine(fake, authn.AudiencePublic)
	rec := doJSON(e, http.MethodPost, "/v1/auth/logout", "", map[string]string{
		"Authorization": "Bearer admin-bearer",
		"Cookie":        publicRefreshCookie + "=sid.raw",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if fake.logoutRaw != "sid.raw" {
		t.Fatalf("logout raw %q", fake.logoutRaw)
	}
	c := findSetCookie(rec, publicRefreshCookie)
	if c == nil || c.MaxAge != -1 {
		t.Fatalf("clear cookie %v", c)
	}

	fake = &fakeAuth{}
	e = newEngine(fake, authn.AudiencePublic)
	rec = doJSON(e, http.MethodPost, "/v1/auth/logout", "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("empty logout status %d", rec.Code)
	}
}

func TestSessionsShape(t *testing.T) {
	uid := uuid.New()
	bearer := "sess-bearer"
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeAuth{
		principals: map[string]authn.Principal{
			bearer: {UserID: uid, Audience: authn.AudiencePublic},
		},
		listSessionsOut: []service.SessionInfo{{
			ID: "s1", Current: true, UserAgent: "ua",
			CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
		}},
	}
	e := newEngine(fake, authn.AudiencePublic)
	rec := doJSON(e, http.MethodGet, "/v1/me/sessions", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon status %d", rec.Code)
	}
	rec = doJSON(e, http.MethodGet, "/v1/me/sessions", "", map[string]string{
		"Authorization": "Bearer " + bearer,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d %s", rec.Code, rec.Body.String())
	}
	var listBody struct {
		Sessions []struct {
			ID         string `json:"id"`
			Current    bool   `json:"current"`
			UserAgent  string `json:"user_agent"`
			CreatedAt  string `json:"created_at"`
			LastSeenAt string `json:"last_seen_at"`
			ExpiresAt  string `json:"expires_at"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listBody.Sessions) != 1 {
		t.Fatalf("sessions %v", listBody.Sessions)
	}
	s := listBody.Sessions[0]
	if s.ID != "s1" || !s.Current || s.UserAgent != "ua" {
		t.Fatalf("row %v", s)
	}
	for _, ts := range []string{s.CreatedAt, s.LastSeenAt, s.ExpiresAt} {
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Fatalf("time %q: %v", ts, err)
		}
	}

	rec = doJSON(e, http.MethodDelete, "/v1/me/sessions/s1", "", map[string]string{
		"Authorization": "Bearer " + bearer,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status %d", rec.Code)
	}
	var rev struct {
		Revoked bool `json:"revoked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rev); err != nil || !rev.Revoked {
		t.Fatalf("revoked body %s", rec.Body.String())
	}
	if fake.revokeID != "s1" {
		t.Fatalf("revoke id %q", fake.revokeID)
	}
}

func TestQuickLoginShape(t *testing.T) {
	uid := uuid.New()
	bearer := "ql-bearer"
	exp := time.Now().UTC().Add(10 * time.Minute)
	fake := &fakeAuth{
		principals: map[string]authn.Principal{
			bearer: {UserID: uid, Audience: authn.AudiencePublic},
		},
		issueQuickOut: service.QuickLoginLink{
			Token: "qtok", ExpiresAt: exp, ExpiresIn: 600,
		},
		quickConsumeOut: service.Tokens{
			AccessToken: "at", ExpiresIn: 900,
			Refresh: "r", RefreshExpires: exp,
		},
	}
	e := newEngine(fake, authn.AudiencePublic)
	rec := doJSON(e, http.MethodPost, "/v1/me/quick-login", "", map[string]string{
		"Authorization": "Bearer " + bearer,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("issue status %d", rec.Code)
	}
	var issue struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issue); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if issue.Token != "qtok" || issue.ExpiresIn != 600 {
		t.Fatalf("issue %v", issue)
	}
	if _, err := time.Parse(time.RFC3339, issue.ExpiresAt); err != nil {
		t.Fatalf("expires_at: %v", err)
	}

	rec = doJSON(e, http.MethodPost, "/v1/auth/quick-login", `{"token":"qtok"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("consume status %d %s", rec.Code, rec.Body.String())
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tok.AccessToken != "at" || tok.TokenType != "Bearer" || tok.ExpiresIn != 900 {
		t.Fatalf("token %v", tok)
	}
	if findSetCookie(rec, publicRefreshCookie) == nil {
		t.Fatal("no refresh cookie after quick-login")
	}
}

func TestAdminMeShape(t *testing.T) {
	uid := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	bearer := "admin-me"
	fake := &fakeAuth{
		principals: map[string]authn.Principal{
			bearer: {UserID: uid, Audience: authn.AudienceAdmin},
		},
		adminProfile: service.AdminProfile{
			UserID: uid, Email: "a@example.com", Reauthed: true,
		},
	}
	e := newEngine(fake, authn.AudienceAdmin)
	rec := doJSON(e, http.MethodGet, "/v1/me", "", map[string]string{
		"Authorization": "Bearer " + bearer,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	// 契约：恰好这四个键，没有 permissions、roles、kind
	m := assertKeys(t, rec.Body.Bytes(), "user_id", "email", "display_name", "reauthed")
	if string(m["display_name"]) != "null" {
		t.Fatalf("display_name %s", m["display_name"])
	}
	var body struct {
		UserID      string  `json:"user_id"`
		Email       string  `json:"email"`
		DisplayName *string `json:"display_name"`
		Reauthed    bool    `json:"reauthed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.UserID != uid.String() || body.Email != "a@example.com" {
		t.Fatalf("identity %v", body)
	}
	if body.DisplayName != nil {
		t.Fatalf("display_name %v", body.DisplayName)
	}
	if !body.Reauthed {
		t.Fatal("reauthed false")
	}
}

func TestReauth(t *testing.T) {
	uid := uuid.New()
	bearer := "reauth-bearer"
	fake := &fakeAuth{
		principals: map[string]authn.Principal{
			bearer: {UserID: uid, Audience: authn.AudienceAdmin},
		},
		reauthOut: service.Tokens{AccessToken: "new", ExpiresIn: 900},
	}
	e := newEngine(fake, authn.AudienceAdmin)
	rec := doJSON(e, http.MethodPost, "/v1/auth/reauth", `{"password":"x"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon status %d", rec.Code)
	}
	rec = doJSON(e, http.MethodPost, "/v1/auth/reauth", `{"password":"x"}`, map[string]string{
		"Authorization": "Bearer " + bearer,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if fake.reauthPass != "x" {
		t.Fatalf("password %q", fake.reauthPass)
	}
	var tok tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tok.AccessToken != "new" || tok.TokenType != "Bearer" || tok.ExpiresIn != 900 {
		t.Fatalf("token %v", tok)
	}
}

func TestLoginCrossSiteForbidden(t *testing.T) {
	cross := map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}
	for _, aud := range []string{authn.AudiencePublic, authn.AudienceAdmin} {
		fake := &fakeAuth{}
		e := newEngine(fake, aud)
		rec := doJSON(e, http.MethodPost, "/v1/auth/login", `{"email":"u@example.com","password":"p"}`, cross)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s login cross-site status %d", aud, rec.Code)
		}
		if fake.loginAud != "" {
			t.Fatalf("%s: 跨站请求不应调到 Login", aud)
		}
		if findSetCookie(rec, cookieName(aud)) != nil {
			t.Fatalf("%s: 跨站请求不应下发 cookie", aud)
		}
	}
	fake := &fakeAuth{}
	e := newEngine(fake, authn.AudiencePublic)
	rec := doJSON(e, http.MethodPost, "/v1/auth/quick-login", `{"token":"t"}`, cross)
	if rec.Code != http.StatusForbidden || fake.quickConsumeToken != "" {
		t.Fatalf("quick-login cross-site status %d token %q", rec.Code, fake.quickConsumeToken)
	}
	// 同源仍放行
	fake = &fakeAuth{loginOut: service.Tokens{AccessToken: "at", Refresh: "s.r", RefreshExpires: time.Now().Add(time.Hour)}}
	e = newEngine(fake, authn.AudiencePublic)
	rec = doJSON(e, http.MethodPost, "/v1/auth/login", `{"email":"u@example.com","password":"p"}`,
		map[string]string{"Sec-Fetch-Site": "same-origin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin login status %d", rec.Code)
	}
}
