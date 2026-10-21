package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/content/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

type listPortalPagesResponse struct {
	Pages []portalPageView `json:"pages"`
}

type getPortalPageResponse struct {
	Page portalPageView `json:"page"`
}

func visibleFilter(c *gin.Context, withQuery bool) service.VisibleFilter {
	f := service.VisibleFilter{
		Kind: c.Query("kind"),
	}
	if withQuery {
		f.Query = c.Query("q")
	}
	return f
}

// listPortalPages 门户可见文章列表。
func (h *Handler) listPortalPages(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	p, ok := authn.FromGin(c)
	if !ok {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return
	}
	rows, err := h.svc.ListVisible(c.Request.Context(), p.UserID, visibleFilter(c, true))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]portalPageView, 0, len(rows))
	for _, row := range rows {
		out = append(out, portalPageFrom(row))
	}
	httpx.OK(c, listPortalPagesResponse{Pages: out})
}

// getPortalPage 门户单篇文章（含正文）。
func (h *Handler) getPortalPage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	p, ok := authn.FromGin(c)
	if !ok {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return
	}
	slug := c.Param("slug")
	page, err := h.svc.GetVisible(c.Request.Context(), p.UserID, slug, visibleFilter(c, false))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getPortalPageResponse{Page: portalPageFrom(page)})
}
