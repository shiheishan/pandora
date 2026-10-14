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

func (h *Handler) setPlanPools(c *gin.Context) {
	planID, err := poolPathID(c, "id")
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	var req planPoolsSetRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}

	versionID, poolIDs, err := parsePlanPoolsSetRequest(req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	ctx := c.Request.Context()
	next, err := h.svc.SetPlanPools(ctx, planID, versionID, req.ExpectedVersionRowVersion, poolIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, planPoolsSetResponse{
		Bound:      len(poolIDs),
		RowVersion: next,
		VersionID:  versionID.String(),
	})
}
