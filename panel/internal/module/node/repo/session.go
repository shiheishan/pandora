// 节点通道会话写的列：服务器的自报信息与心跳时间、节点的运行状态与配置回执。各一条语句、按主键；
// 只在值真的变了时写（IS DISTINCT FROM），心跳时间除外（每拍写）。
package repo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ServerAgentInfo 是 Hello 带的自报信息；nil 存 NULL（未知）。调用方已按列约束截断与校验。
type ServerAgentInfo struct {
	AgentVersion, BinarySHA256, Hostname, OSName, Architecture *string
	CPUCores, MemoryMB, DiskGB                                 *int32
}

// UpdateServerAgent 写服务器的自报信息；与库里相同时不写。返回是否写了。已删除的服务器不写。
func (r *Repo) UpdateServerAgent(ctx context.Context, serverID uuid.UUID, in ServerAgentInfo) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE servers SET
  agent_version = ?, binary_sha256 = ?, hostname = ?, os_name = ?, architecture = ?,
  cpu_cores = ?, memory_mb = ?, disk_gb = ?
WHERE id = ? AND deleted_at IS NULL
  AND ROW(agent_version, binary_sha256, hostname, os_name, architecture, cpu_cores, memory_mb, disk_gb)
      IS DISTINCT FROM ROW(CAST(? AS text), CAST(? AS text), CAST(? AS text), CAST(? AS text), CAST(? AS text),
                           CAST(? AS integer), CAST(? AS integer), CAST(? AS integer))`,
		in.AgentVersion, in.BinarySHA256, in.Hostname, in.OSName, in.Architecture, in.CPUCores, in.MemoryMB, in.DiskGB,
		serverID,
		in.AgentVersion, in.BinarySHA256, in.Hostname, in.OSName, in.Architecture, in.CPUCores, in.MemoryMB, in.DiskGB)
	return res.RowsAffected > 0, res.Error
}

// TouchServerHeartbeat 写服务器的最后心跳时间（每拍写）。已删除的服务器不写。
func (r *Repo) TouchServerHeartbeat(ctx context.Context, serverID uuid.UUID, at time.Time) error {
	return r.d.Conn(ctx).Exec(`UPDATE servers SET last_heartbeat_at = ? WHERE id = ? AND deleted_at IS NULL`,
		at, serverID).Error
}

// NodeRuntimeRow 是一个节点的运行状态（Status 取 running / degraded / down；Reason 为 nil 存 NULL）。
type NodeRuntimeRow struct {
	NodeID       uuid.UUID  `json:"id"`
	Status       string     `json:"status"`
	Reason       *string    `json:"reason"`
	ReasonNodeID *uuid.UUID `json:"reason_node_id"`
}

// UpdateNodeRuntimes 一条语句写一拍里多个节点的运行状态：只写这台服务器名下、未删除、且状态真的变了的节点，
// runtime_state_at 只在变化时前进。runtime_reason_node_id 只保留同一台服务器上未删除的占用节点 id，否则置 NULL。
// 返回写了几行。rows 里节点 id 不重复（调用方去重）。
//
// 用 jsonb_to_recordset 传一批行：GORM 会把切片参数展开成逗号列表，unnest(?::uuid[]) 这类写法传不了数组。
func (r *Repo) UpdateNodeRuntimes(ctx context.Context, serverID uuid.UUID, rows []NodeRuntimeRow, at time.Time) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return 0, err
	}
	res := r.d.Conn(ctx).Exec(`WITH v AS (
  SELECT x.id, x.status, x.reason, o.id AS reason_node_id
  FROM jsonb_to_recordset(CAST(? AS jsonb)) AS x(id uuid, status text, reason text, reason_node_id uuid)
  LEFT JOIN nodes o ON o.id = x.reason_node_id AND o.server_id = ? AND o.deleted_at IS NULL
)
UPDATE nodes n SET
  runtime_status = v.status, runtime_reason = v.reason, runtime_reason_node_id = v.reason_node_id, runtime_state_at = ?
FROM v
WHERE n.id = v.id AND n.server_id = ? AND n.deleted_at IS NULL
  AND ROW(n.runtime_status, n.runtime_reason, n.runtime_reason_node_id)
      IS DISTINCT FROM ROW(v.status, v.reason, v.reason_node_id)`,
		string(payload), serverID, at, serverID)
	return res.RowsAffected, res.Error
}

// MarkConfigApplied 记节点已应用某版配置：写 applied_config_*，清掉 apply_failed_*（成功应用了更新的版本）。
// 已是这一版且没有失败记录时不写。只写这台服务器名下、未删除的节点。返回是否写了。
func (r *Repo) MarkConfigApplied(ctx context.Context, serverID, nodeID uuid.UUID, sha string, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE nodes SET
  applied_config_sha256 = ?, applied_config_at = ?,
  apply_failed_sha256 = NULL, apply_failed_reason = NULL, apply_failed_detail = NULL, apply_failed_at = NULL
WHERE id = ? AND server_id = ? AND deleted_at IS NULL
  AND (applied_config_sha256 IS DISTINCT FROM ? OR apply_failed_sha256 IS NOT NULL)`,
		sha, at, nodeID, serverID, sha)
	return res.RowsAffected > 0, res.Error
}

// MarkConfigFailed 记节点某版配置装不上：写 apply_failed_*（已应用的版本不动，旧配置仍在服务）。
// 与库里的失败记录（摘要、原因、详情）相同时不写。只写这台服务器名下、未删除的节点。返回是否写了。
func (r *Repo) MarkConfigFailed(ctx context.Context, serverID, nodeID uuid.UUID, sha, reason string, detail *string, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE nodes SET
  apply_failed_sha256 = ?, apply_failed_reason = ?, apply_failed_detail = ?, apply_failed_at = ?
WHERE id = ? AND server_id = ? AND deleted_at IS NULL
  AND ROW(apply_failed_sha256, apply_failed_reason, apply_failed_detail)
      IS DISTINCT FROM ROW(CAST(? AS text), CAST(? AS text), CAST(? AS text))`,
		sha, reason, detail, at, nodeID, serverID, sha, reason, detail)
	return res.RowsAffected > 0, res.Error
}
