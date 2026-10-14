package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/route"
)

// registerXB 注册 Xboard 补齐的后台路由：中转、流量重置、批量修改、ECH。
func (h *Handler) registerXB(v1 *gin.RouterGroup, k route.Kit) {
	// 列出父节点下的中转条目
	v1.GET("/nodes/:id/relays", k.RequireAuth, h.listNodeRelays)
	// 新建中转（幂等）
	v1.POST("/nodes/:id/relays", k.RequireAuth, k.Idempotency("node_relay_create"), h.createNodeRelay)
	// 修改中转
	v1.PATCH("/node-relays/:id", k.RequireAuth, h.patchNodeRelay)
	// 删除中转
	v1.DELETE("/node-relays/:id", k.RequireAuth, h.deleteNodeRelay)
	// 手动重置本周期流量（重认证 + 幂等）
	v1.POST("/nodes/:id/traffic/reset", k.RequireAuth, k.RecentReauth, k.Idempotency("node_traffic_reset"), h.resetNodeTraffic)
	// gin 转义冒号：匹配字面路径 /nodes/edit:batch
	v1.POST("/nodes/edit\\:batch", k.RequireAuth, k.RecentReauth, k.Idempotency("node_batch_edit"), h.batchEditNodes)
	// 查看 ECH 状态
	v1.GET("/nodes/:id/ech", k.RequireAuth, h.getNodeECH)
	// 生成或轮换 ECH（重认证 + 幂等）
	v1.POST("/nodes/:id/ech", k.RequireAuth, k.RecentReauth, k.Idempotency("node_ech_generate"), h.generateNodeECH)
	// 停用 ECH（重认证）
	v1.DELETE("/nodes/:id/ech", k.RequireAuth, k.RecentReauth, h.disableNodeECH)
}
