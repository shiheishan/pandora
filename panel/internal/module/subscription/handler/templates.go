package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// TemplateService 是后台订阅模板用到的 service 方法。
type TemplateService interface {
	AdminTemplates(ctx context.Context) ([]service.TemplateSummary, error)
	AdminTemplate(ctx context.Context, format string) (service.TemplateDetail, error)
	AdminSaveTemplate(ctx context.Context, format, content string) (service.TemplateDetail, error)
	AdminResetTemplate(ctx context.Context, format string) (service.TemplateDetail, error)
	AdminPreviewTemplate(ctx context.Context, format, content string) (service.TemplatePreview, error)
}

type templateSummaryItem struct {
	Format     string     `json:"format"`
	Customized bool       `json:"customized"`
	Bytes      int        `json:"bytes"`
	UpdatedAt  *time.Time `json:"updated_at"`
}

type templateListResponse struct {
	Templates []templateSummaryItem `json:"templates"`
}

type templatePlaceholderItem struct {
	Token    string `json:"token"`
	Required bool   `json:"required"`
	Desc     string `json:"desc"`
}

type templateDetailResponse struct {
	Format       string                    `json:"format"`
	Customized   bool                      `json:"customized"`
	Content      string                    `json:"content"`
	Default      string                    `json:"default"`
	UpdatedAt    *time.Time                `json:"updated_at"`
	Placeholders []templatePlaceholderItem `json:"placeholders"`
	MaxBytes     int                       `json:"max_bytes"`
}

type templateContentRequest struct {
	Content string `json:"content"`
}

type templatePreviewResponse struct {
	Body        string `json:"body"`
	ContentType string `json:"content_type"`
	NodeCount   int    `json:"node_count"`
}

func toTemplateListResponse(in []service.TemplateSummary) templateListResponse {
	out := templateListResponse{Templates: make([]templateSummaryItem, 0, len(in))}
	for _, s := range in {
		out.Templates = append(out.Templates, templateSummaryItem{
			Format:     s.Format,
			Customized: s.Customized,
			Bytes:      s.Bytes,
			UpdatedAt:  utcPtr(s.UpdatedAt),
		})
	}
	return out
}

func toTemplateDetailResponse(d service.TemplateDetail) templateDetailResponse {
	ph := make([]templatePlaceholderItem, 0, len(d.Placeholders))
	for _, p := range d.Placeholders {
		ph = append(ph, templatePlaceholderItem{Token: p.Token, Required: p.Required, Desc: p.Desc})
	}
	return templateDetailResponse{
		Format:       d.Format,
		Customized:   d.Customized,
		Content:      d.Content,
		Default:      d.Default,
		UpdatedAt:    utcPtr(d.UpdatedAt),
		Placeholders: ph,
		MaxBytes:     d.MaxBytes,
	}
}

func toTemplatePreviewResponse(p service.TemplatePreview) templatePreviewResponse {
	return templatePreviewResponse{Body: p.Body, ContentType: p.ContentType, NodeCount: p.NodeCount}
}

func (h *Handler) adminTemplates(c *gin.Context) {
	rows, err := h.svc.AdminTemplates(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, toTemplateListResponse(rows))
}

func (h *Handler) adminTemplate(c *gin.Context) {
	d, err := h.svc.AdminTemplate(c.Request.Context(), c.Param("format"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, toTemplateDetailResponse(d))
}

func (h *Handler) adminSaveTemplate(c *gin.Context) {
	var req templateContentRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	d, err := h.svc.AdminSaveTemplate(c.Request.Context(), c.Param("format"), req.Content)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, toTemplateDetailResponse(d))
}

func (h *Handler) adminResetTemplate(c *gin.Context) {
	d, err := h.svc.AdminResetTemplate(c.Request.Context(), c.Param("format"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, toTemplateDetailResponse(d))
}

func (h *Handler) adminPreviewTemplate(c *gin.Context) {
	var req templateContentRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	p, err := h.svc.AdminPreviewTemplate(c.Request.Context(), c.Param("format"), req.Content)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, toTemplatePreviewResponse(p))
}
