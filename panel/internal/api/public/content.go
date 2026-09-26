// [INPUT]: 依赖 domain/content 的 ListVisible / GetVisible / SubmitFeedback，依赖 platform/httpx
// [OUTPUT]: 对外提供 listContentPages、getContentPage、submitContentFeedback 处理器与 contentPageResponse 投递形状
// [POS]: api/public 的帮助中心处理器（门户-09）：只投递给用户该看的字段，反馈与详情共用同一套可见性 query
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
	// q 只用于列表：帮助中心的搜索要匹配正文，而列表不带正文
	filter := contentFilter(r)
	filter.Query = r.URL.Query().Get("q")
	pages, err := h.d.Content.ListVisible(r.Context(), httpx.TenantIDFrom(r.Context()),
		p.UserID, filter)
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

// contentFeedbackRequest 的 helpful 用指针：漏传时不能默认成「没帮助」。
type contentFeedbackRequest struct {
	Helpful *bool `json:"helpful"`
	Version int   `json:"version"`
}

// submitContentFeedback 记录「这篇文章有帮助吗」。query 与读详情时相同
// （platform / client_version / locale），可见性按同一套规则判定。
func (h *handlers) submitContentFeedback(w http.ResponseWriter, r *http.Request) {
	var req contentFeedbackRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Helpful == nil {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"helpful": "必填"}))
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Content.SubmitFeedback(r.Context(), httpx.TenantIDFrom(r.Context()), p.UserID,
		chi.URLParam(r, "slug"), req.Version, *req.Helpful, contentFilter(r)); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}
