// Package handler 是 appearance 模块的 HTTP 层：后台主题与插槽、门户外观。
package handler

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/appearance/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// Service 是 handler 用到的 appearance service 方法；*service.Service 实现它。
type Service interface {
	ListThemes(ctx context.Context) ([]service.Theme, error)
	SaveTheme(ctx context.Context, in service.SaveThemeInput) ([]string, error)
	ActivateTheme(ctx context.Context, code string) error
	DeleteTheme(ctx context.Context, code string) error
	ListSlots(ctx context.Context) ([]service.Slot, error)
	SaveSlot(ctx context.Context, in service.SaveSlotInput) ([]string, error)
	Public(ctx context.Context) (service.PublicAppearance, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// 幂等作用域（沿用旧接口）。
const (
	ScopeThemeSave = "appearance_theme_save"
	ScopeSlotSave  = "appearance_slot_save"
)

// Handler 是 appearance 模块的 HTTP 入口。
type Handler struct{ svc Service }

// New 构造 Handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// RegisterAdmin 注册后台主题与插槽路由（写操作要重认证）。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/themes", k.RequireAuth, h.listThemes)
	v1.POST("/themes", k.RequireAuth, k.RecentReauth, k.Idempotency(ScopeThemeSave), h.saveTheme)
	v1.POST("/themes/:code/activate", k.RequireAuth, k.RecentReauth, h.activateTheme)
	v1.DELETE("/themes/:code", k.RequireAuth, k.RecentReauth, h.deleteTheme)
	v1.GET("/slots", k.RequireAuth, h.listSlots)
	v1.POST("/slots/:key", k.RequireAuth, k.RecentReauth, k.Idempotency(ScopeSlotSave), h.saveSlot)
}

// RegisterPublic 注册门户外观（登录页也要按主题渲染，免登录）。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, _ route.Kit) {
	v1.GET("/appearance", h.publicAppearance)
}

func (h *Handler) listThemes(c *gin.Context) {
	rows, err := h.svc.ListThemes(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]themeView, 0, len(rows))
	for _, r := range rows {
		out = append(out, themeFrom(r))
	}
	httpx.OK(c, listThemesResponse{Themes: out})
}

func (h *Handler) saveTheme(c *gin.Context) {
	var req saveThemeRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	notes, err := h.svc.SaveTheme(c.Request.Context(), service.SaveThemeInput{
		Code: req.Code, Name: req.Name, Create: req.Create,
		Tokens: req.Tokens, Branding: req.Branding, CustomCSS: req.CustomCSS,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, saveThemeResponse{Dropped: notes, Saved: true})
}

func (h *Handler) activateTheme(c *gin.Context) {
	if err := h.svc.ActivateTheme(c.Request.Context(), c.Param("code")); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, activateThemeResponse{Activated: true})
}

func (h *Handler) deleteTheme(c *gin.Context) {
	if err := h.svc.DeleteTheme(c.Request.Context(), c.Param("code")); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, deleteThemeResponse{Deleted: true})
}

func (h *Handler) listSlots(c *gin.Context) {
	rows, err := h.svc.ListSlots(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]slotView, 0, len(rows))
	for _, r := range rows {
		out = append(out, slotFrom(r))
	}
	httpx.OK(c, listSlotsResponse{Slots: out})
}

func (h *Handler) saveSlot(c *gin.Context) {
	var req saveSlotRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	notes, err := h.svc.SaveSlot(c.Request.Context(), service.SaveSlotInput{
		Key: c.Param("key"), Content: req.Content, Enabled: req.Enabled,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, saveSlotResponse{Dropped: notes, Saved: true})
}

func (h *Handler) publicAppearance(c *gin.Context) {
	out, err := h.svc.Public(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, publicFrom(out))
}
