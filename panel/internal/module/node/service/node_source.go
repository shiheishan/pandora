package service

// 订阅的可下发线路（p5-delivery 实现，装配为 subscription 的 NodeSource）。

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/subscription/render"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// DeliverableNodes 实现 subscription 的 NodeSource：这份订阅能拿的线路，协议配置已是内核字段名，已按新鲜度分层排好序。
func (s *Service) DeliverableNodes(ctx context.Context, q subsvc.NodeQuery) ([]render.Node, error) {
	panic("p5-delivery: DeliverableNodes")
}
