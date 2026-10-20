package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

type periodEndRequest struct {
	PeriodEnd string `json:"period_end"`
	Reason    string `json:"reason"`
}

type periodEndResponse struct {
	SubscriptionID string    `json:"subscription_id"`
	UserID         string    `json:"user_id"`
	PreviousEnd    time.Time `json:"previous_end"`
	PeriodEnd      time.Time `json:"period_end"`
}

type quotaLimitRequest struct {
	Metric     string `json:"metric"`
	Period     string `json:"period"`
	LimitBytes *int64 `json:"limit_bytes"`
	Reason     string `json:"reason"`
}

// quotaLimitResponse 是改订阅配额上限的响应。
type quotaLimitResponse struct {
	OK bool `json:"ok"`
}

// adminSetPeriodEnd 把一份正在生效的订阅的到期时间直接改成指定时刻。
func (h *Handler) adminSetPeriodEnd(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	var req periodEndRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	end, err := time.Parse(time.RFC3339, req.PeriodEnd)
	if err != nil {
		httpx.Fail(c, errs.Invalid(map[string]string{"period_end": "时间格式不正确"}))
		return
	}
	res, err := h.svc.AdminSetPeriodEnd(c.Request.Context(), id, end, req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, periodEndResponse{
		SubscriptionID: res.SubscriptionID.String(),
		UserID:         res.UserID.String(),
		PreviousEnd:    utc(res.PreviousEnd),
		PeriodEnd:      utc(res.PeriodEnd),
	})
}

// adminSetQuotaLimit 改一份订阅某个配额行的上限。
func (h *Handler) adminSetQuotaLimit(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	var req quotaLimitRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.AdminSetQuotaLimit(c.Request.Context(), id, req.Metric, req.Period, req.LimitBytes, req.Reason); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, quotaLimitResponse{OK: true})
}
