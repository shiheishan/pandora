package handler

import (
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

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
	httpx.OK(c, siteConfigResponse{
		RegistrationMode:  p.Mode,
		EmailVerification: p.EmailVerification,
		PasswordReset:     ok,
	})
}

type siteResponse struct {
	Timezone string `json:"timezone"`
}

type setSiteRequest struct {
	Timezone string `json:"timezone"`
}

func (h *Handler) getSite(c *gin.Context) {
	ctx := c.Request.Context()
	site, err := h.svc.Site(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, siteResponse{Timezone: site.Timezone})
}

func (h *Handler) setSite(c *gin.Context) {
	ctx := c.Request.Context()
	var req setSiteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	cur, err := h.svc.Site(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	cur.Timezone = req.Timezone
	if err := h.svc.SetSite(ctx, cur); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, siteResponse(req))
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
