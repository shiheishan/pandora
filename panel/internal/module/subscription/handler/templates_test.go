package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// templateSvc 是后台订阅模板接口的替身。
type templateSvc struct {
	Service
	summaries []service.TemplateSummary
	detail    service.TemplateDetail
	preview   service.TemplatePreview

	gotFormat  string
	gotContent string

	saveErr    error
	resetErr   error
	previewErr error
}

func (f *templateSvc) AdminTemplates(context.Context) ([]service.TemplateSummary, error) {
	return f.summaries, nil
}

func (f *templateSvc) AdminTemplate(_ context.Context, format string) (service.TemplateDetail, error) {
	f.gotFormat = format
	return f.detail, nil
}

func (f *templateSvc) AdminSaveTemplate(_ context.Context, format, content string) (service.TemplateDetail, error) {
	f.gotFormat, f.gotContent = format, content
	if f.saveErr != nil {
		return service.TemplateDetail{}, f.saveErr
	}
	return f.detail, nil
}

func (f *templateSvc) AdminResetTemplate(_ context.Context, format string) (service.TemplateDetail, error) {
	f.gotFormat = format
	if f.resetErr != nil {
		return service.TemplateDetail{}, f.resetErr
	}
	return f.detail, nil
}

func (f *templateSvc) AdminPreviewTemplate(_ context.Context, format, content string) (service.TemplatePreview, error) {
	f.gotFormat, f.gotContent = format, content
	if f.previewErr != nil {
		return service.TemplatePreview{}, f.previewErr
	}
	return f.preview, nil
}

func TestAdminTemplatesList(t *testing.T) {
	updated := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f := &templateSvc{summaries: []service.TemplateSummary{
		{Format: "clash", Customized: true, Bytes: 100, UpdatedAt: &updated},
		{Format: "surge", Customized: false, Bytes: 0, UpdatedAt: nil},
	}}
	e := newEngine(f, true)
	w := do(e, http.MethodGet, "/v1/subscription-templates", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	got := decMap(t, w)
	templates := got["templates"].([]any)
	if len(templates) != 2 {
		t.Fatalf("templates len %d", len(templates))
	}
	first := templates[0].(map[string]any)
	if first["format"] != "clash" || first["customized"] != true || first["bytes"] != float64(100) {
		t.Fatalf("first: %v", first)
	}
	if first["updated_at"] != "2026-10-01T12:00:00Z" {
		t.Fatalf("updated_at: %v", first["updated_at"])
	}
	second := templates[1].(map[string]any)
	if second["format"] != "surge" || second["customized"] != false || second["bytes"] != float64(0) {
		t.Fatalf("second: %v", second)
	}
	if second["updated_at"] != nil {
		t.Fatalf("updated_at should be null: %v", second["updated_at"])
	}
}

func TestAdminTemplateDetail(t *testing.T) {
	f := &templateSvc{detail: service.TemplateDetail{
		Format:     "clash",
		Customized: false,
		Content:    "x",
		Default:    "y",
		MaxBytes:   65536,
		Placeholders: []service.TemplatePlaceholder{
			{Token: "{{proxies}}", Required: true, Desc: "节点块"},
		},
	}}
	e := newEngine(f, true)
	w := do(e, http.MethodGet, "/v1/subscription-templates/clash", "")
	if w.Code != http.StatusOK || f.gotFormat != "clash" {
		t.Fatalf("status %d format=%q", w.Code, f.gotFormat)
	}
	got := decMap(t, w)
	ph := got["placeholders"].([]any)
	if len(ph) != 1 {
		t.Fatalf("placeholders: %v", ph)
	}
	if got["max_bytes"] != float64(65536) {
		t.Fatalf("max_bytes: %v", got["max_bytes"])
	}
}

func TestAdminSaveTemplate(t *testing.T) {
	f := &templateSvc{detail: service.TemplateDetail{Format: "clash", Content: "x"}}
	e := newEngine(f, true)
	w := do(e, http.MethodPut, "/v1/subscription-templates/clash", `{"content":"x"}`)
	if w.Code != http.StatusOK || f.gotFormat != "clash" || f.gotContent != "x" {
		t.Fatalf("ok: %d format=%q content=%q", w.Code, f.gotFormat, f.gotContent)
	}

	f.saveErr = errs.Invalid(map[string]string{"content": "缺少占位 {{proxies}}"})
	w = do(e, http.MethodPut, "/v1/subscription-templates/clash", `{"content":"bad"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation status %d body=%s", w.Code, w.Body.String())
	}
	errObj := decMap(t, w)["error"].(map[string]any)
	if errObj["code"] != "validation_failed" {
		t.Fatalf("code: %v", errObj["code"])
	}
}

func TestAdminSaveTemplateBadJSON(t *testing.T) {
	f := &templateSvc{}
	e := newEngine(f, true)
	w := do(e, http.MethodPut, "/v1/subscription-templates/clash", `{`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminResetTemplate(t *testing.T) {
	f := &templateSvc{detail: service.TemplateDetail{Format: "clash"}}
	e := newEngine(f, true)
	w := do(e, http.MethodDelete, "/v1/subscription-templates/clash", "")
	if w.Code != http.StatusOK || f.gotFormat != "clash" {
		t.Fatalf("ok: %d format=%q", w.Code, f.gotFormat)
	}

	f.resetErr = errs.New(errs.NotFound)
	w = do(e, http.MethodDelete, "/v1/subscription-templates/unknown", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("not found: %d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminPreviewTemplate(t *testing.T) {
	f := &templateSvc{preview: service.TemplatePreview{
		Body:        "body",
		ContentType: "text/plain; charset=utf-8",
		NodeCount:   3,
	}}
	e := newEngine(f, true)
	w := do(e, http.MethodPost, "/v1/subscription-templates/loon/preview", `{"content":""}`)
	if w.Code != http.StatusOK || f.gotFormat != "loon" || f.gotContent != "" {
		t.Fatalf("status %d format=%q content=%q", w.Code, f.gotFormat, f.gotContent)
	}
	got := decMap(t, w)
	if got["body"] != "body" || got["content_type"] != "text/plain; charset=utf-8" || got["node_count"] != float64(3) {
		t.Fatalf("body: %v", got)
	}
}

func TestAdminTemplateWritesRequireReauth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	reauth := func(c *gin.Context) {
		if c.GetHeader("X-Test-Reauth") != "1" {
			httpx.Fail(c, errs.New(errs.ReauthRequired))
			return
		}
		c.Next()
	}
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: reauth,
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	f := &templateSvc{
		detail:  service.TemplateDetail{Format: "clash", Content: "x"},
		preview: service.TemplatePreview{Body: "b", NodeCount: 1},
	}
	New(f, nil, 0).RegisterAdmin(e.Group("/v1"), kit)

	// 不带头：PUT 与 DELETE 被近期重认证拦截（403 reauth_required），替身不被调用
	f.gotFormat = ""
	w := do(e, http.MethodPut, "/v1/subscription-templates/clash", `{"content":"x"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("PUT 无重认证应为 403: %d body=%s", w.Code, w.Body.String())
	}
	if errObj := decMap(t, w)["error"].(map[string]any); errObj["code"] != "reauth_required" {
		t.Fatalf("PUT 错误码应为 reauth_required: %v", errObj["code"])
	}
	if f.gotFormat != "" {
		t.Fatalf("PUT 替身不应被调用: gotFormat=%q", f.gotFormat)
	}

	f.gotFormat = ""
	w = do(e, http.MethodDelete, "/v1/subscription-templates/clash", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("DELETE 无重认证应为 403: %d body=%s", w.Code, w.Body.String())
	}
	if errObj := decMap(t, w)["error"].(map[string]any); errObj["code"] != "reauth_required" {
		t.Fatalf("DELETE 错误码应为 reauth_required: %v", errObj["code"])
	}
	if f.gotFormat != "" {
		t.Fatalf("DELETE 替身不应被调用: gotFormat=%q", f.gotFormat)
	}

	// 不带头：读与预览接口放行，返回 200
	w = do(e, http.MethodGet, "/v1/subscription-templates", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET 列表应放行 200: %d body=%s", w.Code, w.Body.String())
	}

	f.gotFormat = ""
	w = do(e, http.MethodGet, "/v1/subscription-templates/clash", "")
	if w.Code != http.StatusOK || f.gotFormat != "clash" {
		t.Fatalf("GET 详情应放行 200: %d format=%q", w.Code, f.gotFormat)
	}

	f.gotFormat = ""
	w = do(e, http.MethodPost, "/v1/subscription-templates/clash/preview", `{"content":""}`)
	if w.Code != http.StatusOK || f.gotFormat != "clash" {
		t.Fatalf("POST 预览应放行 200: %d format=%q", w.Code, f.gotFormat)
	}

	// 带 X-Test-Reauth: 1：PUT 与 DELETE 放行，返回 200
	f.gotFormat = ""
	req := httptest.NewRequest(http.MethodPut, "/v1/subscription-templates/clash", bytes.NewBufferString(`{"content":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Reauth", "1")
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK || f.gotFormat != "clash" {
		t.Fatalf("带重认证 PUT 应为 200: %d format=%q body=%s", w.Code, f.gotFormat, w.Body.String())
	}

	f.gotFormat = ""
	req = httptest.NewRequest(http.MethodDelete, "/v1/subscription-templates/clash", nil)
	req.Header.Set("X-Test-Reauth", "1")
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK || f.gotFormat != "clash" {
		t.Fatalf("带重认证 DELETE 应为 200: %d format=%q body=%s", w.Code, f.gotFormat, w.Body.String())
	}
}
