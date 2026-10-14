package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// NodeDegradedCond 是「节点报告它的入站没按期望在服务」的唯一口径（别名 n），恒为 true / false：
// 运行状态是 degraded（新配置没装上，旧配置还在服务）或 down（入站没在服务），或最近一次应用配置失败
// （apply_failed_*，成功应用更新的版本后清空）。降级节点不排除、只在线路分层里排到后面（service.preferFresh）。
const NodeDegradedCond = `(coalesce(n.runtime_status IN ('degraded', 'down'), false) OR n.apply_failed_sha256 IS NOT NULL)`

// DeliveryRelay 是一条已启用的中转线路定义。
type DeliveryRelay struct {
	Name       string  `json:"name"`
	Host       string  `json:"host"`
	Port       int     `json:"port"`
	SNI        *string `json:"sni"`
	HostHeader *string `json:"host_header"`
}

// DeliveryNodeRow 是一条候选线路：池里满足 DeliverableNodeCond 的节点。
type DeliveryNodeRow struct {
	ID     uuid.UUID
	PoolID uuid.UUID
	// Name 是 display_name，空则用 name
	Name     string
	NodeType string
	Host     string
	Port     int
	// ProtocolConfig 是库里的存储形状（JSON 对象文本）
	ProtocolConfig string
	TrafficRate    decimal.Decimal
	// Tags 是节点标签（jsonb 数组文本）
	Tags string
	// RateSchedule 是时段倍率（jsonb 数组文本）
	RateSchedule string
	// ECHConfigList 是当前有效 ECH 钥的公开配置列表字节（nil 表示未开）
	ECHConfigList []byte
	// Relays 是已启用的中转列表
	Relays []DeliveryRelay
	// HeartbeatFresh 是服务器心跳不早于 freshSince
	HeartbeatFresh bool
	// Degraded 按 NodeDegradedCond
	Degraded bool
	// PoolGroupIDs 是所在池的「仅限用户组」名单（空 = 不限定）
	PoolGroupIDs []uuid.UUID
}

type deliveryNodeScan struct {
	ID             uuid.UUID       `gorm:"column:id"`
	PoolID         uuid.UUID       `gorm:"column:pool_id"`
	Name           string          `gorm:"column:name"`
	NodeType       string          `gorm:"column:node_type"`
	Host           string          `gorm:"column:host"`
	Port           int             `gorm:"column:port"`
	ProtocolConfig string          `gorm:"column:protocol_config"`
	TrafficRate    decimal.Decimal `gorm:"column:traffic_rate"`
	Tags           string          `gorm:"column:tags"`
	RateSchedule   string          `gorm:"column:rate_schedule"`
	ECHConfigList  []byte          `gorm:"column:ech_config_list"`
	Relays         string          `gorm:"column:relays"`
	HeartbeatFresh bool            `gorm:"column:heartbeat_fresh"`
	Degraded       bool            `gorm:"column:degraded"`
	PoolGroupIDs   string          `gorm:"column:pool_group_ids"`
}

// DeliveryNodes 一条 SQL 取这些池里能发给用户的节点（DeliverableNodeCond），带分层事实与池的用户组名单，
// 按 sort_order、node_no 排序。节点按 nodes_pool_idx 取，池的名单按 node_pool_user_groups 主键前缀取。
// SQL 中附带：
// 1. tags：节点标签（jsonb 文本）
// 2. rate_schedule：时段倍率（jsonb 文本）
// 3. ech_config_list：走 node_ech_keys_current_key 索引取得的当前有效公开 ECH 配置列表字节
// 4. relays：走 node_relays_parent_idx 索引聚合的已启用中转线路 JSON 数组
func (r *Repo) DeliveryNodes(ctx context.Context, poolIDs []uuid.UUID, freshSince time.Time) ([]DeliveryNodeRow, error) {
	if len(poolIDs) == 0 {
		return []DeliveryNodeRow{}, nil
	}
	var rows []deliveryNodeScan
	err := r.d.Conn(ctx).Raw(`
		SELECT n.id, n.pool_id,
		       coalesce(nullif(n.display_name, ''), n.name) AS name,
		       n.node_type, n.server_host AS host, n.server_port AS port,
		       n.protocol_config, n.traffic_rate,
		       n.tags::text AS tags,
		       n.traffic_rate_schedule::text AS rate_schedule,
		       (SELECT k.config_list FROM node_ech_keys k WHERE k.node_id = n.id AND k.superseded_at IS NULL) AS ech_config_list,
		       coalesce((SELECT jsonb_agg(jsonb_build_object('name', r.name, 'host', r.server_host, 'port', r.server_port,
		                                                     'sni', r.sni, 'host_header', r.host_header)
		                                 ORDER BY r.sort_order, r.id)
		                  FROM node_relays r WHERE r.parent_node_id = n.id AND r.enabled), '[]'::jsonb)::text AS relays,
		       coalesce(s.last_heartbeat_at >= ?, false) AS heartbeat_fresh,
		       `+NodeDegradedCond+` AS degraded,
		       array_to_string(ARRAY(SELECT g.user_group_id::text FROM node_pool_user_groups g
		                              WHERE g.pool_id = n.pool_id ORDER BY 1), ',') AS pool_group_ids
		  FROM nodes n
		  JOIN servers s ON s.id = n.server_id
		 WHERE n.pool_id IN ? AND `+DeliverableNodeCond+`
		 ORDER BY n.sort_order, n.node_no`, freshSince, poolIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]DeliveryNodeRow, len(rows))
	for i, row := range rows {
		groups, err := parseUUIDList(row.PoolGroupIDs)
		if err != nil {
			return nil, err
		}
		var relays []DeliveryRelay
		if row.Relays != "" {
			if err := json.Unmarshal([]byte(row.Relays), &relays); err != nil {
				return nil, fmt.Errorf("node: 中转列表解析失败: %w", err)
			}
		}
		out[i] = DeliveryNodeRow{
			ID: row.ID, PoolID: row.PoolID, Name: row.Name, NodeType: row.NodeType,
			Host: row.Host, Port: row.Port, ProtocolConfig: row.ProtocolConfig, TrafficRate: row.TrafficRate,
			Tags: row.Tags, RateSchedule: row.RateSchedule, ECHConfigList: row.ECHConfigList, Relays: relays,
			HeartbeatFresh: row.HeartbeatFresh, Degraded: row.Degraded, PoolGroupIDs: groups,
		}
	}
	return out, nil
}

// parseUUIDList 解析逗号分隔的 uuid 列表；空串是空列表。
func parseUUIDList(s string) ([]uuid.UUID, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]uuid.UUID, len(parts))
	for i, p := range parts {
		id, err := uuid.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("node: 池的用户组名单解析失败: %w", err)
		}
		out[i] = id
	}
	return out, nil
}
