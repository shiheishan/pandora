// [INPUT]: 依赖 domain/adminops 的 ListTrafficPacks / CreateTrafficPack / UpdateTrafficPack / SetTrafficPackStatus，依赖 platform/httpx、chi 的路径参数
// [OUTPUT]: 对包内提供 listTrafficPacks、createTrafficPack、updateTrafficPack、setTrafficPackStatus 四个处理器
// [POS]: api/admin 后台-04 流量包 tab 的 HTTP 外壳；路由与保护链（catalog.publish + 重认证 + 幂等）在 router_catalog.go 的 registerTrafficPackRoutes
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) listTrafficPacks(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Ops.ListTrafficPacks(r.Context(), httpx.TenantIDFrom(r.Context()),
		r.URL.Query().Get("status"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"packs": out})
}

func (h *handlers) createTrafficPack(w http.ResponseWriter, r *http.Request) {
	var in adminops.TrafficPackInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Ops.CreateTrafficPack(r.Context(), httpx.TenantIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"pack": out})
}

func (h *handlers) updateTrafficPack(w http.ResponseWriter, r *http.Request) {
	var in adminops.TrafficPackInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Ops.UpdateTrafficPack(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "id"), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"pack": out})
}

// setTrafficPackStatus 上架（active）或下架（archived）。
func (h *handlers) setTrafficPackStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status            string     `json:"status"`
		ExpectedUpdatedAt *time.Time `json:"expected_updated_at"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Ops.SetTrafficPackStatus(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "id"), httpx.PrincipalFrom(r.Context()).UserID,
		req.Status, req.ExpectedUpdatedAt)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"pack": out})
}
