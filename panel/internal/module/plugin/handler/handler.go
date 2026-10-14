// Package handler 是 plugin 模块的 HTTP 层：后台插件钩子的列表、保存、删除、投递记录与测试投递。
package handler

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/plugin/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// Service 是 handler 用到的 plugin service 方法；*service.Service 实现它。
type Service interface {
	List(ctx context.Context) ([]service.Hook, error)
	SaveHook(ctx context.Context, in service.SaveHookInput) (string, error)
	DeleteHook(ctx context.Context, code string) error
	Deliveries(ctx context.Context, code string, limit int) ([]service.Delivery, error)
	TestHook(ctx context.Context, code string) (service.TestResult, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// ScopeHookSave 是保存钩子的幂等作用域。
const ScopeHookSave = "plugin_hook_save"

const deliveriesLimit = 50

// Handler 是 plugin 模块的 HTTP 入口。
type Handler struct{ svc Service }

// New 构造 Handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// RegisterAdmin 注册后台插件钩子路由（写操作要重认证）。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/plugin-hooks", k.RequireAuth, h.list)
	v1.POST("/plugin-hooks", k.RequireAuth, k.RecentReauth, k.Idempotency(ScopeHookSave), h.save)
	v1.DELETE("/plugin-hooks/:code", k.RequireAuth, k.RecentReauth, h.delete)
	v1.GET("/plugin-hooks/:code/deliveries", k.RequireAuth, h.deliveries)
	v1.POST("/plugin-hooks/:code/test", k.RequireAuth, k.RecentReauth, h.test)
}

// RegisterPublic 门户没有插件接口。
func (h *Handler) RegisterPublic(*gin.RouterGroup, route.Kit) {}

func (h *Handler) list(c *gin.Context) {
	rows, err := h.svc.List(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	hooks := make([]hookView, 0, len(rows))
	for _, r := range rows {
		hooks = append(hooks, hookFrom(r))
	}
	events := make([]eventView, 0, len(service.Events))
	for _, e := range service.Events {
		events = append(events, eventFrom(e))
	}
	httpx.OK(c, listResponse{Events: events, Hooks: hooks})
}

func (h *Handler) save(c *gin.Context) {
	var req saveRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	secret, err := h.svc.SaveHook(c.Request.Context(), service.SaveHookInput{
		Code: req.Code, Name: req.Name, Description: req.Description,
		Enabled: req.Enabled, Events: req.Events, EndpointURL: req.EndpointURL,
		Secret: req.Secret, TimeoutMS: req.TimeoutMS, MaxAttempts: req.MaxAttempts,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp := saveResponse{Saved: true}
	if secret != "" {
		resp.Secret = secret
		resp.SecretHint = "签名密钥只显示这一次，请立刻填进插件那边的配置"
	}
	httpx.OK(c, resp)
}

func (h *Handler) delete(c *gin.Context) {
	if err := h.svc.DeleteHook(c.Request.Context(), c.Param("code")); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, deleteResponse{Deleted: true})
}

func (h *Handler) deliveries(c *gin.Context) {
	rows, err := h.svc.Deliveries(c.Request.Context(), c.Param("code"), deliveriesLimit)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]deliveryView, 0, len(rows))
	for _, r := range rows {
		out = append(out, deliveryFrom(r))
	}
	httpx.OK(c, deliveriesResponse{Deliveries: out})
}

func (h *Handler) test(c *gin.Context) {
	res, err := h.svc.TestHook(c.Request.Context(), c.Param("code"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, testResponse{
		DurationMS:   res.DurationMS,
		ResponseCode: res.ResponseCode,
		Sent:         true,
	})
}
