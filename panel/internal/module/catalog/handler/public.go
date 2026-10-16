package handler

import (
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// portalPlans 是用户端的公开目录：不要求登录，带了登录态就按用户组过滤可见性（在 service）。
func (h *Handler) portalPlans(c *gin.Context) {
	var userID *uuid.UUID
	if p, ok := authn.FromGin(c); ok && p.UserID != uuid.Nil {
		userID = &p.UserID
	}
	offers, err := h.svc.PortalCatalog(c.Request.Context(), userID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	plans := portalPlansFrom(offers)
	if plans == nil {
		plans = []portalPlan{}
	}
	httpx.OK(c, portalPlansResponse{Plans: plans})
}
