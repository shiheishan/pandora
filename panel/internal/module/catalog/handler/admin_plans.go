package handler

import (
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) listPlans(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.svc.ListPlans(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	subs := map[uuid.UUID]int64{}
	if h.subs != nil && len(rows) > 0 {
		counts, err := h.subs.LiveSubscriptionCounts(ctx, collectPlanIDs(rows))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if counts != nil {
			subs = counts
		}
	}

	nodes := map[uuid.UUID]int64{}
	ids := collectCurrentVersionIDs(rows)
	if h.nodes != nil && len(ids) > 0 {
		counts, err := h.nodes.ServingNodeCounts(ctx, ids)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if counts != nil {
			nodes = counts
		}
	}

	plans := make([]planRowResponse, 0, len(rows))
	for _, r := range rows {
		var nodeCount int64
		if r.Plan.CurrentVersionID != nil {
			nodeCount = nodes[*r.Plan.CurrentVersionID]
		}
		plans = append(plans, planRowFromSummary(r, subs[r.Plan.ID], nodeCount))
	}

	httpx.OK(c, listPlansResponse{Plans: plans})
}

func (h *Handler) getPlan(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	d, err := h.svc.GetPlan(c.Request.Context(), planID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPlanResponse{Plan: catalogPlanDetailFrom(d)})
}

func (h *Handler) createPlan(c *gin.Context) {
	var req createPlanRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := planInputFromCreate(req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	d, err := h.svc.CreatePlan(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, createPlanResponse{Plan: catalogPlanDetailFrom(d)})
}

func (h *Handler) createPlanComplete(c *gin.Context) {
	var req createPlanCompleteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := wizardCreateFromRequest(req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	w, err := h.svc.CreatePlanComplete(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, wizardCreatedResponse(w))
}

func (h *Handler) updatePlanComplete(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req updatePlanCompleteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := wizardUpdateFromRequest(req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	w, err := h.svc.UpdatePlanComplete(c.Request.Context(), planID, in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, wizardUpdatedResponse(w))
}

func (h *Handler) updatePlan(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req updatePlanRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := planInputFromUpdate(req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	next, err := h.svc.UpdatePlan(c.Request.Context(), planID, req.ExpectedRowVersion, in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, updatePlanResponse{OK: true, RowVersion: next})
}

func (h *Handler) archivePlan(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req expectedRowVersionRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	next, err := h.svc.ArchivePlan(c.Request.Context(), planID, req.ExpectedRowVersion)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okRowVersionResponse{OK: true, RowVersion: next})
}

func (h *Handler) createVersion(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	v, err := h.svc.CreateVersion(c.Request.Context(), planID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, createVersionResponse{Version: versionRowFrom(v)})
}

func (h *Handler) updateVersion(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	versionID, err := uuid.Parse(c.Param("versionID"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req updateVersionRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if req.PoolIDs != nil {
		httpx.Fail(c, errs.Invalid(map[string]string{
			"pool_ids": "请使用 POST /v1/plans/{id}/pools 专用端点更新节点池绑定",
		}))
		return
	}
	next, err := h.svc.UpdateVersion(c.Request.Context(), planID, versionID, req.ExpectedRowVersion, versionInputFromRequest(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okRowVersionResponse{OK: true, RowVersion: next})
}

func (h *Handler) publishVersion(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	versionID, err := uuid.Parse(c.Param("versionID"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req publishVersionRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	planRV, versionRV, err := h.svc.PublishVersion(
		c.Request.Context(),
		planID,
		versionID,
		req.ExpectedPlanRowVersion,
		req.ExpectedVersionRowVersion,
	)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, publishVersionResponse{
		OK:                true,
		PlanRowVersion:    planRV,
		VersionRowVersion: versionRV,
	})
}

func (h *Handler) createPrice(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req createPriceRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := priceInputFromRequest(req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	p, err := h.svc.CreatePrice(c.Request.Context(), planID, in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, createPriceResponse{Price: priceRowFrom(p)})
}

func (h *Handler) archivePrice(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	priceID, err := uuid.Parse(c.Param("priceID"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req expectedRowVersionRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	next, err := h.svc.ArchivePrice(c.Request.Context(), planID, priceID, req.ExpectedRowVersion)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okRowVersionResponse{OK: true, RowVersion: next})
}
