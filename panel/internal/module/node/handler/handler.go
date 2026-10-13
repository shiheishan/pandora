// Package handler 是 node 模块的 HTTP 层：服务器、节点、节点池、套餐绑池的后台接口，以及节点端一键安装的公开下载。
// 节点通道不走 HTTP（gRPC，见 grpc/）。
//
// 后台不分权限（docs/architecture.md 第 9 节）：每条路由都挂 k.RequireAuth；
// 影响多个节点或不可逆的写再挂 RecentReauth，防重试的写挂 Idempotency（RecentReauth 在前）。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

var (
	_ route.Module     = (*Handler)(nil)
	_ route.RootModule = (*Handler)(nil)
)

// Handler 是 node 模块的 HTTP 入口。
type Handler struct {
	svc *service.Service
}

// New 构造 Handler。
func New(svc *service.Service) *Handler { return &Handler{svc: svc} }

// RegisterPublic 门户没有节点接口。
func (h *Handler) RegisterPublic(_ *gin.RouterGroup, _ route.Kit) {}

// RegisterRoot 注册 public 根路径下的节点端一键安装（/pdnd/...）。
func (h *Handler) RegisterRoot(r gin.IRoutes) { h.registerPdndDist(r) }

// RegisterAdmin 注册后台路由，各主题在自己的文件里注册。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	h.registerInventory(v1, k)
	h.registerPools(v1, k)
	h.registerIdentity(v1, k)
	h.registerMetrics(v1, k)
	h.registerRouting(v1, k)
}
