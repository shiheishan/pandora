package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/content"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) listContentPages(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	pages, err := h.d.Content.ListAdmin(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, content.ListFilter{
			Kind: r.URL.Query().Get("kind"), Status: r.URL.Query().Get("status"),
			Query: r.URL.Query().Get("q"), Limit: limit,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"pages": pages})
}

func (h *handlers) getContentPage(w http.ResponseWriter, r *http.Request) {
	page, err := h.d.Content.GetAdmin(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"page": page})
}

func (h *handlers) publishContentVersion(w http.ResponseWriter, r *http.Request) {
	var in content.PublishInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Content.PublishVersion(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, httpx.RequestIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"page": out})
}

func (h *handlers) archiveContentPage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedVersion int `json:"expected_version"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Content.Archive(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, httpx.RequestIDFrom(r.Context()),
		chi.URLParam(r, "id"), req.ExpectedVersion)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"page": out})
}
