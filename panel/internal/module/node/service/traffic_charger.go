package service

// node 要的订阅扣量面与注入方法。
//
// 装配（主 Agent）：service.go 的 Service 加字段 `charger TrafficCharger`，app 里 `nodeSvc.UseTrafficCharger(subSvc)`。

import (
	"context"

	"github.com/google/uuid"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// TrafficCharger 是 node 要的订阅读写面（表归 subscription），由 subscription service 实现。
type TrafficCharger interface {
	// ChargeTraffic 在调用方事务里把一批计费流量记到订阅的配额、流量包与按日用量上，返回 uid → 订阅 id（找得到的）。
	ChargeTraffic(ctx context.Context, charges []subsvc.TrafficCharge) (map[int64]uuid.UUID, error)
	// SubscriptionIDsByNodeUIDs 批量把 node_uid 换成订阅 id（一条 SQL）。
	SubscriptionIDsByNodeUIDs(ctx context.Context, uids []int64) (map[int64]uuid.UUID, error)
}

var _ TrafficCharger = (*subsvc.Service)(nil)

// UseTrafficCharger 只在启动装配时调用（不并发安全）。没装配时流量与在线上报返回错误（会话层不回 ack，节点稍后重发）。
func (s *Service) UseTrafficCharger(c TrafficCharger) { s.charger = c }
