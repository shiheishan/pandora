package grpc

// ControlService 的实现（p5-session）。

import (
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

// NewControlServer 构造 ControlService 的服务端实现；server.go 注册它。
func NewControlServer(svc *service.Service) nodev1.ControlServiceServer {
	panic("p5-session: NewControlServer")
}
