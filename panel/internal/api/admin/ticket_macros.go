package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type listTicketMacrosResponse struct {
	Macros []support.Macro `json:"macros"`
}

func (h *handlers) listTicketMacros(w http.ResponseWriter, r *http.Request) {
	macros, err := h.d.Support.ListMacros(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listTicketMacrosResponse{Macros: macros})
}

type saveTicketMacroResponse struct {
	ID string `json:"id"`
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
	httpx.OK(w, saveTicketMacroResponse{ID: id})
}

type deleteTicketMacroResponse struct {
	OK bool `json:"ok"`
}

func (h *handlers) deleteTicketMacro(w http.ResponseWriter, r *http.Request) {
	if err := h.d.Support.DeleteMacro(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id")); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, deleteTicketMacroResponse{OK: true})
}
