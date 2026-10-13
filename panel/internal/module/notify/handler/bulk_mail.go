package handler

import (
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// bulkMailRequest 是群发邮件的请求体：筛选条件在顶层，与 identity 的批量预览同一套字段。
type bulkMailRequest struct {
	Status            string `json:"status" binding:"omitempty,oneof=active suspended banned"`
	GroupID           string `json:"group_id" binding:"max=64"`
	Query             string `json:"query" binding:"max=200"`
	HasActiveSub      *bool  `json:"has_active_sub"`
	PlanID            string `json:"plan_id" binding:"max=64"`
	ExpiresWithinDays int    `json:"expires_within_days"`
	SubState          string `json:"sub_state" binding:"max=16"`
	Subject           string `json:"subject" binding:"max=1000"`
	Body              string `json:"body" binding:"max=100000"`
}

type bulkMailResponse struct {
	Queued  int `json:"queued"`
	Skipped int `json:"skipped"`
}

// toInput 解析 uuid 字段；格式错的字段合进一个 422。其余取值由 service 校验。
func (r bulkMailRequest) toInput() (service.BulkMailInput, error) {
	bad := map[string]string{}
	parse := func(field, raw, msg string) *uuid.UUID {
		if raw == "" {
			return nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			bad[field] = msg
			return nil
		}
		return &id
	}
	groupID := parse("group_id", r.GroupID, "分组标识格式不正确")
	planID := parse("plan_id", r.PlanID, "套餐标识格式不正确")
	if len(bad) > 0 {
		return service.BulkMailInput{}, errs.Invalid(bad)
	}
	return service.BulkMailInput{
		Filter: identitysvc.BulkFilter{
			Status:            r.Status,
			GroupID:           groupID,
			Query:             r.Query,
			HasActiveSub:      r.HasActiveSub,
			PlanID:            planID,
			ExpiresWithinDays: r.ExpiresWithinDays,
			SubState:          r.SubState,
		},
		Subject: r.Subject,
		Body:    r.Body,
	}, nil
}

// sendBulkMail 按筛选给命中用户排群发邮件；返回排了多少封、跳过多少人（发送是异步的）。
func (h *Handler) sendBulkMail(c *gin.Context) {
	var req bulkMailRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := req.toInput()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.SendBulkMail(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, bulkMailResponse{Queued: res.Queued, Skipped: res.Skipped})
}
