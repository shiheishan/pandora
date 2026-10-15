package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/route"
)

// registerIdentity 注册节点身份与安装令牌的后台路由（全部要登录）。签安装令牌只在服务器页有入口；吊销要近期重认证。
func (h *Handler) registerIdentity(v1 *gin.RouterGroup, k route.Kit) {
	v1.POST("/servers/:id/bootstrap-token", k.RequireAuth, k.RecentReauth, k.Idempotency("server_bootstrap_token"), h.issueServerBootstrapToken)
	v1.GET("/nodes/:id/identity", k.RequireAuth, h.nodeIdentity)
	v1.POST("/nodes/:id/revoke-identity", k.RequireAuth, k.RecentReauth, h.revokeNodeIdentity)
}
