// [INPUT]: 依赖 domain/content 的 ListAdmin / GetAdmin / PublishVersion / Archive，依赖 platform/httpx、chi 的路径参数
// [OUTPUT]: 对包内提供知识库页面处理器 listContentPages、getContentPage、publishContentVersion、archiveContentPage；成功响应为具名 DTO（*Response）
// [POS]: api/admin 知识库（内容页）版本的 HTTP 外壳，与 announce.go 同属内容段；路由与保护链在 router_content.go 的 registerContentPageRoutes
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/content"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type listContentPagesResponse struct {
	Pages []content.Page `json:"pages"`
}

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
	httpx.OK(w, listContentPagesResponse{Pages: pages})
}

type getContentPageResponse struct {
	Page *content.Page `json:"page"`
}

func (h *handlers) getContentPage(w http.ResponseWriter, r *http.Request) {
	page, err := h.d.Content.GetAdmin(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, getContentPageResponse{Page: page})
}

type publishContentVersionResponse struct {
	Page *content.PublishResult `json:"page"`
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
	httpx.JSON(w, http.StatusCreated, publishContentVersionResponse{Page: out})
}

type archiveContentPageResponse struct {
	Page *content.ArchiveResult `json:"page"`
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
	httpx.OK(w, archiveContentPageResponse{Page: out})
}
