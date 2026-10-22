// Package handler 是 content 模块的 HTTP 层：后台知识库/自定义页面版本管理，门户帮助中心读取。
package handler

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/content/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// Service 是 handler 用到的 content service 方法；*service.Service 实现它。
type Service interface {
	ListAdmin(ctx context.Context, f service.ListFilter) ([]service.Page, error)
	GetAdmin(ctx context.Context, id uuid.UUID) (service.Page, error)
	PublishVersion(ctx context.Context, in service.PublishInput) (service.PublishResult, error)
	Archive(ctx context.Context, id uuid.UUID, expectedVersion int) (service.ArchiveResult, error)
	ListVisible(ctx context.Context, userID uuid.UUID, f service.VisibleFilter) ([]service.Page, error)
	GetVisible(ctx context.Context, userID uuid.UUID, slug string, f service.VisibleFilter) (service.Page, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// 幂等作用域。
const (
	ScopeVersionCreate = "content_page_version_create"
	ScopeArchive       = "content_page_archive"
)

// Handler 是 content 模块的 HTTP 入口。
type Handler struct {
	svc Service
}

// New 构造 Handler。
func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterAdmin 注册后台内容页路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/content-pages", k.RequireAuth, h.listAdminPages)
	v1.GET("/content-pages/:id", k.RequireAuth, h.getAdminPage)
	v1.POST("/content-pages", k.RequireAuth, k.RecentReauth, k.Idempotency(ScopeVersionCreate), h.publishPage)
	v1.POST("/content-pages/:id/archive", k.RequireAuth, k.RecentReauth, k.Idempotency(ScopeArchive), h.archivePage)
}

// RegisterPublic 注册门户内容路由（均需登录）。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/content/pages", k.RequireAuth, h.listPortalPages)
	v1.GET("/content/pages/:slug", k.RequireAuth, h.getPortalPage)
}

// pathID 解析路径里的 :id；格式不对按不存在处理。
func pathID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return uuid.Nil, false
	}
	return id, true
}
