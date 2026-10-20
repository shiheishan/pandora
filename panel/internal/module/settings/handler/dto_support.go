package handler

import (
	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

type supportRulesResponse struct {
	WaitForReply   bool `json:"wait_for_reply"`
	AutoClose      bool `json:"auto_close_enabled"`
	AutoCloseHours int  `json:"auto_close_hours"`
	MaxOpenTickets int  `json:"max_open_tickets"`
}

type setSupportRulesRequest struct {
	WaitForReply   *bool `json:"wait_for_reply"`
	AutoClose      *bool `json:"auto_close_enabled"`
	AutoCloseHours *int  `json:"auto_close_hours"`
	MaxOpenTickets *int  `json:"max_open_tickets"`
}

func (h *Handler) getSupportRules(c *gin.Context) {
	ctx := c.Request.Context()
	rules, err := h.svc.SupportRules(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, supportRulesResponse{
		WaitForReply:   rules.WaitForReply,
		AutoClose:      rules.AutoClose,
		AutoCloseHours: rules.AutoCloseHours,
		MaxOpenTickets: rules.MaxOpenTickets,
	})
}

func (h *Handler) setSupportRules(c *gin.Context) {
	ctx := c.Request.Context()
	var req setSupportRulesRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	fields := map[string]string{}
	if req.WaitForReply == nil {
		fields["wait_for_reply"] = "必填"
	}
	if req.AutoClose == nil {
		fields["auto_close_enabled"] = "必填"
	}
	if req.AutoCloseHours == nil {
		fields["auto_close_hours"] = "必填"
	}
	if req.MaxOpenTickets == nil {
		fields["max_open_tickets"] = "必填"
	}
	if len(fields) > 0 {
		httpx.Fail(c, errs.Invalid(fields))
		return
	}
	v := service.SupportRules{
		WaitForReply:   *req.WaitForReply,
		AutoClose:      *req.AutoClose,
		AutoCloseHours: *req.AutoCloseHours,
		MaxOpenTickets: *req.MaxOpenTickets,
	}
	if err := h.svc.SetSupportRules(ctx, v); err != nil {
		httpx.Fail(c, err)
		return
	}
	rules, err := h.svc.SupportRules(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, supportRulesResponse{
		WaitForReply:   rules.WaitForReply,
		AutoClose:      rules.AutoClose,
		AutoCloseHours: rules.AutoCloseHours,
		MaxOpenTickets: rules.MaxOpenTickets,
	})
}
