package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
)

type fakeNotify struct {
	p6Fake // P6 的方法与记录，见 p6fake_test.go

	mail        service.MailSettings
	mailSave    service.MailSettingsUpdate
	mailSaveErr error
	templates   []service.TemplateView
	testTplIn   service.TemplateTest
	testTplSubj string
}

func (f *fakeNotify) MailSettings(ctx context.Context) (service.MailSettings, error) {
	return f.mail, nil
}

func (f *fakeNotify) SaveMailSettings(ctx context.Context, in service.MailSettingsUpdate) error {
	f.mailSave = in
	return f.mailSaveErr
}

func (f *fakeNotify) TestMailSettings(ctx context.Context, to string) error {
	return nil
}

func (f *fakeNotify) ListTemplates(ctx context.Context) ([]service.TemplateView, error) {
	return f.templates, nil
}

func (f *fakeNotify) SaveTemplate(ctx context.Context, in service.TemplateEdit) (service.TemplateView, error) {
	return service.TemplateView{
		Code: in.Code, Channel: in.Channel, Subject: in.Subject, Body: in.Body,
		PreviewSubject: "ps", PreviewBody: "pb",
	}, nil
}

func (f *fakeNotify) ResetTemplate(ctx context.Context, code, channel string) (service.TemplateView, error) {
	return service.TemplateView{Code: code, Channel: channel}, nil
}

func (f *fakeNotify) PreviewDraft(ctx context.Context, in service.TemplateEdit) (service.DraftPreview, error) {
	return service.DraftPreview{Subject: "s", Body: "b", UnknownVariables: nil}, nil
}

func (f *fakeNotify) TestTemplate(ctx context.Context, in service.TemplateTest) (string, error) {
	f.testTplIn = in
	return f.testTplSubj, nil
}

type notifyTestKit struct {
	authCalls   int
	reauthCalls int
	idemScopes  []string // 经过幂等中间件的作用域，按请求顺序
}

func (k *notifyTestKit) kit() route.Kit {
	return route.Kit{
		RequireAuth: func(c *gin.Context) {
			k.authCalls++
			c.Next()
		},
		RecentReauth: func(c *gin.Context) {
			k.reauthCalls++
			c.Next()
		},
		Idempotency: func(scope string) gin.HandlerFunc {
			return func(c *gin.Context) {
				k.idemScopes = append(k.idemScopes, scope)
				c.Next()
			}
		},
	}
}

func doNotify(e http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
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

func setupEngine(fake Notify) (*gin.Engine, *notifyTestKit) {
	gin.SetMode(gin.TestMode)
	k := &notifyTestKit{}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), k.kit())
	return e, k
}

func TestGetMailSettingsKeys(t *testing.T) {
	fake := &fakeNotify{
		mail: service.MailSettings{
			Host: "h", Port: 465, Encryption: "ssl", Username: "u",
			HasPassword: true, FromAddress: "a@b.c", FromName: "N",
			EmailVerification: true, RegistrationMode: "open",
		},
	}
	e, _ := setupEngine(fake)
	rec := doNotify(e, http.MethodGet, "/v1/settings/mail", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"smtp_host", "smtp_port", "encryption", "smtp_username", "has_password",
		"from_address", "from_name", "email_verification", "registration_mode",
	}
	if len(m) != len(want) {
		t.Fatalf("keys %d: %v", len(m), m)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing key %q in %v", k, m)
		}
	}
	if _, ok := m["smtp_password"]; ok {
		t.Fatalf("must not expose smtp_password")
	}
}

func TestSetMailSettingsPasswordAndOptional(t *testing.T) {
	fake := &fakeNotify{}
	e, _ := setupEngine(fake)
	base := `{"smtp_host":"","smtp_port":25,"encryption":"none","smtp_username":"","from_address":"","from_name":""`

	for _, tc := range []struct {
		pw        string
		wantNil   bool
		wantEmpty bool
		wantVal   string
	}{
		{"", true, false, ""},
		{"-", false, true, ""},
		{"abc", false, false, "abc"},
	} {
		body := base + `,"smtp_password":"` + tc.pw + `"}`
		rec := doNotify(e, http.MethodPost, "/v1/settings/mail", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("pw %q status %d %s", tc.pw, rec.Code, rec.Body.String())
		}
		got := fake.mailSave.Password
		if tc.wantNil {
			if got != nil {
				t.Fatalf("pw %q: want nil, got %v", tc.pw, got)
			}
		} else if tc.wantEmpty {
			if got == nil || *got != "" {
				t.Fatalf("pw %q: want empty ptr, got %v", tc.pw, got)
			}
		} else {
			if got == nil || *got != tc.wantVal {
				t.Fatalf("pw %q: want %q, got %v", tc.pw, tc.wantVal, got)
			}
		}
	}

	rec := doNotify(e, http.MethodPost, "/v1/settings/mail", base+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("optional status %d", rec.Code)
	}
	if fake.mailSave.EmailVerification != nil || fake.mailSave.RegistrationMode != nil {
		t.Fatalf("optional fields should be nil: ev=%v rm=%v",
			fake.mailSave.EmailVerification, fake.mailSave.RegistrationMode)
	}
	var ok okResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil || !ok.OK {
		t.Fatalf("ok resp %s", rec.Body.String())
	}
}

func TestSetMailSettingsUnknownField(t *testing.T) {
	e, _ := setupEngine(&fakeNotify{})
	rec := doNotify(e, http.MethodPost, "/v1/settings/mail",
		`{"smtp_host":"","smtp_port":25,"encryption":"none","extra":1}`)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "bad_request" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}
}

func TestSetMailSettingsInvalidField(t *testing.T) {
	fake := &fakeNotify{mailSaveErr: errs.Invalid(map[string]string{"smtp_password": "x"})}
	e, _ := setupEngine(fake)
	rec := doNotify(e, http.MethodPost, "/v1/settings/mail",
		`{"smtp_host":"","smtp_port":25,"encryption":"none"}`)
	if rec.Code != http.StatusUnprocessableEntity || errCode(t, rec) != "validation_failed" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}
	var body struct {
		Error struct {
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Fields["smtp_password"] != "x" {
		t.Fatalf("fields %v", body.Error.Fields)
	}
}

func TestListTemplatesItemShape(t *testing.T) {
	fake := &fakeNotify{
		templates: []service.TemplateView{{
			Code: "c", Channel: "email", AllowedVariables: nil,
			HasDefault: true, Description: "d", PreviewSubject: "ps", PreviewBody: "pb",
		}},
	}
	e, _ := setupEngine(fake)
	rec := doNotify(e, http.MethodGet, "/v1/mail/templates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out listTemplatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Templates) != 1 {
		t.Fatalf("templates %d", len(out.Templates))
	}
	item := out.Templates[0]
	if !item.HasDefault || item.Description != "d" || item.PreviewSubject != "ps" || item.PreviewBody != "pb" {
		t.Fatalf("item %+v", item)
	}
	if item.AllowedVariables == nil || len(item.AllowedVariables) != 0 {
		t.Fatalf("allowed_variables %v", item.AllowedVariables)
	}
	if !strings.Contains(rec.Body.String(), `"allowed_variables":[]`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestSaveTemplateResponseShape(t *testing.T) {
	e, _ := setupEngine(&fakeNotify{})
	rec := doNotify(e, http.MethodPost, "/v1/mail/templates",
		`{"code":"x","channel":"email","subject":"s","body":"b"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"template", "preview_subject", "preview_body"} {
		if m[k] == nil {
			t.Fatalf("missing %q", k)
		}
	}
	var tpl map[string]any
	if err := json.Unmarshal(m["template"], &tpl); err != nil {
		t.Fatal(err)
	}
	if _, ok := tpl["has_default"]; ok {
		t.Fatalf("template must not have has_default: %v", tpl)
	}
}

func TestPreviewTemplateUnknownVariablesEmpty(t *testing.T) {
	e, _ := setupEngine(&fakeNotify{})
	rec := doNotify(e, http.MethodPost, "/v1/mail/templates/preview",
		`{"code":"x","channel":"email","subject":"","body":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"unknown_variables":[]`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestTestTemplateDraftPointersAndReauth(t *testing.T) {
	fake := &fakeNotify{testTplSubj: "subj"}
	e, k := setupEngine(fake)

	rec := doNotify(e, http.MethodPost, "/v1/mail/templates/test",
		`{"code":"x","channel":"email","to":"a@b.c"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if fake.testTplIn.Subject != nil || fake.testTplIn.Body != nil {
		t.Fatalf("want nil draft pointers, got subj=%v body=%v", fake.testTplIn.Subject, fake.testTplIn.Body)
	}
	if k.reauthCalls != 1 {
		t.Fatalf("reauth calls %d", k.reauthCalls)
	}
	var resp testTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Sent || resp.Subject != "subj" {
		t.Fatalf("resp %+v", resp)
	}

	rec = doNotify(e, http.MethodPost, "/v1/mail/templates/test",
		`{"code":"x","channel":"email","to":"a@b.c","subject":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty subject status %d", rec.Code)
	}
	if fake.testTplIn.Subject == nil || *fake.testTplIn.Subject != "" {
		t.Fatalf("subject ptr %v", fake.testTplIn.Subject)
	}
}

// 后台不分权限：每条后台路由都挂 RequireAuth，且只挂一次。
func TestAdminRoutesRequireAuth(t *testing.T) {
	e, k := setupEngine(&fakeNotify{
		mail: service.MailSettings{RegistrationMode: "open"},
		templates: []service.TemplateView{{
			Code: "c", Channel: "email", UpdatedAt: time.Unix(0, 0),
		}},
	})

	calls := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/settings/mail", ""},
		{http.MethodPost, "/v1/settings/mail", `{"smtp_port":25,"encryption":"none"}`},
		{http.MethodPost, "/v1/settings/mail/test", `{"to":"a@b.c"}`},
		{http.MethodGet, "/v1/mail/templates", ""},
		{http.MethodPost, "/v1/mail/templates/preview", `{"code":"c","channel":"email","subject":"","body":""}`},
		{http.MethodPost, "/v1/mail/templates", `{"code":"c","channel":"email","subject":"","body":""}`},
		{http.MethodPost, "/v1/mail/templates/reset", `{"code":"c","channel":"email"}`},
		{http.MethodPost, "/v1/mail/templates/test", `{"code":"c","channel":"email","to":"a@b.c"}`},
	}
	if n := len(e.Routes()); n != len(calls) {
		t.Fatalf("后台路由 %d 条，用例覆盖 %d 条", n, len(calls))
	}
	for i, c := range calls {
		rec := doNotify(e, c.method, c.path, c.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s status %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
		if k.authCalls != i+1 {
			t.Fatalf("%s %s: RequireAuth 调用 %d 次，want %d", c.method, c.path, k.authCalls, i+1)
		}
	}
}
