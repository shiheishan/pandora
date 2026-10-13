package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

var nodeListStates = []string{"", "all", "online", "offline", "disabled", "retired"}

const nodeSearchMax = 120

// nodeListExtras 是一页节点批量取来的附加读数，按 id 查。
type nodeListExtras struct {
	live    map[uuid.UUID]NodeLiveStat          // 节点 id →
	metrics map[uuid.UUID]ServerMetricsSnapshot // 服务器 id →
	seqs    map[uuid.UUID]int32                 // 服务器 id → active 身份 seq
	usage   map[uuid.UUID]catalogsvc.PoolUsage  // 池 id →
	desired map[uuid.UUID]string                // 节点 id → 期望配置文档 SHA-256
}

// QueryNodes 返回后台节点列表的一页与筛选后的总数；只读，ListNodePage 一次，
// 非空页再做固定条数的批量读（在线与流量、最新指标、身份序号、池套餐引用、期望配置文档，共约 8 条查询），与页大小无关；
// granted_plans 来自 catalog PoolUsage（绑定了该池的任意套餐版本所属套餐名）。
func (s *Service) QueryNodes(ctx context.Context, q NodeQuery) ([]NodeRow, int64, error) {
	q.Search = strings.TrimSpace(q.Search)
	if utf8.RuneCountInString(q.Search) > nodeSearchMax {
		return nil, 0, errs.Invalid(map[string]string{"q": "搜索词太长"})
	}
	if !slices.Contains(nodeListStates, q.State) {
		return nil, 0, errs.Invalid(map[string]string{"state": "只能是 all、online、offline、disabled、retired 之一"})
	}
	if q.State != "" {
		q.IncludeRetired = true
	}
	if s.catalog == nil {
		return nil, 0, errPoolPortMissing
	}

	rows, total, err := s.repo.ListNodePage(ctx, repo.NodeListFilter{
		IncludeRetired: q.IncludeRetired,
		State:          q.State,
		Search:         q.Search,
		ID:             q.ID,
		Limit:          q.Limit,
		Offset:         q.Offset,
		Detail:         q.ID != nil,
	})
	if err != nil {
		return nil, 0, err
	}

	now := s.now()
	var extras nodeListExtras
	if len(rows) > 0 {
		extras, err = s.nodeListExtrasFor(ctx, rows)
		if err != nil {
			return nil, 0, err
		}
	}
	out := make([]NodeRow, 0, len(rows)) // 空页也编成 []
	for _, row := range rows {
		out = append(out, nodeRowFromList(row, now, q.ID != nil, extras))
	}
	return out, total, nil
}

// dedupeUUIDs 返回排序去重后的副本。
func dedupeUUIDs(ids []uuid.UUID) []uuid.UUID {
	if len(ids) == 0 {
		return ids
	}
	ids = slices.Clone(ids)
	slices.SortFunc(ids, compareUUID)
	return slices.Compact(ids)
}

// nodeListExtrasFor 对一页节点做固定条数的批量读，不逐行查。
func (s *Service) nodeListExtrasFor(ctx context.Context, rows []repo.NodeListRow) (nodeListExtras, error) {
	var nodeIDs, serverIDs, poolIDs []uuid.UUID
	for _, row := range rows {
		nodeIDs = append(nodeIDs, row.ID)
		if row.ServerID != nil {
			serverIDs = append(serverIDs, *row.ServerID)
		}
		if row.PoolID != nil {
			poolIDs = append(poolIDs, *row.PoolID)
		}
	}
	serverIDs = dedupeUUIDs(serverIDs)
	poolIDs = dedupeUUIDs(poolIDs)

	live, err := s.NodeLiveStats(ctx, nodeIDs)
	if err != nil {
		return nodeListExtras{}, err
	}
	metrics, err := s.LatestServerMetrics(ctx, serverIDs)
	if err != nil {
		return nodeListExtras{}, err
	}
	seqs, err := s.repo.ActiveIdentitySeqs(ctx, serverIDs, s.identityNow())
	if err != nil {
		return nodeListExtras{}, err
	}
	usage, err := s.catalog.PoolUsage(ctx, poolIDs)
	if err != nil {
		return nodeListExtras{}, err
	}
	desired := make(map[uuid.UUID]string)
	// 某个节点的配置拼不出文档（协议配置不是对象等）时不让整个列表 500，否则连去修它的页面都打不开；
	// 记警告，期望摘要留空，生效状态退回只看节点回报。
	configs, err := s.ServerConfigs(ctx, serverIDs)
	if err != nil {
		plog.From(ctx).Warn("节点列表算期望配置失败，生效状态只看节点回报", zap.Error(err))
		configs = nil
	}
	for _, docs := range configs {
		for _, doc := range docs {
			desired[doc.NodeID] = doc.SHA256
		}
	}
	return nodeListExtras{
		live:    live,
		metrics: metrics,
		seqs:    seqs,
		usage:   usage,
		desired: desired,
	}, nil
}

// nodeRowFromList 把一行读模型转成 NodeRow：心跳取所在服务器的，下发说明按 nodeDeliveryVerdict；
// 在线、指标、身份序号、池绑套餐与期望摘要从 nodeListExtras 填。
func nodeRowFromList(row repo.NodeListRow, now time.Time, detail bool, x nodeListExtras) NodeRow {
	hb := row.ServerLastHeartbeatAt
	stale := hb == nil || now.Sub(*hb) >= NodeStaleAfter

	poolActive := row.PoolID != nil && row.PoolStatus != nil && *row.PoolStatus == "active"
	everSeen := hb != nil
	beatFresh := everSeen && now.Sub(*hb) < nodeDeliveryFreshWindow

	poolBound := false
	if row.PoolID != nil {
		poolBound = x.usage[*row.PoolID].Versions > 0
	}

	var desired *string
	if sha, ok := x.desired[row.ID]; ok {
		desired = &sha
	}

	facts := nodeDeliveryFacts{
		Serving:     row.ServingStatus,
		Pooled:      row.PoolID != nil,
		PoolActive:  poolActive,
		PoolBound:   poolBound,
		EverSeen:    everSeen,
		BeatFresh:   beatFresh,
		Deliverable: row.Deliverable,
		ServerReady: row.ServerReady,
		Degraded:    nodeDegraded(row),
	}
	delivered, note := nodeDeliveryVerdict(facts)
	eff, fail := nodeEffectiveState(row, desired)

	nr := NodeRow{
		ID:                row.ID,
		NodeNo:            row.NodeNo,
		RowVersion:        row.RowVersion,
		Name:              row.Name,
		ServingStatus:     row.ServingStatus,
		ServerID:          row.ServerID,
		ServerName:        row.ServerName,
		PoolID:            row.PoolID,
		PoolName:          row.PoolName,
		AgentVersion:      row.AgentVersion,
		Hostname:          row.Hostname,
		PublicIPv4:        row.PublicIPv4,
		CPUCores:          row.CPUCores,
		MemoryMB:          row.MemoryMB,
		DiskGB:            row.DiskGB,
		LastHeartbeatAt:   hb,
		Stale:             stale,
		Delivered:         delivered,
		DeliveryNote:      note,
		CreatedAt:         row.CreatedAt,
		NodeType:          row.NodeType,
		ServerHost:        row.ServerHost,
		ServerPort:        row.ServerPort,
		DisplayName:       row.DisplayName,
		CountryCode:       row.CountryCode,
		SortOrder:         row.SortOrder,
		EffectiveState:    eff,
		LastApplyFailure:  fail,
		RuntimeStatus:     row.RuntimeStatus,
		RuntimeReason:     row.RuntimeReason,
		RuntimeStateAt:    row.RuntimeStateAt,
		RuntimeReasonNode: row.RuntimeReasonNode,
		DeliveryDegraded:  nodeDegraded(row),
		GrantedPlans:      []string{},
	}

	if live, ok := x.live[row.ID]; ok {
		nr.OnlineUsers = live.OnlineUsers
		nr.OnlineIPs = live.OnlineIPs
		nr.TrafficBytes24h = live.TrafficBytes24h
	}
	if row.ServerID != nil {
		sid := *row.ServerID
		if m, ok := x.metrics[sid]; ok {
			cpu := m.CPUPercent
			nr.CPUPercent = &cpu
			nr.MemPercent = m.MemPercent
			at := m.At
			nr.MetricsAt = &at
		}
		if seq, ok := x.seqs[sid]; ok {
			v := int64(seq)
			nr.IdentitySerial = &v
		}
	}
	if row.PoolID != nil {
		if u, ok := x.usage[*row.PoolID]; ok && len(u.PlanNames) > 0 {
			nr.GrantedPlans = slices.Clone(u.PlanNames)
		}
	}

	if detail {
		raw := "{}"
		if row.ProtocolConfig != nil && *row.ProtocolConfig != "" {
			raw = *row.ProtocolConfig
		}
		redacted := protocol.Redact(json.RawMessage(raw))
		var warns []string
		if row.NodeType != nil {
			warns = protocol.Warnings(*row.NodeType, json.RawMessage(raw))
		}
		if len(warns) == 0 {
			warns = nil
		}
		nr.Detail = &NodeDetail{
			TrafficRate:           row.TrafficRate,
			Kernel:                row.Kernel,
			ProtocolConfig:        redacted,
			ProtocolSchemaVersion: row.ProtocolSchemaVersion,
			ConfigValidatedAt:     row.ConfigValidatedAt,
			Warnings:              warns,
		}
	}
	return nr
}
