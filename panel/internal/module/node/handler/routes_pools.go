package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/route"
)

// registerPools 注册节点池与套餐绑池的后台路由（全部要登录）。
// 节点池的建、改只有请求里带了 allowed_user_group_ids 才要求近期重认证（在处理函数里按字段调 k.RecentReauth）。
// /plans/:id/pools（GET）放在 node：选择器要节点数据；改绑走 catalog 的 PUT /plans/:id/pools。
func (h *Handler) registerPools(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/node-pools", k.RequireAuth, h.listPools)
	v1.POST("/node-pools", k.RequireAuth, h.createPool(k.RecentReauth))
	v1.POST("/node-pools/:id", k.RequireAuth, h.updatePool(k.RecentReauth))
	v1.DELETE("/node-pools/:id", k.RequireAuth, h.deletePool)
	v1.GET("/plans/:id/pools", k.RequireAuth, h.planPools)
}
