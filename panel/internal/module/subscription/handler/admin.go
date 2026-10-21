package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// 后台入参的范围（与 service 一致，handler 先挡一道给出字段错误）。
const (
	maxExtendDays  = 3650
	maxGrantBytes  = int64(10) << 40
	minDeviceLimit = 0
	maxDeviceLimit = 1000
)

// adminExtend 给一份订阅加时长。
func (h *Handler) adminExtend(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	var req extendRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if req.Days < 1 || req.Days > maxExtendDays {
		httpx.Fail(c, errs.Invalid(map[string]string{"days": "天数须在 1 到 3650 之间"}))
		return
	}
	res, err := h.svc.AdminExtend(c.Request.Context(), id, req.Days, req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, extendResponse{
		SubscriptionID: res.SubscriptionID.String(),
		UserEmail:      res.UserEmail,
		Days:           res.Days,
		PreviousEnd:    utc(res.PreviousEnd),
		PeriodEnd:      utc(res.PeriodEnd),
	})
}

// adminGrantTraffic 给一份生效中的订阅加本期流量（送的量，订阅套餐当期有效、一次性一直有效）。
func (h *Handler) adminGrantTraffic(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	var req trafficGrantRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if req.Bytes < 1 || req.Bytes > maxGrantBytes {
		httpx.Fail(c, errs.Invalid(map[string]string{"bytes": "流量须在 1 字节到 10 TiB 之间"}))
		return
	}
	res, err := h.svc.AdminGrantTraffic(c.Request.Context(), id, req.Bytes, req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, trafficGrantResponse{
		SubscriptionID: res.SubscriptionID.String(),
		UserID:         res.UserID.String(),
		UserEmail:      res.UserEmail,
		GrantedBytes:   res.GrantedBytes,
		BonusBytes:     res.BonusBytes,
	})
}

// adminResetTraffic 把一份生效中订阅的流量已用量清零。
func (h *Handler) adminResetTraffic(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	var req trafficResetRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	freed, err := h.svc.AdminResetTraffic(c.Request.Context(), id, req.Note)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, trafficResetResponse{Reset: true, FreedBytes: freed})
}

// adminSetDeviceLimit 改一份订阅的设备数覆盖。
func (h *Handler) adminSetDeviceLimit(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	var req deviceLimitRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if req.Limit != nil && (*req.Limit < minDeviceLimit || *req.Limit > maxDeviceLimit) {
		httpx.Fail(c, errs.Invalid(map[string]string{"limit": "设备数须在 0 到 1000 之间，留空沿用套餐"}))
		return
	}
	if err := h.svc.AdminSetDeviceLimit(c.Request.Context(), id, req.Limit); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}
