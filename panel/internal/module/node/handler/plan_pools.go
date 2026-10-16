package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

func (h *Handler) planPools(c *gin.Context) {
	planID, err := poolPathID(c, "id")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	data, err := h.svc.PlanPools(ctx, planID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, planPoolsResponseFrom(data))
}
