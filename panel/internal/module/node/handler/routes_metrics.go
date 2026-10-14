package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/route"
)

// registerMetrics 注册节点指标的后台路由（要登录）。指标按服务器记，这里按节点取它所在服务器的曲线。
func (h *Handler) registerMetrics(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/nodes/:id/metrics", k.RequireAuth, h.nodeMetrics)
}
