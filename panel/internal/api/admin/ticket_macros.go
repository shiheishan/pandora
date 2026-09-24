// [INPUT]: 依赖 domain/support 的 ListMacros / SaveMacro / DeleteMacro，依赖 platform/httpx
// [OUTPUT]: 对外提供 listTicketMacros、saveTicketMacro、deleteTicketMacro 三个处理器
// [POS]: api/admin 的工单快捷回复处理器（后台-02 回复框上方的标签与「管理」对话框），路由在 router.go 工单段
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) listTicketMacros(w http.ResponseWriter, r *http.Request) {
	macros, err := h.d.Support.ListMacros(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"macros": macros})
}

// saveTicketMacro 同时服务新建（POST v1/ticket-macros）与编辑（POST v1/ticket-macros/{id}），
// 沿用 saveUserGroup 的写法：路径里有 id 就是覆盖。
func (h *handlers) saveTicketMacro(w http.ResponseWriter, r *http.Request) {
	var in support.MacroInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	id, err := h.d.Support.SaveMacro(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id"), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"id": id})
}

func (h *handlers) deleteTicketMacro(w http.ResponseWriter, r *http.Request) {
	if err := h.d.Support.DeleteMacro(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id")); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}
