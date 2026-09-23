package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func parseServerListQuery(r *http.Request) (nodefabric.ListServersInput, error) {
	in := nodefabric.ListServersInput{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
	}
	if in.Status != "" && !nodefabric.ValidServerStatus(in.Status) {
		return in, httpx.Invalid(map[string]string{"status": "不支持的服务器状态"})
	}
	if len([]rune(in.Query)) > 120 {
		return in, httpx.Invalid(map[string]string{"q": "搜索词不能超过 120 个字符"})
	}
	return in, nil
}

func validateServerID(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return httpx.NotFoundOrForbidden()
	}
	return nil
}

func validateServerTextLimits(values map[string]string) error {
	limits := map[string]int{
		"region": 64, "hostname": 253, "architecture": 32,
		"os_name": 120, "notes": 2000, "reason": 500,
	}
	fields := map[string]string{}
	for key, value := range values {
		if limit, ok := limits[key]; ok && len([]rune(strings.TrimSpace(value))) > limit {
			fields[key] = "内容过长，最多允许 " + strconv.Itoa(limit) + " 个字符"
		}
	}
	if len(fields) > 0 {
		return httpx.Invalid(fields)
	}
	return nil
}

func (h *handlers) serverList(w http.ResponseWriter, r *http.Request) {
	in, err := parseServerListQuery(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Node.ListServers(r.Context(), httpx.TenantIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"servers": out, "total": len(out)})
}

func (h *handlers) serverCreate(w http.ResponseWriter, r *http.Request) {
	var in nodefabric.CreateServerInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if err := validateServerTextLimits(map[string]string{
		"region": in.Region, "hostname": in.Hostname, "architecture": in.Architecture,
		"os_name": in.OSName, "notes": in.Notes,
	}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Node.CreateServer(r.Context(), httpx.TenantIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

func (h *handlers) serverGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateServerID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Node.GetServer(r.Context(), httpx.TenantIDFrom(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) serverPatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateServerID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var in nodefabric.PatchServerInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	text := map[string]string{}
	for key, value := range map[string]*string{
		"region": in.Region, "hostname": in.Hostname, "architecture": in.Architecture,
		"os_name": in.OSName, "notes": in.Notes,
	} {
		if value != nil {
			text[key] = *value
		}
	}
	if err := validateServerTextLimits(text); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Node.PatchServer(r.Context(), httpx.TenantIDFrom(r.Context()), id, in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) serverSetStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateServerID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var in nodefabric.SetServerStatusInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if err := validateServerTextLimits(map[string]string{"reason": in.Reason}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	in.ActorID = httpx.PrincipalFrom(r.Context()).UserID
	out, err := h.d.Node.SetServerStatus(r.Context(), httpx.TenantIDFrom(r.Context()), id, in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) serverDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateServerID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var req struct {
		RowVersion int64 `json:"row_version"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	principal := httpx.PrincipalFrom(r.Context())
	err := h.d.Node.DeleteServer(r.Context(), httpx.TenantIDFrom(r.Context()), principal.UserID, id, req.RowVersion)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "id": id})
}

func (h *handlers) serverNodes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateServerID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Node.ListServerNodes(r.Context(), httpx.TenantIDFrom(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"nodes": out, "total": len(out)})
}
