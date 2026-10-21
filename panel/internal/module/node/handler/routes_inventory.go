package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/route"
)

// registerInventory 注册服务器与节点的后台路由。全部要登录；
// 中间件顺序 RequireAuth → RecentReauth → Idempotency。
// 批量在线升级要近期重认证与幂等键。
// 节点通道相关接口（身份、接入令牌、指标、路由）分别在 routes_identity.go、routes_metrics.go、routes_routing.go 注册。
func (h *Handler) registerInventory(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/nodes", k.RequireAuth, h.listNodes)
	v1.POST("/nodes", k.RequireAuth, k.Idempotency("node_create"), h.createNode)
	v1.PUT("/nodes/order", k.RequireAuth, h.reorderNodes)
	// gin 1.12 的转义冒号：匹配字面路径 /nodes/status:batch
	v1.POST("/nodes/status\\:batch", k.RequireAuth, k.Idempotency("node_status_batch"), h.batchNodeStatus)
	v1.POST("/nodes/reality-keypair", k.RequireAuth, h.realityKeypair)
	v1.PATCH("/nodes/:id", k.RequireAuth, h.patchNode)
	v1.DELETE("/nodes/:id", k.RequireAuth, k.RecentReauth, h.deleteNode)
	v1.POST("/nodes/:id/bind", k.RequireAuth, k.Idempotency("node_bind"), h.bindNode)
	v1.POST("/nodes/:id/unbind", k.RequireAuth, k.Idempotency("node_unbind"), h.unbindNode)
	v1.POST("/nodes/:id/activate", k.RequireAuth, k.Idempotency("node_activate"), h.activateNode)
	v1.POST("/nodes/:id/retire", k.RequireAuth, k.RecentReauth, k.Idempotency("node_retire"), h.retireNode)
	v1.GET("/node-protocol-schemas", k.RequireAuth, h.protocolSchemas)

	v1.GET("/servers", k.RequireAuth, h.listServers)
	v1.POST("/servers", k.RequireAuth, h.createServer)
	v1.POST("/servers/upgrade", k.RequireAuth, k.RecentReauth, k.Idempotency("server_upgrade"), h.upgradeServers)
	v1.GET("/servers/:id", k.RequireAuth, h.getServer)
	v1.PATCH("/servers/:id", k.RequireAuth, h.patchServer)
	v1.POST("/servers/:id/status", k.RequireAuth, h.setServerStatus)
	v1.DELETE("/servers/:id", k.RequireAuth, k.RecentReauth, h.deleteServer)
}
