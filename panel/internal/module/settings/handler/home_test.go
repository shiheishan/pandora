package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

func (f *fakeSettings) Home(ctx context.Context) (service.Home, error) {
	return service.Home{Enabled: true, FAQ: []service.HomeFAQ{}}, nil
}

func (f *fakeSettings) SetHome(ctx context.Context, u service.HomeUpdate) error {
	return nil
}

type fakeHome struct {
	fakeSettings
	home service.Home
	got  service.HomeUpdate
}

func (f *fakeHome) Home(ctx context.Context) (service.Home, error) {
	return f.home, nil
}

func (f *fakeHome) SetHome(ctx context.Context, u service.HomeUpdate) error {
	f.got = u
	if u.Enabled != nil {
		f.home.Enabled = *u.Enabled
	}
	if u.Intro != nil {
		f.home.Intro = *u.Intro
	}
	if u.FAQ != nil {
		f.home.FAQ = *u.FAQ
	}
	return nil
}

func TestGetHomeRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeHome{home: service.Home{Enabled: true, FAQ: []service.HomeFAQ{}}}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))

	rec := do(e, http.MethodGet, "/v1/settings/home", "", nil)
	if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthorized" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}
}

func TestGetHomeOK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeHome{home: service.Home{Enabled: true, FAQ: []service.HomeFAQ{}}}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))

	rec := do(e, http.MethodGet, "/v1/settings/home", "", map[string]string{"X-Test-Auth": "1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	want := `{"enabled":true,"intro":"","faq":[]}`
	if rec.Body.String() != want {
		t.Fatalf("body %s want %s", rec.Body.String(), want)
	}
}

func TestSetHomeRequiresReauth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeHome{home: service.Home{Enabled: true, FAQ: []service.HomeFAQ{}}}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))

	rec := do(e, http.MethodPost, "/v1/settings/home", `{"intro":"x"}`, map[string]string{"X-Test-Auth": "1"})
	if rec.Code != http.StatusForbidden || errCode(t, rec) != "reauth_required" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}
}

func TestSetHomePartialUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeHome{home: service.Home{Enabled: true, FAQ: []service.HomeFAQ{}}}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))
	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}

	body := `{"intro":"欢迎光临虚构加速","faq":[{"q":"能试用吗？","a":"可以"}]}`
	rec := do(e, http.MethodPost, "/v1/settings/home", body, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fake.got.Intro == nil || *fake.got.Intro != "欢迎光临虚构加速" {
		t.Fatalf("intro got %+v", fake.got.Intro)
	}
	if fake.got.FAQ == nil || len(*fake.got.FAQ) != 1 {
		t.Fatalf("faq got %+v", fake.got.FAQ)
	}
	if (*fake.got.FAQ)[0].Q != "能试用吗？" || (*fake.got.FAQ)[0].A != "可以" {
		t.Fatalf("faq item %+v", (*fake.got.FAQ)[0])
	}
	if fake.got.Enabled != nil {
		t.Fatalf("enabled should be nil, got %+v", fake.got.Enabled)
	}
}

func TestSetHomeUnknownField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeHome{home: service.Home{Enabled: true, FAQ: []service.HomeFAQ{}}}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))
	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}

	rec := do(e, http.MethodPost, "/v1/settings/home", `{"x":1}`, hdrs)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}
