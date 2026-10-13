package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/route"
)

// registerPools 注册节点池与套餐绑池的后台路由（全部要登录）。
// 节点池的建、改只有请求里带了 allowed_user_group_ids 才要求近期重认证（在处理函数里按字段调 k.RecentReauth）。
// /plans/:id/pools 放在 node：要节点数据，而 catalog 不依赖 node；绑池行的写入调 catalog service。
func (h *Handler) registerPools(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/node-pools", k.RequireAuth, h.listPools)
	v1.POST("/node-pools", k.RequireAuth, h.createPool(k.RecentReauth))
	v1.POST("/node-pools/:id", k.RequireAuth, h.updatePool(k.RecentReauth))
	v1.DELETE("/node-pools/:id", k.RequireAuth, h.deletePool)
	v1.GET("/plans/:id/pools", k.RequireAuth, h.planPools)
	v1.POST("/plans/:id/pools", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_pools_update"), h.setPlanPools)
}
