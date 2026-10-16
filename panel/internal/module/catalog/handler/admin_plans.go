package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// planDetailResponse 组装写接口与详情共用的响应：只查这一个套餐的在用订阅数与可服务节点数。
func (h *Handler) planDetailResponse(ctx context.Context, d service.PlanDetail) (planDetailResponse, error) {
	subs, nodes, err := h.counts(ctx, []uuid.UUID{d.ID})
	if err != nil {
		return planDetailResponse{}, err
	}
	out := planDetailFrom(d)
	out.ActiveSubscriptions = subs[d.ID]
	out.NodeCount = nodes[d.ID]
	return out, nil
}

func (h *Handler) listPlans(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.svc.ListPlans(ctx, planFilterFrom(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	// 计数一次批量取：全部套餐 id 各查一遍。
	subs, nodes, err := h.counts(ctx, collectPlanIDs(rows))
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	plans := make([]planRow, 0, len(rows))
	for _, r := range rows {
		plans = append(plans, planRowFrom(r, subs[r.ID], nodes[r.ID]))
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
	plan, err := h.planDetailResponse(c.Request.Context(), d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPlanResponse{Plan: plan})
}

func (h *Handler) createPlan(c *gin.Context) {
	var req createPlanRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := planCreateFrom(req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	d, err := h.svc.CreatePlan(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	plan, err := h.planDetailResponse(c.Request.Context(), d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, createPlanResponse{Plan: plan})
}

func (h *Handler) updateBasic(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req basicUpdateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	d, err := h.svc.UpdateBasic(c.Request.Context(), planID, basicUpdateFrom(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	plan, err := h.planDetailResponse(c.Request.Context(), d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPlanResponse{Plan: plan})
}

func (h *Handler) updateSales(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req salesUpdateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := salesUpdateFrom(req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	d, err := h.svc.UpdateSales(c.Request.Context(), planID, in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	plan, err := h.planDetailResponse(c.Request.Context(), d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPlanResponse{Plan: plan})
}

func (h *Handler) updateReset(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req resetUpdateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	d, err := h.svc.UpdateReset(c.Request.Context(), planID, resetUpdateFrom(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	plan, err := h.planDetailResponse(c.Request.Context(), d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPlanResponse{Plan: plan})
}

func (h *Handler) updatePrices(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req pricesUpdateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := pricesUpdateFrom(req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	d, err := h.svc.UpdatePrices(c.Request.Context(), planID, in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	plan, err := h.planDetailResponse(c.Request.Context(), d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPlanResponse{Plan: plan})
}

func (h *Handler) updatePools(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req poolsUpdateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := poolsUpdateFrom(req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	d, err := h.svc.SetPlanPools(c.Request.Context(), planID, in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	plan, err := h.planDetailResponse(c.Request.Context(), d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPlanResponse{Plan: plan})
}

func (h *Handler) updateUsage(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req usageUpdateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	d, err := h.svc.UpdateUsage(c.Request.Context(), planID, usageUpdateFrom(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	plan, err := h.planDetailResponse(c.Request.Context(), d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPlanResponse{Plan: plan})
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
	var req expectedRowVersionRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	d, err := h.svc.PublishVersion(c.Request.Context(), planID, versionID, req.ExpectedRowVersion)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	plan, err := h.planDetailResponse(c.Request.Context(), d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPlanResponse{Plan: plan})
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
	d, err := h.svc.ArchivePlan(c.Request.Context(), planID, req.ExpectedRowVersion)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	plan, err := h.planDetailResponse(c.Request.Context(), d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPlanResponse{Plan: plan})
}
