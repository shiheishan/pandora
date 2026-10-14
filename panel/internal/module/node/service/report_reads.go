package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ReportNodeCounts 是看板的节点计数。
type ReportNodeCounts struct {
	Total  int64 // 未删除、未退役且 serving_status='active' 的节点
	Online int64 // 其中所在服务器心跳在 NodeStaleAfter 之内（与节点列表的 online 筛选同口径）
}

// ReportNodeCounts 一条 SQL 数出活跃节点总数与在线数。
func (s *Service) ReportNodeCounts(ctx context.Context, now time.Time) (ReportNodeCounts, error) {
	onlineSince := now.Add(-NodeStaleAfter)
	row, err := s.repo.ReportNodeCounts(ctx, onlineSince)
	if err != nil {
		return ReportNodeCounts{}, err
	}
	return ReportNodeCounts{Total: row.Total, Online: row.Online}, nil
}

// NodeRef 是失联节点的样例。
type NodeRef struct {
	ID   uuid.UUID
	Name string // display_name 非空用它，否则 name
}

// OfflineNodes 是失联节点统计：active 但所在服务器从没心跳或心跳超过 NodeStaleAfter（与节点列表的 offline 筛选同口径）。
type OfflineNodes struct {
	Count                 int64
	Sample                []NodeRef // 心跳最旧的至多 sample 个（从没心跳过的排最后）
	LongestOfflineSeconds *int64    // 最旧心跳距 now 的秒数；全都没心跳过或没有失联节点时为 nil
}

// OfflineNodes 一条 SQL 取失联节点的数量、样例与最长失联时长。
func (s *Service) OfflineNodes(ctx context.Context, now time.Time, sample int) (OfflineNodes, error) {
	onlineSince := now.Add(-NodeStaleAfter)
	rows, err := s.repo.OfflineNodes(ctx, now, onlineSince, sample)
	if err != nil {
		return OfflineNodes{}, err
	}
	if len(rows) == 0 {
		return OfflineNodes{Count: 0, Sample: []NodeRef{}}, nil
	}
	sampleOut := make([]NodeRef, len(rows))
	for i, r := range rows {
		sampleOut[i] = NodeRef{ID: r.ID, Name: r.Name}
	}
	return OfflineNodes{
		Count:                 rows[0].Total,
		Sample:                sampleOut,
		LongestOfflineSeconds: rows[0].Longest,
	}, nil
}

// ConfigLaggingCount 数配置落后的节点：落在 ServedOnServerCond 上、面板算出的期望配置摘要与节点回报的已应用摘要不同（含从没应用过）。
// 期望摘要经 serverConfigsWithSkips 现算（与节点列表的生效状态同一口径）；面板生成文档失败、本次下发不含的节点算落后
// （节点端拿不到它的配置，入站已停）；其余没有期望摘要的节点不计。
func (s *Service) ConfigLaggingCount(ctx context.Context) (int64, error) {
	rows, err := s.repo.ServedNodeApplied(ctx)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	serverSet := make(map[uuid.UUID]struct{})
	for _, row := range rows {
		serverSet[row.ServerID] = struct{}{}
	}
	serverIDs := make([]uuid.UUID, 0, len(serverSet))
	for id := range serverSet {
		serverIDs = append(serverIDs, id)
	}
	configs, skips, err := s.serverConfigsWithSkips(ctx, serverIDs)
	if err != nil {
		return 0, err
	}
	desired := make(map[uuid.UUID]string)
	for _, docs := range configs {
		for _, doc := range docs {
			desired[doc.NodeID] = doc.SHA256
		}
	}
	var lagging int64
	for _, row := range rows {
		if _, skipped := skips[row.NodeID]; skipped {
			lagging++
			continue
		}
		exp, ok := desired[row.NodeID]
		if !ok {
			continue
		}
		if row.AppliedSHA256 == nil || *row.AppliedSHA256 != exp {
			lagging++
		}
	}
	return lagging, nil
}

// NodeTraffic 是一个节点在窗口内的流量（实际字节，含名单外）。
type NodeTraffic struct {
	NodeID      uuid.UUID
	Name        string
	DisplayName *string
	Upload      int64
	Download    int64
	ActiveHours int64     // 有流量的小时桶数
	Cells       int64     // 有流量的（订阅, 小时）格子数，来自 node_user_traffic_hourly
	LastAt      time.Time // 有流量的小时桶里最新的 updated_at（最近一次入账）
}

// TrafficByNode 汇总小时桶起点落在 [from, to) 的节点流量；只返回上下行合计 > 0 的节点，顺序不保证。
func (s *Service) TrafficByNode(ctx context.Context, from, to time.Time) ([]NodeTraffic, error) {
	rows, err := s.repo.TrafficByNode(ctx, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]NodeTraffic, len(rows))
	for i, r := range rows {
		out[i] = NodeTraffic{
			NodeID: r.NodeID, Name: r.Name, DisplayName: r.DisplayName,
			Upload: r.Upload, Download: r.Download, ActiveHours: r.ActiveHours,
			Cells: r.Cells, LastAt: r.LastAt,
		}
	}
	return out, nil
}

// SubscriptionTraffic 是一份订阅在窗口内入账的流量（实际字节）。
type SubscriptionTraffic struct {
	SubscriptionID uuid.UUID
	Upload         int64
	Download       int64
	Cells          int64     // 有流量的（节点, 小时）格子数
	LastAt         time.Time // 有流量的格子里最新的 updated_at
}

// TrafficBySubscription 汇总小时桶起点落在 [from, to) 的订阅流量；只返回合计 > 0 的订阅，顺序不保证。
func (s *Service) TrafficBySubscription(ctx context.Context, from, to time.Time) ([]SubscriptionTraffic, error) {
	rows, err := s.repo.TrafficBySubscription(ctx, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]SubscriptionTraffic, len(rows))
	for i, r := range rows {
		out[i] = SubscriptionTraffic{
			SubscriptionID: r.SubscriptionID,
			Upload:         r.Upload, Download: r.Download,
			Cells: r.Cells, LastAt: r.LastAt,
		}
	}
	return out, nil
}

// TrafficQuality 是窗口内上报的质量计数（按服务器上报留档）。
type TrafficQuality struct {
	InvalidReports int64 // 含不合规条目的上报份数
	InvalidEntries int64 // 不合规条目合计
}

// TrafficQuality 统计 received_at 落在 [from, to) 的上报留档。
func (s *Service) TrafficQuality(ctx context.Context, from, to time.Time) (TrafficQuality, error) {
	row, err := s.repo.TrafficQuality(ctx, from, to)
	if err != nil {
		return TrafficQuality{}, err
	}
	return TrafficQuality{InvalidReports: row.InvalidReports, InvalidEntries: row.InvalidEntries}, nil
}
