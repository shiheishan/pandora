// [INPUT]: 依赖 domain/adminops 的套餐目录用例（详情 / 新建 / 向导一次建成与改完 / 资料 / 版本 / 发布 / 价格 / 归档），依赖 platform/httpx、chi 的路径参数
// [OUTPUT]: 对包内提供套餐目录处理器 getPlan、createPlan、createPlanComplete、updatePlanComplete、updatePlan、createPlanVersion、updatePlanVersion、publishPlanVersion、createPlanPrice、archivePlanPrice、archivePlan；成功响应为具名 DTO（*Response）
// [POS]: api/admin 套餐目录（含版本与价格）的 HTTP 外壳；向导两个处理器直接回领域层的输出结构，路由与保护链在 router_catalog.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type getPlanResponse struct {
	Plan *adminops.CatalogPlanDetail `json:"plan"`
}

func (h *handlers) getPlan(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Ops.GetPlan(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, getPlanResponse{Plan: out})
}

type createPlanResponse struct {
	Plan *adminops.CatalogPlanDetail `json:"plan"`
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
	httpx.JSON(w, http.StatusCreated, createPlanResponse{Plan: out})
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

type updatePlanResponse struct {
	OK         bool  `json:"ok"`
	RowVersion int64 `json:"row_version"`
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
	httpx.OK(w, updatePlanResponse{OK: true, RowVersion: next})
}

type createPlanVersionResponse struct {
	Version *adminops.VersionRow `json:"version"`
}

func (h *handlers) createPlanVersion(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Ops.CreatePlanVersion(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"), p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, createPlanVersionResponse{Version: out})
}

type updatePlanVersionResponse struct {
	OK         bool  `json:"ok"`
	RowVersion int64 `json:"row_version"`
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
	httpx.OK(w, updatePlanVersionResponse{OK: true, RowVersion: next})
}

type publishPlanVersionResponse struct {
	OK                bool  `json:"ok"`
	PlanRowVersion    int64 `json:"plan_row_version"`
	VersionRowVersion int64 `json:"version_row_version"`
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
	httpx.OK(w, publishPlanVersionResponse{OK: true, PlanRowVersion: planNext, VersionRowVersion: versionNext})
}

type createPlanPriceResponse struct {
	Price *adminops.PriceRow `json:"price"`
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
	httpx.JSON(w, http.StatusCreated, createPlanPriceResponse{Price: out})
}

type archivePlanPriceResponse struct {
	OK         bool  `json:"ok"`
	RowVersion int64 `json:"row_version"`
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
	httpx.OK(w, archivePlanPriceResponse{OK: true, RowVersion: next})
}

type archivePlanResponse struct {
	OK         bool  `json:"ok"`
	RowVersion int64 `json:"row_version"`
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
	httpx.OK(w, archivePlanResponse{OK: true, RowVersion: next})
}
