// [INPUT]: 依赖 domain/nodefabric 的后台节点编排与 NodeCredentials，依赖 platform/httpx
// [OUTPUT]: 对外提供节点新建 / 编辑 / 复制 / 移动 / 排序 / 批量改状态 / 一步退役（nodeRetire）与身份令牌状态（nodeIdentity）处理器
// [POS]: api/admin 的节点编排处理器（后台-07 节点 tab 与抽屉），路径 id 先做 UUID 校验回 404
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func validateAdminNodeID(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return httpx.NotFoundOrForbidden()
	}
	return nil
}

func (h *handlers) createAdminNode(w http.ResponseWriter, r *http.Request) {
	var in nodefabric.CreateAdminNodeInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Node.CreateAdminNode(r.Context(), httpx.TenantIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

func (h *handlers) patchAdminNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateAdminNodeID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var in nodefabric.PatchAdminNodeInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Node.PatchAdminNode(r.Context(), httpx.TenantIDFrom(r.Context()), id, in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) copyAdminNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateAdminNodeID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var in nodefabric.CloneAdminNodeInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Node.CloneAdminNode(r.Context(), httpx.TenantIDFrom(r.Context()), id, in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

func (h *handlers) moveAdminNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateAdminNodeID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var in nodefabric.MoveAdminNodeInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Node.MoveAdminNode(r.Context(), httpx.TenantIDFrom(r.Context()), id, in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) reorderAdminNodes(w http.ResponseWriter, r *http.Request) {
	var in nodefabric.ReorderNodesInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	if err := h.d.Node.ReorderAdminNodes(r.Context(), httpx.TenantIDFrom(r.Context()), in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "updated": len(in.Items)})
}

func (h *handlers) batchAdminNodeStatus(w http.ResponseWriter, r *http.Request) {
	var in nodefabric.BatchNodeLifecycleInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	if err := h.d.Node.BatchAdminNodeLifecycle(r.Context(), httpx.TenantIDFrom(r.Context()), in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "updated": len(in.Items), "serving_status": in.ServingStatus})
}

// nodeIdentity 返回节点身份与令牌状态（后台节点抽屉「身份与令牌」）。
func (h *handlers) nodeIdentity(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Node.NodeCredentials(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

// nodeRetire 一步退役（契约后台-07）：生命周期、服务状态、身份与在途任务在一个事务里
// 收尾，之后 DELETE 可以直接销毁。提交后通知节点端：它的配置已不再下发
func (h *handlers) nodeRetire(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateAdminNodeID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var req struct {
		RowVersion int64  `json:"row_version"`
		Reason     string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	tenantID := httpx.TenantIDFrom(r.Context())
	out, err := h.d.Node.RetireNode(r.Context(), tenantID, nodefabric.RetireNodeInput{
		ID: id, RowVersion: req.RowVersion, Reason: req.Reason,
		ActorID: httpx.PrincipalFrom(r.Context()).UserID,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.d.Node.NotifyNodeChanged(r.Context(), tenantID, id)
	httpx.OK(w, out)
}
