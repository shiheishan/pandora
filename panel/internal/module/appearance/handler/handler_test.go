package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/aegis/internal/module/appearance/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeSvc struct {
	Service
	themes      []service.Theme
	slots       []service.Slot
	public      service.PublicAppearance
	lastSave    service.SaveThemeInput
	lastSlot    service.SaveSlotInput
	activate    string
	delete      string
	saveErr     error
	activateErr error
	deleteErr   error
	slotNotes   []string
}

func (f *fakeSvc) ListThemes(context.Context) ([]service.Theme, error) {
	return f.themes, nil
}

func (f *fakeSvc) SaveTheme(_ context.Context, in service.SaveThemeInput) ([]string, error) {
	f.lastSave = in
	if f.saveErr != nil {
		return nil, f.saveErr
	}
	return []string{"note1"}, nil
}

func (f *fakeSvc) ActivateTheme(_ context.Context, code string) error {
	f.activate = code
	return f.activateErr
}

func (f *fakeSvc) DeleteTheme(_ context.Context, code string) error {
	f.delete = code
	return f.deleteErr
}

func (f *fakeSvc) ListSlots(context.Context) ([]service.Slot, error) {
	return f.slots, nil
}

func (f *fakeSvc) SaveSlot(_ context.Context, in service.SaveSlotInput) ([]string, error) {
	f.lastSlot = in
	return f.slotNotes, nil
}

func (f *fakeSvc) Public(context.Context) (service.PublicAppearance, error) {
	return f.public, nil
}

func newEngine(svc Service, admin bool, p *authn.Principal, kit route.Kit) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	if p != nil {
		e.Use(func(c *gin.Context) { authn.Set(c, *p); c.Next() })
	}
	if kit.RequireAuth == nil {
		pass := func(c *gin.Context) { c.Next() }
		kit = route.Kit{
			RequireAuth:  pass,
			RecentReauth: pass,
			Idempotency:  func(string) gin.HandlerFunc { return pass },
		}
	}
	if kit.RecentReauth == nil {
		kit.RecentReauth = kit.RequireAuth
	}
	if kit.Idempotency == nil {
		kit.Idempotency = func(string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } }
	}
	h := New(svc)
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

func decMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v body=%s", err, w.Body.String())
	}
	return got
}

func errFields(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	got := decMap(t, w)
	errObj, _ := got["error"].(map[string]any)
	fields, _ := errObj["fields"].(map[string]any)
	return fields
}

func TestListThemesShape(t *testing.T) {
	id := uuid.New()
	f := &fakeSvc{
		themes: []service.Theme{{
			ID: id, Code: "paper", Name: "纸", IsBuiltin: true, IsActive: true,
			Tokens:   json.RawMessage(`{"light":{"--bg":"#fff"},"dark":{}}`),
			Branding: json.RawMessage(`{"tagline":"稳"}`),
		}},
	}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}, route.Kit{})
	w := do(e, http.MethodGet, "/v1/themes", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	got := decMap(t, w)
	themes, _ := got["themes"].([]any)
	if len(themes) != 1 {
		t.Fatalf("themes len %d", len(themes))
	}
	row, _ := themes[0].(map[string]any)
	wantKeys := []string{"id", "code", "name", "is_builtin", "is_active", "tokens", "branding", "custom_css"}
	for _, k := range wantKeys {
		if _, ok := row[k]; !ok {
			t.Fatalf("缺键 %s: %v", k, row)
		}
	}
	if len(row) != len(wantKeys) {
		t.Fatalf("键数量 %d 应为 %d: %v", len(row), len(wantKeys), row)
	}
	if row["custom_css"] != "" {
		t.Fatalf("custom_css %v", row["custom_css"])
	}
	tokens, _ := row["tokens"].(map[string]any)
	if tokens == nil {
		t.Fatalf("tokens 应为对象")
	}
}

func TestSaveThemeMapping(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}, route.Kit{})
	body := `{"code":"mine","name":"我的","create":true,"tokens":{"light":{}},"branding":{"tagline":"A"},"custom_css":"x"}`
	w := do(e, http.MethodPost, "/v1/themes", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	got := decMap(t, w)
	if got["saved"] != true {
		t.Fatalf("saved %v", got["saved"])
	}
	dropped, _ := got["dropped"].([]any)
	if len(dropped) != 1 {
		t.Fatalf("dropped %v", got["dropped"])
	}
	if f.lastSave.Code != "mine" || f.lastSave.Name != "我的" || !f.lastSave.Create || f.lastSave.CustomCSS != "x" {
		t.Fatalf("SaveThemeInput %+v", f.lastSave)
	}

	f.saveErr = errs.Invalid(map[string]string{"code": "bad"})
	w = do(e, http.MethodPost, "/v1/themes", `{"code":"","name":"n","create":false}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("422 status %d", w.Code)
	}
	if errFields(t, w)["code"] == nil {
		t.Fatalf("fields")
	}

	w = do(e, http.MethodPost, "/v1/themes", `{"code":"a","name":"b","create":false,"extra":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status %d", w.Code)
	}
}

func TestActivateDeleteTheme(t *testing.T) {
	f := &fakeSvc{activateErr: errs.New(errs.NotFound), deleteErr: errs.New(errs.NotFound)}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}, route.Kit{})

	w := do(e, http.MethodPost, "/v1/themes/missing/activate", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("activate %d", w.Code)
	}
	if f.activate != "missing" {
		t.Fatalf("code %q", f.activate)
	}

	w = do(e, http.MethodDelete, "/v1/themes/missing", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete %d", w.Code)
	}
	if f.delete != "missing" {
		t.Fatalf("delete code %q", f.delete)
	}

	f.activateErr = nil
	f.deleteErr = nil
	w = do(e, http.MethodPost, "/v1/themes/ok/activate", "")
	got := decMap(t, w)
	if got["activated"] != true {
		t.Fatalf("activated %v", got)
	}
	w = do(e, http.MethodDelete, "/v1/themes/ok", "")
	got = decMap(t, w)
	if got["deleted"] != true {
		t.Fatalf("deleted %v", got)
	}
}

func TestListSlotsUpdatedAt(t *testing.T) {
	f := &fakeSvc{slots: []service.Slot{
		{Key: "portal.footer", Label: "页脚", Where: "底", Content: "", Enabled: false},
		{Key: "portal.home.banner", Label: "横幅", Where: "顶", Content: "hi", Enabled: true, UpdatedAt: "2026-10-01 12:00"},
	}}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}, route.Kit{})
	w := do(e, http.MethodGet, "/v1/slots", "")
	got := decMap(t, w)
	slots, _ := got["slots"].([]any)
	if len(slots) != 2 {
		t.Fatalf("len %d", len(slots))
	}
	s0, _ := slots[0].(map[string]any)
	s1, _ := slots[1].(map[string]any)
	if _, ok := s0["updated_at"]; ok {
		t.Fatalf("未保存应有 updated_at: %v", s0)
	}
	if s1["updated_at"] != "2026-10-01 12:00" {
		t.Fatalf("updated_at %v", s1["updated_at"])
	}
}

func TestSaveSlotDroppedNull(t *testing.T) {
	f := &fakeSvc{slotNotes: nil}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}, route.Kit{})
	w := do(e, http.MethodPost, "/v1/slots/portal.footer", `{"content":"<p>x</p>","enabled":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.lastSlot.Key != "portal.footer" || f.lastSlot.Content != "<p>x</p>" || !f.lastSlot.Enabled {
		t.Fatalf("slot %+v", f.lastSlot)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["dropped"]) != "null" {
		t.Fatalf("dropped 应为 null: %s", raw["dropped"])
	}
}

func TestPublicAppearance(t *testing.T) {
	f := &fakeSvc{public: service.PublicAppearance{Theme: nil, Slots: nil}}
	e := newEngine(f, false, nil, route.Kit{})
	w := do(e, http.MethodGet, "/v1/appearance", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	got := decMap(t, w)
	if got["site_name"] != "Pandora" {
		t.Fatalf("site_name %v", got["site_name"])
	}
	if got["theme"] != nil {
		t.Fatalf("theme %v", got["theme"])
	}
	slots, _ := got["slots"].(map[string]any)
	if slots == nil || len(slots) != 0 {
		t.Fatalf("slots %v", got["slots"])
	}

	id := uuid.New()
	f.public = service.PublicAppearance{
		SiteName: "站",
		Theme: &service.Theme{
			ID: id, Code: "paper", Name: "纸", IsActive: true,
			Tokens:   json.RawMessage(`{"light":{"--x":"1"},"dark":{}}`),
			Branding: json.RawMessage(`{"tagline":"稳"}`),
		},
		Slots: map[string]string{"portal.footer": "<p>f</p>"},
	}
	w = do(e, http.MethodGet, "/v1/appearance", "")
	got = decMap(t, w)
	if got["site_name"] != "站" {
		t.Fatalf("site_name %v", got["site_name"])
	}
	theme, _ := got["theme"].(map[string]any)
	if theme == nil {
		t.Fatal("theme nil")
	}
	if theme["tokens"] == nil || theme["branding"] == nil {
		t.Fatalf("theme %v", theme)
	}
}

func TestAppearanceAnonymousThemesProtected(t *testing.T) {
	reject := func(c *gin.Context) {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		c.Abort()
	}
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{RequireAuth: reject, RecentReauth: pass, Idempotency: func(string) gin.HandlerFunc { return pass }}
	f := &fakeSvc{public: service.PublicAppearance{Slots: map[string]string{}}}

	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	h := New(f)
	h.RegisterPublic(e.Group("/v1"), route.Kit{})
	h.RegisterAdmin(e.Group("/v1"), kit)

	w := do(e, http.MethodGet, "/v1/appearance", "")
	if w.Code != http.StatusOK {
		t.Fatalf("appearance 应匿名 %d", w.Code)
	}
	w = do(e, http.MethodGet, "/v1/themes", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("themes 应 401 得 %d", w.Code)
	}
}
