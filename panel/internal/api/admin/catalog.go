package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) getPlan(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Ops.GetPlan(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"plan": out})
}

func (h *handlers) createPlan(w http.ResponseWriter, r *http.Request) {
	var in adminops.CreatePlanInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Ops.CreatePlan(r.Context(), httpx.TenantIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"plan": out})
}

// createPlanComplete 一次建成一个能卖的套餐。
//
// 对应界面上合并后的「新建套餐」表单：基本信息、流量、设备数、价格、
// 节点分组填在同一屏，提交一次。原先这要开五个弹窗，中间断在哪一步
// 都会留下一个卖不出去的半成品，而列表里看不出它缺什么。
func (h *handlers) createPlanComplete(w http.ResponseWriter, r *http.Request) {
	var in adminops.CreatePlanCompleteInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Ops.CreatePlanComplete(r.Context(), httpx.TenantIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// updatePlanComplete 一次改完套餐的资料、额度、价格与节点分组。
//
// 对应界面上合并后的编辑表单。原先改一个套餐要在四个弹窗之间跳：
// 套餐管理 → 编辑资料 / 编辑版本 / 新增价格 / 绑定分组，每个都是独立
// 提交，改到一半走神就是个不一致的状态。
//
// 返回的 changed 列表逐条说明这次改了什么、什么时候生效 —— 「保存成功」
// 说不清额度是立刻变了还是只对新用户生效。
func (h *handlers) updatePlanComplete(w http.ResponseWriter, r *http.Request) {
	var in adminops.UpdatePlanCompleteInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Ops.UpdatePlanComplete(r.Context(),
		httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) updatePlan(w http.ResponseWriter, r *http.Request) {
	var in adminops.UpdatePlanInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	next, err := h.d.Ops.UpdatePlan(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "row_version": next})
}

func (h *handlers) createPlanVersion(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Ops.CreatePlanVersion(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"version": out})
}

func (h *handlers) updatePlanVersion(w http.ResponseWriter, r *http.Request) {
	var in adminops.VersionSemanticsInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	next, err := h.d.Ops.UpdatePlanVersion(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "versionID"), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "row_version": next})
}

func (h *handlers) publishPlanVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedPlanRowVersion    int64 `json:"expected_plan_row_version"`
		ExpectedVersionRowVersion int64 `json:"expected_version_row_version"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	planNext, versionNext, err := h.d.Ops.PublishPlanVersion(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "versionID"), p.UserID, req.ExpectedPlanRowVersion, req.ExpectedVersionRowVersion)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "plan_row_version": planNext, "version_row_version": versionNext})
}

func (h *handlers) createPlanPrice(w http.ResponseWriter, r *http.Request) {
	var in adminops.CreatePriceInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Ops.CreatePlanPrice(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"price": out})
}

func (h *handlers) archivePlanPrice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedRowVersion int64 `json:"expected_row_version"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	next, err := h.d.Ops.ArchivePlanPrice(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "priceID"), p.UserID, req.ExpectedRowVersion)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "row_version": next})
}

func (h *handlers) archivePlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedRowVersion int64 `json:"expected_row_version"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	next, err := h.d.Ops.ArchivePlan(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), p.UserID, req.ExpectedRowVersion)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "row_version": next})
}
