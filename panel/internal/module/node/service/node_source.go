package service

// 订阅的可下发线路（装配为 subscription 的 NodeSource）。结果按（套餐版本, 用户组）缓存几秒，见 delivery_cache.go。

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// DeliverableNodes 实现 subscription 的 NodeSource：这份订阅能拿的线路，协议配置已是内核字段名，已按新鲜度分层排好序。
//
// 订阅生效、账号 active 由拉取路径先判过，这里只管节点：套餐版本绑的池里满足 repo.DeliverableNodeCond 的节点，
// 经 poolAdmits 按订阅主人的用户组过滤，再按 preferFresh 分层。
// 结果只取决于 q.PlanVersionID 与 q.UserGroupID（SubscriptionID、UserID 不参与），按这两项缓存 deliveryCacheTTL
// （delivery_cache.go）：返回的切片与其中的 Config 被所有命中者共享，调用方只读不改。永不为 nil。
func (s *Service) DeliverableNodes(ctx context.Context, q subsvc.NodeQuery) ([]render.Node, error) {
	if s.catalog == nil {
		return nil, errDeliveryPortMissing
	}
	versionID, groupID := q.PlanVersionID, q.UserGroupID
	return s.lineCache.get(ctx, deliveryKey(versionID, groupID), func(ctx context.Context) ([]render.Node, error) {
		return s.loadDeliverableNodes(ctx, versionID, groupID)
	})
}

// loadDeliverableNodes 现算一份线路，不经缓存。两次读：版本绑池（catalog）、候选节点（一条 SQL）。永不为 nil。
func (s *Service) loadDeliverableNodes(ctx context.Context, planVersionID uuid.UUID, userGroupID *uuid.UUID) ([]render.Node, error) {
	vp, err := s.catalog.VersionPools(ctx, []uuid.UUID{planVersionID})
	if err != nil {
		return nil, err
	}
	pools := vp[planVersionID]
	if len(pools) == 0 {
		return []render.Node{}, nil
	}
	loc, err := s.siteLoc(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.DeliveryNodes(ctx, pools, s.now().Add(-HeartbeatFreshWindow))
	if err != nil {
		return nil, err
	}
	return deliverableFromRows(rows, userGroupID, s.now(), loc)
}

// deliverableFromRows 把候选节点按池的用户组限定过滤（poolAdmits），协议配置翻成内核字段名，再按 preferFresh 分层。
// 每次调用都重新解析协议配置，结果与 rows 不共享引用。永不为 nil。
// 中转紧跟父节点、继承父节点的一切，只换名称地址端口与 SNI/Host。
func deliverableFromRows(rows []repo.DeliveryNodeRow, userGroupID *uuid.UUID, now time.Time, loc *time.Location) ([]render.Node, error) {
	cands := make([]deliveryCandidate, 0, len(rows))
	for _, row := range rows {
		if !poolAdmits(row.PoolGroupIDs, userGroupID) {
			continue
		}
		stored := map[string]any{}
		if row.ProtocolConfig != "" {
			if err := json.Unmarshal([]byte(row.ProtocolConfig), &stored); err != nil {
				return nil, fmt.Errorf("node: 节点 %s 的协议配置不是 JSON 对象: %w", row.ID, err)
			}
		}

		sched, err := decodeRateSchedule(row.RateSchedule)
		if err != nil {
			sched = nil
		}

		kernel := protocol.KernelConfig(row.NodeType, stored)
		var echConfigList []byte
		if len(row.ECHConfigList) > 0 && protocol.ECHCapable(row.NodeType, kernel) {
			echConfigList = row.ECHConfigList
		}

		parentNode := render.Node{
			Name: row.Name,
			Type: protocol.CanonicalType(row.NodeType),
			Host: row.Host,
			Port: row.Port,
			// Config 浅拷贝内核配置
			Config: kernel,
			// 倍率只作展示（客户端里的节点名后缀、门户预览），取装载时刻的生效倍率，缓存几秒内不变，转 float64 不参与任何计费
			TrafficRate:   effectiveRate(row.TrafficRate, sched, now, loc).InexactFloat64(),
			Tags:          decodeNodeTags(row.Tags),
			ECHConfigList: echConfigList,
		}

		cands = append(cands, deliveryCandidate{
			node:     parentNode,
			fresh:    row.HeartbeatFresh,
			degraded: row.Degraded,
		})

		for _, rel := range row.Relays {
			cands = append(cands, deliveryCandidate{
				node:     relayRenderNode(parentNode, rel),
				fresh:    row.HeartbeatFresh,
				degraded: row.Degraded,
			})
		}
	}
	return preferFresh(cands), nil
}
