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
