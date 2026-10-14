package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/route"
)

// registerIdentity 注册节点身份与安装令牌的后台路由（全部要登录）。签令牌要近期重认证并防重试；吊销要近期重认证。
func (h *Handler) registerIdentity(v1 *gin.RouterGroup, k route.Kit) {
	v1.POST("/servers/:id/bootstrap-token", k.RequireAuth, k.RecentReauth, k.Idempotency("server_bootstrap_token"), h.issueServerBootstrapToken)
	v1.POST("/nodes/bootstrap-token", k.RequireAuth, k.RecentReauth, k.Idempotency("node_bootstrap_token"), h.issueNodeBootstrapToken)
	v1.POST("/nodes/:id/server-token", k.RequireAuth, h.issueNodeServerToken)
	v1.GET("/nodes/:id/identity", k.RequireAuth, h.nodeIdentity)
	v1.POST("/nodes/:id/revoke-identity", k.RequireAuth, k.RecentReauth, h.revokeNodeIdentity)
}
