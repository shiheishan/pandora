package public

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/content"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// contentPageResponse is deliberately smaller than content.Page. Delivery
// must not disclose internal UUIDs, targeting plan IDs, review deadlines or
// authoring workflow state to portal users.
type contentPageResponse struct {
	Slug             string     `json:"slug"`
	Kind             string     `json:"kind"`
	Category         string     `json:"category,omitempty"`
	Version          int        `json:"version"`
	Title            string     `json:"title"`
	Summary          string     `json:"summary,omitempty"`
	Body             string     `json:"body,omitempty"`
	Locale           string     `json:"locale"`
	TargetPlatforms  []string   `json:"target_platforms"`
	MinClientVersion string     `json:"min_client_version,omitempty"`
	MaxClientVersion string     `json:"max_client_version,omitempty"`
	PublishedAt      *time.Time `json:"published_at,omitempty"`
}

func deliveryPage(page content.Page) contentPageResponse {
	return contentPageResponse{
		Slug: page.Slug, Kind: page.Kind, Category: page.Category, Version: page.Version,
		Title: page.Title, Summary: page.Summary, Body: page.Body, Locale: page.Locale,
		TargetPlatforms: page.TargetPlatforms, MinClientVersion: page.MinClientVersion,
		MaxClientVersion: page.MaxClientVersion, PublishedAt: page.PublishedAt,
	}
}

func contentFilter(r *http.Request) content.VisibleFilter {
	return content.VisibleFilter{
		Kind: r.URL.Query().Get("kind"), Platform: r.URL.Query().Get("platform"),
		ClientVersion: r.URL.Query().Get("client_version"), Locale: r.URL.Query().Get("locale"),
	}
}

func (h *handlers) listContentPages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p := httpx.PrincipalFrom(r.Context())
	pages, err := h.d.Content.ListVisible(r.Context(), httpx.TenantIDFrom(r.Context()),
		p.UserID, contentFilter(r))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out := make([]contentPageResponse, 0, len(pages))
	for _, page := range pages {
		out = append(out, deliveryPage(page))
	}
	httpx.OK(w, map[string]any{"pages": out})
}

func (h *handlers) getContentPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p := httpx.PrincipalFrom(r.Context())
	page, err := h.d.Content.GetVisible(r.Context(), httpx.TenantIDFrom(r.Context()),
		p.UserID, chi.URLParam(r, "slug"), contentFilter(r))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"page": deliveryPage(*page)})
}
