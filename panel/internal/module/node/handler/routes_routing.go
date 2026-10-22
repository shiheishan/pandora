package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/route"
)

// registerRouting 注册出站、分流规则与路由组的后台路由。全部要登录；
// 中间件顺序 RequireAuth → RecentReauth → Idempotency。影响多个节点的写（全局发布、改组、删组、组内路由）
// 要近期重认证与幂等键；新建空组不影响节点，只要幂等；单节点路由与节点所在组只影响一个节点，靠节点行版本防并发覆盖。
func (h *Handler) registerRouting(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/nodes/routing", k.RequireAuth, h.getGlobalRouting)
	v1.PUT("/nodes/routing", k.RequireAuth, k.RecentReauth, k.Idempotency("node_routing_global_publish"), h.setGlobalRouting)
	v1.GET("/nodes/:id/routing", k.RequireAuth, h.getNodeRouting)
	v1.PUT("/nodes/:id/routing", k.RequireAuth, h.setNodeRouting)
	v1.GET("/nodes/:id/routing/effective", k.RequireAuth, h.previewNodeRouting)
	v1.PUT("/nodes/:id/route-groups", k.RequireAuth, h.setNodeRouteGroups)
	v1.GET("/route-groups", k.RequireAuth, h.listRouteGroups)
	v1.POST("/route-groups", k.RequireAuth, k.Idempotency("route_group_create"), h.createRouteGroup)
	v1.PATCH("/route-groups/:id", k.RequireAuth, k.RecentReauth, k.Idempotency("route_group_update"), h.updateRouteGroup)
	v1.DELETE("/route-groups/:id", k.RequireAuth, k.RecentReauth, k.Idempotency("route_group_delete"), h.deleteRouteGroup)
	v1.GET("/route-groups/:id/routing", k.RequireAuth, h.getGroupRouting)
	v1.PUT("/route-groups/:id/routing", k.RequireAuth, k.RecentReauth, k.Idempotency("route_group_routing_publish"), h.setGroupRouting)
}
