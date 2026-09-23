package admin

import (
	"net/http"

	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) listMailTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.Notify.ListTemplates(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 把「这个模板什么时候发」一并回给前端。只给 code 的话，
	// 管理员不敢改 —— 不知道改了会影响谁。
	out := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		subject, body := notify.RenderPreview(t.Subject, t.Body, t.AllowedVariables)
		out = append(out, map[string]any{
			"code": t.Code, "channel": t.Channel, "locale": t.Locale,
			"category": t.Category, "status": t.Status, "version": t.Version,
			"subject": t.Subject, "body": t.Body,
			"allowed_variables": t.AllowedVariables,
			"is_default":        t.IsDefault,
			"updated_at":        t.UpdatedAt,
			"description":       notify.TemplateDescription(t.Code),
			"preview_subject":   subject,
			"preview_body":      body,
		})
	}
	httpx.OK(w, map[string]any{"templates": out})
}

type saveMailTemplateReq struct {
	Code    string `json:"code"`
	Channel string `json:"channel"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (h *handlers) saveMailTemplate(w http.ResponseWriter, r *http.Request) {
	var req saveMailTemplateReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	principal := httpx.PrincipalFrom(r.Context())
	t, err := h.d.Notify.SaveTemplate(r.Context(), httpx.TenantIDFrom(r.Context()),
		notify.SaveTemplateInput{Code: req.Code, Channel: req.Channel,
			Subject: req.Subject, Body: req.Body, ActorID: principal.UserID})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	subject, body := notify.RenderPreview(t.Subject, t.Body, t.AllowedVariables)
	httpx.OK(w, map[string]any{"template": t,
		"preview_subject": subject, "preview_body": body})
}

type resetMailTemplateReq struct {
	Code    string `json:"code"`
	Channel string `json:"channel"`
}

func (h *handlers) resetMailTemplate(w http.ResponseWriter, r *http.Request) {
	var req resetMailTemplateReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	principal := httpx.PrincipalFrom(r.Context())
	t, err := h.d.Notify.ResetTemplate(r.Context(), httpx.TenantIDFrom(r.Context()),
		req.Code, req.Channel, principal.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"template": t})
}

type testMailTemplateReq struct {
	Code    string `json:"code"`
	Channel string `json:"channel"`
	To      string `json:"to"`
}

// testMailTemplate 用示例值渲染当前模板并真发一封。
//
// 与 /settings/mail/test 的区别：那个只验证 SMTP 通不通，发的是固定内容；
// 这个发的是模板本身，能看出变量替换和排版的实际效果。
func (h *handlers) testMailTemplate(w http.ResponseWriter, r *http.Request) {
	var req testMailTemplateReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.To == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"to": "请填写收件地址"}))
		return
	}
	if req.Channel != "email" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"只有邮件模板可以测试发送，站内信请到用户端查看"))
		return
	}

	tenantID := httpx.TenantIDFrom(r.Context())
	rows, err := h.d.Notify.ListTemplates(r.Context(), tenantID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var found bool
	var subject, body string
	for _, t := range rows {
		if t.Code == req.Code && t.Channel == req.Channel {
			subject, body = notify.RenderPreview(t.Subject, t.Body, t.AllowedVariables)
			found = true
			break
		}
	}
	if !found {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}

	cfg, err := notify.LoadSMTPConfig(r.Context(), h.d.Pool, h.d.Envelope, tenantID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if !cfg.Enabled() {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"SMTP 还没配置好：服务器地址、端口、发件人地址都要填"))
		return
	}

	if err := notify.NewSMTPSender(cfg).Send(r.Context(), req.To,
		"[测试] "+subject, body); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"发送失败："+err.Error()))
		return
	}
	httpx.OK(w, map[string]any{"sent": true, "subject": subject})
}
