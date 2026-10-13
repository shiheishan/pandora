package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

var nodeListStates = []string{"", "all", "online", "offline", "disabled", "retired"}

const nodeSearchMax = 120

// QueryNodes 返回后台节点列表的一页与筛选后的总数；只读，只调一次 ListNodePage。
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
	out := make([]NodeRow, 0, len(rows)) // 空页也编成 []
	for _, row := range rows {
		out = append(out, nodeRowFromList(row, now, q.ID != nil))
	}
	return out, total, nil
}

// nodeRowFromList 把一行读模型转成 NodeRow：心跳取所在服务器的，下发说明按 nodeDeliveryVerdict。
func nodeRowFromList(row repo.NodeListRow, now time.Time, detail bool) NodeRow {
	hb := row.ServerLastHeartbeatAt
	stale := hb == nil || now.Sub(*hb) >= NodeStaleAfter

	poolActive := row.PoolID != nil && row.PoolStatus != nil && *row.PoolStatus == "active"
	everSeen := hb != nil
	beatFresh := everSeen && now.Sub(*hb) < nodeDeliveryFreshWindow

	facts := nodeDeliveryFacts{
		Serving:     row.ServingStatus,
		Pooled:      row.PoolID != nil,
		PoolActive:  poolActive,
		EverSeen:    everSeen,
		BeatFresh:   beatFresh,
		Deliverable: row.Deliverable,
		ServerReady: row.ServerReady,
		Degraded:    nodeDegraded(row),
	}
	delivered, note := nodeDeliveryVerdict(facts)
	eff, fail := nodeEffectiveState(row, nil)

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
