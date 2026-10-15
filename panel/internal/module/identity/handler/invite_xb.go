package handler

import (
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// inviteVisitRequest 是公开记录邀请链接访问的请求体。
type inviteVisitRequest struct {
	Code string `json:"code" binding:"max=64"`
}

// admSetReferrerRequest 是后台改用户邀请人的请求体。
type admSetReferrerRequest struct {
	ReferrerID *string `json:"referrer_id"`
	Reason     string  `json:"reason" binding:"max=1000"`
}

// createInviteCode 处理 POST /v1/me/invite/codes。
func (h *Handler) createInviteCode(c *gin.Context) {
	p, ok := authn.FromGin(c)
	if !ok {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return
	}
	v, err := h.svc.CreateInviteCode(c.Request.Context(), p.UserID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, inviteCodeFrom(v))
}

// inviteVisit 处理 POST /v1/invite/visit（公开）。
func (h *Handler) inviteVisit(c *gin.Context) {
	var req inviteVisitRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.RecordInviteVisit(c.Request.Context(), req.Code); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"ok": true})
}

// inviteVisitLimit 是邀请访问记录的按路由 IP 限流；不加 StrictMode，Redis 故障时放行，不因访问量统计返回 503。
func (h *Handler) inviteVisitLimit() gin.HandlerFunc {
	if h.limiter == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return h.limiter.Middleware(
		ratelimit.Dimension{
			Rule: ratelimit.PerMinute("invite_visit_ip", 30),
			Key:  ratelimit.ByRoute,
		},
	)
}

// setUserReferrer 处理 POST /v1/users/:id/referrer。
func (h *Handler) setUserReferrer(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req admSetReferrerRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	var referrerID *uuid.UUID
	if req.ReferrerID != nil && *req.ReferrerID != "" {
		parsed, parseErr := uuid.Parse(*req.ReferrerID)
		if parseErr != nil {
			httpx.Fail(c, errs.Invalid(map[string]string{"referrer_id": "邀请人 ID 不正确"}))
			return
		}
		referrerID = &parsed
	}
	if err := h.svc.AdminSetReferrer(c.Request.Context(), id, referrerID, req.Reason); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"ok": true})
}
