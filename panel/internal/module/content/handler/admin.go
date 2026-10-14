package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/content/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
)

type listAdminPagesResponse struct {
	Pages []adminPageView `json:"pages"`
}

type getAdminPageResponse struct {
	Page adminPageView `json:"page"`
}

type publishPageResponse struct {
	Page publishPageSaved `json:"page"`
}

type publishPageSaved struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Version int    `json:"version"`
	Status  string `json:"status"`
}

type archivePageResponse struct {
	Page archivePageResult `json:"page"`
}

type archivePageResult struct {
	ID              string `json:"id"`
	Version         int    `json:"version"`
	Status          string `json:"status"`
	AlreadyArchived bool   `json:"already_archived"`
}

// listAdminPages 后台内容页列表。
func (h *Handler) listAdminPages(c *gin.Context) {
	limit, _, err := pagination.Parse(c, 200, 500)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := h.svc.ListAdmin(c.Request.Context(), service.ListFilter{
		Kind:   c.Query("kind"),
		Status: c.Query("status"),
		Query:  c.Query("q"),
		Limit:  limit,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]adminPageView, 0, len(rows))
	for _, row := range rows {
		out = append(out, adminPageFrom(row))
	}
	httpx.OK(c, listAdminPagesResponse{Pages: out})
}

// getAdminPage 后台单版本详情（含正文）。
func (h *Handler) getAdminPage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	page, err := h.svc.GetAdmin(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, getAdminPageResponse{Page: adminPageFrom(page)})
}

// publishPage 发一个新版本。
func (h *Handler) publishPage(c *gin.Context) {
	var req publishRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.PublishVersion(c.Request.Context(), req.publishInput())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, publishPageResponse{
		Page: publishPageSaved{
			ID:      res.ID.String(),
			Slug:    res.Slug,
			Version: res.Version,
			Status:  res.Status,
		},
	})
}

// archivePage 归档指定版本。
func (h *Handler) archivePage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req archiveRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.Archive(c.Request.Context(), id, req.ExpectedVersion)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, archivePageResponse{
		Page: archivePageResult{
			ID:              res.ID.String(),
			Version:         res.Version,
			Status:          res.Status,
			AlreadyArchived: res.AlreadyArchived,
		},
	})
}
