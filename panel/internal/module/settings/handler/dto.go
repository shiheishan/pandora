package handler

import (
	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

// siteConfigResponse 是门户站点能力开关；password_reset 同时要求 SMTP 已配置且 notify.email 未暂停。
type siteConfigResponse struct {
	RegistrationMode  string `json:"registration_mode"`
	EmailVerification bool   `json:"email_verification"`
	PasswordReset     bool   `json:"password_reset"`
}

func (h *Handler) siteConfig(c *gin.Context) {
	ctx := c.Request.Context()
	p, err := h.svc.RegistrationPolicy(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ok, err := h.svc.MailConfigured(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	emailOn, err := h.svc.SwitchEnabled(ctx, service.SwitchNotifyEmail)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, siteConfigResponse{
		RegistrationMode:  p.Mode,
		EmailVerification: p.EmailVerification,
		PasswordReset:     ok && emailOn,
	})
}

// siteResponse 是后台站点设置：站点名是全站唯一的站点名来源（门户标题、入口页 <title>、邮件、订阅配置名都取它）。
// SEO 三项给门户入口页用。URL 是后台存的主域名（空 = 用安装时的地址 URLFallback），只影响链接，nginx 与证书不跟着变。
type siteResponse struct {
	Name           string `json:"name"`
	Timezone       string `json:"timezone"`
	SEOKeywords    string `json:"seo_keywords"`
	SEODescription string `json:"seo_description"`
	AllowIndexing  bool   `json:"allow_indexing"`
	URL            string `json:"url"`
	URLFallback    string `json:"url_fallback"`
}

// setSiteRequest 是站点设置的部分更新：缺的字段保持原值。
type setSiteRequest struct {
	Name           *string `json:"name"`
	Timezone       *string `json:"timezone"`
	SEOKeywords    *string `json:"seo_keywords"`
	SEODescription *string `json:"seo_description"`
	AllowIndexing  *bool   `json:"allow_indexing"`
	// URL 是主域名：空串 = 恢复用安装时的地址；改成新值时服务端先核对它通到本面板，不通回 422 fields.url
	URL *string `json:"url"`
}

func (h *Handler) siteToResponse(s service.Site) siteResponse {
	return siteResponse{
		Name:           s.Name,
		Timezone:       s.Timezone,
		SEOKeywords:    s.SEOKeywords,
		SEODescription: s.SEODescription,
		AllowIndexing:  s.AllowIndexing,
		URL:            s.URL,
		URLFallback:    h.svc.SiteURLFallback(),
	}
}

func (h *Handler) getSite(c *gin.Context) {
	ctx := c.Request.Context()
	site, err := h.svc.Site(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, h.siteToResponse(site))
}

func (h *Handler) setSite(c *gin.Context) {
	ctx := c.Request.Context()
	var req setSiteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SetSite(ctx, service.SiteUpdate{
		Name:           req.Name,
		Timezone:       req.Timezone,
		SEOKeywords:    req.SEOKeywords,
		SEODescription: req.SEODescription,
		AllowIndexing:  req.AllowIndexing,
		URL:            req.URL,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	// 回写入后的值（站点名去过首尾空白），后台据此刷新
	site, err := h.svc.Site(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, h.siteToResponse(site))
}

type switchItem struct {
	Code      string  `json:"code"`
	Enabled   bool    `json:"enabled"`
	Essential bool    `json:"essential"`
	Reason    *string `json:"reason"`
}

type switchesResponse struct {
	Switches []switchItem `json:"switches"`
}

func (h *Handler) listSwitches(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.svc.Switches(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]switchItem, len(rows))
	for i, r := range rows {
		items[i] = switchItem{
			Code:      r.Code,
			Enabled:   r.Enabled,
			Essential: r.Essential,
			Reason:    r.Reason,
		}
	}
	httpx.OK(c, switchesResponse{Switches: items})
}

type setSwitchRequest struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}

type setSwitchResponse struct {
	OK      bool `json:"ok"`
	Enabled bool `json:"enabled"`
}

func (h *Handler) setSwitch(c *gin.Context) {
	ctx := c.Request.Context()
	var req setSwitchRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SetSwitch(ctx, c.Param("code"), req.Enabled, req.Reason); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, setSwitchResponse{OK: true, Enabled: req.Enabled})
}
