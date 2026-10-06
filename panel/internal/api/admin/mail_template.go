// [INPUT]: 依赖 domain/notify 的模板读写、示例渲染与草稿校验、SMTP 配置与发信器，依赖 platform/httpx
// [OUTPUT]: 对外提供 handlers 的 listMailTemplates / saveMailTemplate / resetMailTemplate / previewMailTemplate / testMailTemplate；成功响应为具名 DTO（*Response）
// [POS]: api/admin 的通知模板：code 只能改不能建，列表带 has_default，草稿可预览、可直接实发测试
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"
	"time"

	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// listMailTemplatesItem 是模板列表的一项：模板本身加上发送时机说明与示例渲染。
type listMailTemplatesItem struct {
	AllowedVariables []string  `json:"allowed_variables"`
	Body             string    `json:"body"`
	Category         string    `json:"category"`
	Channel          string    `json:"channel"`
	Code             string    `json:"code"`
	Description      string    `json:"description"`
	HasDefault       bool      `json:"has_default"`
	IsDefault        bool      `json:"is_default"`
	Locale           string    `json:"locale"`
	PreviewBody      string    `json:"preview_body"`
	PreviewSubject   string    `json:"preview_subject"`
	Status           string    `json:"status"`
	Subject          string    `json:"subject"`
	UpdatedAt        time.Time `json:"updated_at"`
	Version          int       `json:"version"`
}

type listMailTemplatesResponse struct {
	Templates []listMailTemplatesItem `json:"templates"`
}

func (h *handlers) listMailTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.Notify.ListTemplates(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 把「这个模板什么时候发」一并回给前端。只给 code 的话，
	// 管理员不敢改 —— 不知道改了会影响谁。
	out := make([]listMailTemplatesItem, 0, len(rows))
	for _, t := range rows {
		subject, body := notify.RenderPreview(t.Subject, t.Body, t.AllowedVariables)
		out = append(out, listMailTemplatesItem{
			Code: t.Code, Channel: t.Channel, Locale: t.Locale,
			Category: t.Category, Status: t.Status, Version: t.Version,
			Subject: t.Subject, Body: t.Body,
			AllowedVariables: t.AllowedVariables,
			IsDefault:        t.IsDefault,
			HasDefault:       notify.HasDefaultTemplate(t.Code, t.Channel),
			UpdatedAt:        t.UpdatedAt,
			Description:      notify.TemplateDescription(t.Code),
			PreviewSubject:   subject,
			PreviewBody:      body,
		})
	}
	httpx.OK(w, listMailTemplatesResponse{Templates: out})
}

type saveMailTemplateReq struct {
	Code    string `json:"code"`
	Channel string `json:"channel"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type saveMailTemplateResponse struct {
	PreviewBody    string              `json:"preview_body"`
	PreviewSubject string              `json:"preview_subject"`
	Template       *notify.TemplateRow `json:"template"`
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
	httpx.OK(w, saveMailTemplateResponse{Template: t,
		PreviewSubject: subject, PreviewBody: body})
}

type resetMailTemplateReq struct {
	Code    string `json:"code"`
	Channel string `json:"channel"`
}

type resetMailTemplateResponse struct {
	Template *notify.TemplateRow `json:"template"`
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
	httpx.OK(w, resetMailTemplateResponse{Template: t})
}

type testMailTemplateReq struct {
	Code    string `json:"code"`
	Channel string `json:"channel"`
	To      string `json:"to"`
	// Subject / Body 提供时按草稿渲染发送（先做保存时同样的校验），省略时发已保存的模板
	Subject *string `json:"subject"`
	Body    *string `json:"body"`
}

// previewMailTemplate 用示例值渲染一份未保存的草稿（纯计算，不写库）。
func (h *handlers) previewMailTemplate(w http.ResponseWriter, r *http.Request) {
	var req saveMailTemplateReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Notify.PreviewDraft(r.Context(), httpx.TenantIDFrom(r.Context()),
		req.Code, req.Channel, req.Subject, req.Body)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

type testMailTemplateResponse struct {
	Sent    bool   `json:"sent"`
	Subject string `json:"subject"`
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
	var subject, body string
	if req.Subject != nil || req.Body != nil {
		draftSubject, draftBody := "", ""
		if req.Subject != nil {
			draftSubject = *req.Subject
		}
		if req.Body != nil {
			draftBody = *req.Body
		}
		var err error
		subject, body, err = h.d.Notify.RenderDraftForTest(r.Context(), tenantID, req.Code, req.Channel, draftSubject, draftBody)
		if err != nil {
			httpx.Fail(w, r, h.d.Log, err)
			return
		}
	} else {
		rows, err := h.d.Notify.ListTemplates(r.Context(), tenantID)
		if err != nil {
			httpx.Fail(w, r, h.d.Log, err)
			return
		}
		var found bool
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
	httpx.OK(w, testMailTemplateResponse{Sent: true, Subject: subject})
}
