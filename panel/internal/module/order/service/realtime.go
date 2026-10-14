package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/order/model"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// tableOrders 是实时事件里的表名，与前端 EventsCapsule 认的一致。
const tableOrders = "orders"

// UseRealtime 注入实时推送；只在启动装配时调用（不并发安全）。
func (s *Service) UseRealtime(p *realtime.Publisher) { s.rt = p }

// orderChanged 在调用方事务里登记「这张订单变了」，提交后推给订单主人与管理端；回滚不发。
// 必须在写库成功之后、事务函数返回之前调用。
func (s *Service) orderChanged(ctx context.Context, op string, o model.Order) {
	owner := o.UserID
	s.rt.Changed(ctx, realtime.TopicOrders, tableOrders, op, o.ID, &owner)
}
