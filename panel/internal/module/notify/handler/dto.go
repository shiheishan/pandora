package handler

import (
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

type mailSettingsResponse struct {
	SMTPHost          string `json:"smtp_host"`
	SMTPPort          int    `json:"smtp_port"`
	Encryption        string `json:"encryption"`
	SMTPUsername      string `json:"smtp_username"`
	HasPassword       bool   `json:"has_password"`
	FromAddress       string `json:"from_address"`
	FromName          string `json:"from_name"`
	EmailVerification bool   `json:"email_verification"`
	RegistrationMode  string `json:"registration_mode"`
}

type setMailSettingsRequest struct {
	SMTPHost          string  `json:"smtp_host"`
	SMTPPort          int     `json:"smtp_port"`
	Encryption        string  `json:"encryption"`
	SMTPUsername      string  `json:"smtp_username"`
	SMTPPassword      string  `json:"smtp_password"`
	FromAddress       string  `json:"from_address"`
	FromName          string  `json:"from_name"`
	EmailVerification *bool   `json:"email_verification"`
	RegistrationMode  *string `json:"registration_mode"`
}

type okResponse struct {
	OK bool `json:"ok"`
}

type testMailSettingsRequest struct {
	To string `json:"to"`
}

type testMailSettingsResponse struct {
	OK bool   `json:"ok"`
	To string `json:"to"`
}

type templateRowDTO struct {
	Code             string    `json:"code"`
	Channel          string    `json:"channel"`
	Locale           string    `json:"locale"`
	Category         string    `json:"category"`
	Status           string    `json:"status"`
	Version          int       `json:"version"`
	Subject          string    `json:"subject"`
	Body             string    `json:"body"`
	AllowedVariables []string  `json:"allowed_variables"`
	IsDefault        bool      `json:"is_default"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type templateItemDTO struct {
	Code             string    `json:"code"`
	Channel          string    `json:"channel"`
	Locale           string    `json:"locale"`
	Category         string    `json:"category"`
	Status           string    `json:"status"`
	Version          int       `json:"version"`
	Subject          string    `json:"subject"`
	Body             string    `json:"body"`
	AllowedVariables []string  `json:"allowed_variables"`
	IsDefault        bool      `json:"is_default"`
	UpdatedAt        time.Time `json:"updated_at"`
	HasDefault       bool      `json:"has_default"`
	Description      string    `json:"description"`
	PreviewSubject   string    `json:"preview_subject"`
	PreviewBody      string    `json:"preview_body"`
}

type listTemplatesResponse struct {
	Templates []templateItemDTO `json:"templates"`
}

type saveTemplateRequest struct {
	Code    string `json:"code"`
	Channel string `json:"channel"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type saveTemplateResponse struct {
	Template       templateRowDTO `json:"template"`
	PreviewSubject string         `json:"preview_subject"`
	PreviewBody    string         `json:"preview_body"`
}

type resetTemplateRequest struct {
	Code    string `json:"code"`
	Channel string `json:"channel"`
}

type resetTemplateResponse struct {
	Template templateRowDTO `json:"template"`
}

type previewTemplateRequest struct {
	Code    string `json:"code"`
	Channel string `json:"channel"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type previewTemplateResponse struct {
	PreviewSubject   string   `json:"preview_subject"`
	PreviewBody      string   `json:"preview_body"`
	UnknownVariables []string `json:"unknown_variables"`
}

type testTemplateRequest struct {
	Code    string  `json:"code"`
	Channel string  `json:"channel"`
	To      string  `json:"to"`
	Subject *string `json:"subject"`
	Body    *string `json:"body"`
}

type testTemplateResponse struct {
	Sent    bool   `json:"sent"`
	Subject string `json:"subject"`
}

func toRowDTO(v service.TemplateView) templateRowDTO {
	return templateRowDTO{
		Code:             v.Code,
		Channel:          v.Channel,
		Locale:           v.Locale,
		Category:         v.Category,
		Status:           v.Status,
		Version:          v.Version,
		Subject:          v.Subject,
		Body:             v.Body,
		AllowedVariables: nonNilSlice(v.AllowedVariables),
		IsDefault:        v.IsDefault,
		UpdatedAt:        v.UpdatedAt,
	}
}

func toItemDTO(v service.TemplateView) templateItemDTO {
	return templateItemDTO{
		Code:             v.Code,
		Channel:          v.Channel,
		Locale:           v.Locale,
		Category:         v.Category,
		Status:           v.Status,
		Version:          v.Version,
		Subject:          v.Subject,
		Body:             v.Body,
		AllowedVariables: nonNilSlice(v.AllowedVariables),
		IsDefault:        v.IsDefault,
		UpdatedAt:        v.UpdatedAt,
		HasDefault:       v.HasDefault,
		Description:      v.Description,
		PreviewSubject:   v.PreviewSubject,
		PreviewBody:      v.PreviewBody,
	}
}

func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func mailUpdateFromReq(req setMailSettingsRequest) service.MailSettingsUpdate {
	var pw *string
	switch req.SMTPPassword {
	case "":
		pw = nil
	case "-":
		empty := ""
		pw = &empty
	default:
		pw = &req.SMTPPassword
	}
	return service.MailSettingsUpdate{
		Host:              req.SMTPHost,
		Port:              req.SMTPPort,
		Encryption:        req.Encryption,
		Username:          req.SMTPUsername,
		Password:          pw,
		FromAddress:       req.FromAddress,
		FromName:          req.FromName,
		EmailVerification: req.EmailVerification,
		RegistrationMode:  req.RegistrationMode,
	}
}

func (h *Handler) getMailSettings(c *gin.Context) {
	ctx := c.Request.Context()
	st, err := h.svc.MailSettings(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, mailSettingsResponse{
		SMTPHost:          st.Host,
		SMTPPort:          st.Port,
		Encryption:        st.Encryption,
		SMTPUsername:      st.Username,
		HasPassword:       st.HasPassword,
		FromAddress:       st.FromAddress,
		FromName:          st.FromName,
		EmailVerification: st.EmailVerification,
		RegistrationMode:  st.RegistrationMode,
	})
}

func (h *Handler) setMailSettings(c *gin.Context) {
	ctx := c.Request.Context()
	var req setMailSettingsRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SaveMailSettings(ctx, mailUpdateFromReq(req)); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}

func (h *Handler) testMailSettings(c *gin.Context) {
	ctx := c.Request.Context()
	var req testMailSettingsRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	to := strings.TrimSpace(req.To)
	if err := h.svc.TestMailSettings(ctx, to); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, testMailSettingsResponse{OK: true, To: to})
}

func (h *Handler) listTemplates(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.svc.ListTemplates(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]templateItemDTO, len(rows))
	for i, r := range rows {
		items[i] = toItemDTO(r)
	}
	httpx.OK(c, listTemplatesResponse{Templates: items})
}

func (h *Handler) previewTemplate(c *gin.Context) {
	ctx := c.Request.Context()
	var req previewTemplateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.PreviewDraft(ctx, service.TemplateEdit{
		Code: req.Code, Channel: req.Channel, Subject: req.Subject, Body: req.Body,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, previewTemplateResponse{
		PreviewSubject:   out.Subject,
		PreviewBody:      out.Body,
		UnknownVariables: nonNilSlice(out.UnknownVariables),
	})
}

func (h *Handler) saveTemplate(c *gin.Context) {
	ctx := c.Request.Context()
	var req saveTemplateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	v, err := h.svc.SaveTemplate(ctx, service.TemplateEdit{
		Code: req.Code, Channel: req.Channel, Subject: req.Subject, Body: req.Body,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, saveTemplateResponse{
		Template:       toRowDTO(v),
		PreviewSubject: v.PreviewSubject,
		PreviewBody:    v.PreviewBody,
	})
}

func (h *Handler) resetTemplate(c *gin.Context) {
	ctx := c.Request.Context()
	var req resetTemplateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	v, err := h.svc.ResetTemplate(ctx, req.Code, req.Channel)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, resetTemplateResponse{Template: toRowDTO(v)})
}

func (h *Handler) testTemplate(c *gin.Context) {
	ctx := c.Request.Context()
	var req testTemplateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	subject, err := h.svc.TestTemplate(ctx, service.TemplateTest{
		Code: req.Code, Channel: req.Channel, To: req.To,
		Subject: req.Subject, Body: req.Body,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, testTemplateResponse{Sent: true, Subject: subject})
}
