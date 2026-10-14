package service

// 订阅的可下发线路（p5-delivery 实现，装配为 subscription 的 NodeSource）。

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// DeliverableNodes 实现 subscription 的 NodeSource：这份订阅能拿的线路，协议配置已是内核字段名，已按新鲜度分层排好序。
//
// 订阅生效、账号 active 由拉取路径先判过，这里只管节点：套餐版本绑的池里满足 repo.DeliverableNodeCond 的节点，
// 经 poolAdmits 按订阅主人的用户组过滤，再按 preferFresh 分层。两次读：版本绑池（catalog）、候选节点（一条 SQL）。
// 永不为 nil。
func (s *Service) DeliverableNodes(ctx context.Context, q subsvc.NodeQuery) ([]render.Node, error) {
	if s.catalog == nil {
		return nil, errDeliveryPortMissing
	}
	vp, err := s.catalog.VersionPools(ctx, []uuid.UUID{q.PlanVersionID})
	if err != nil {
		return nil, err
	}
	pools := vp[q.PlanVersionID]
	if len(pools) == 0 {
		return []render.Node{}, nil
	}
	rows, err := s.repo.DeliveryNodes(ctx, pools, s.now().Add(-HeartbeatFreshWindow))
	if err != nil {
		return nil, err
	}
	cands := make([]deliveryCandidate, 0, len(rows))
	for _, row := range rows {
		if !poolAdmits(row.PoolGroupIDs, q.UserGroupID) {
			continue
		}
		stored := map[string]any{}
		if row.ProtocolConfig != "" {
			if err := json.Unmarshal([]byte(row.ProtocolConfig), &stored); err != nil {
				return nil, fmt.Errorf("node: 节点 %s 的协议配置不是 JSON 对象: %w", row.ID, err)
			}
		}
		cands = append(cands, deliveryCandidate{
			node: render.Node{
				Name:   row.Name,
				Type:   protocol.CanonicalType(row.NodeType),
				Host:   row.Host,
				Port:   row.Port,
				Config: protocol.KernelConfig(row.NodeType, stored),
				// 倍率只作展示（客户端里的节点名后缀、门户预览），转 float64 不参与任何计费
				TrafficRate: row.TrafficRate.InexactFloat64(),
			},
			fresh:    row.HeartbeatFresh,
			degraded: row.Degraded,
		})
	}
	return preferFresh(cands), nil
}
