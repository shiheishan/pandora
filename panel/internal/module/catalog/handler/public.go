package handler

import (
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

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

func (h *Handler) portalTrafficPacks(c *gin.Context) {
	packs, err := h.svc.ActiveTrafficPacks(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := portalPacksFrom(packs)
	if out == nil {
		out = []portalPack{}
	}
	httpx.OK(c, portalTrafficPacksResponse{Packs: out})
}
