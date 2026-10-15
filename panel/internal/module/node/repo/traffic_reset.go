package repo

// 节点本周期流量清零（后台手动重置、按重置日的定时任务）。

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// NodeUsageBefore 是清零前的用量与是否超限。
type NodeUsageBefore struct {
	UsedBytes int64 `gorm:"column:used_bytes"`
	Exceeded  bool  `gorm:"column:exceeded"`
}

// ResetNodeUsage 在调用方事务里清零一个未删除节点的本周期用量：traffic_used_bytes = 0、traffic_exceeded_at = NULL、
// traffic_reset_at = now()，一条 UPDATE，RETURNING 用 PG18 的 old 取清零前的用量与超限标记。节点不存在或已删除返回 found=false。
func (r *Repo) ResetNodeUsage(ctx context.Context, id uuid.UUID) (before NodeUsageBefore, found bool, err error) {
	var rows []NodeUsageBefore
	err = r.d.Conn(ctx).Raw(`
		UPDATE nodes
		   SET traffic_used_bytes = 0, traffic_exceeded_at = NULL, traffic_reset_at = now()
		 WHERE id = ? AND deleted_at IS NULL
		RETURNING old.traffic_used_bytes AS used_bytes, (old.traffic_exceeded_at IS NOT NULL) AS exceeded`, id).
		Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return NodeUsageBefore{}, false, err
	}
	return rows[0], true, nil
}

// NodeResetCandidate 是设了重置日的未删除节点。
type NodeResetCandidate struct {
	ID       uuid.UUID `gorm:"column:id"`
	ResetDay int       `gorm:"column:traffic_reset_day"`
	ResetAt  time.Time `gorm:"column:traffic_reset_at"`
}

// NodeResetCandidates 读全部设了重置日的未删除节点（按 id 升序；节点表是后台手工维护的小表，不分页）。
func (r *Repo) NodeResetCandidates(ctx context.Context) ([]NodeResetCandidate, error) {
	var out []NodeResetCandidate
	err := r.d.Conn(ctx).Raw(`
		SELECT id, traffic_reset_day, traffic_reset_at
		  FROM nodes
		 WHERE deleted_at IS NULL AND traffic_reset_day IS NOT NULL
		 ORDER BY id`).Scan(&out).Error
	return out, err
}

// NodeResetDue 是一个到期要清零的节点：只有 traffic_reset_at 仍早于 Before 才清（并发的手动重置已清过就跳过）。
type NodeResetDue struct {
	ID     uuid.UUID `json:"id"`
	Before time.Time `json:"before"`
}

// ResetNodesUsageIfBefore 一条语句清零一批到期节点（未删除、traffic_reset_at < before）：locked 先按 id 升序
// FOR NO KEY UPDATE 锁行（锁序与入账累加一致），再 UPDATE；返回实际清零的 id 与其中清零前超限的 id（都升序）。
func (r *Repo) ResetNodesUsageIfBefore(ctx context.Context, due []NodeResetDue) (reset, unblocked []uuid.UUID, err error) {
	if len(due) == 0 {
		return nil, nil, nil
	}
	ids := make([]uuid.UUID, 0, len(due))
	for _, d := range due {
		ids = append(ids, d.ID)
	}
	payload, err := jsonArray(due)
	if err != nil {
		return nil, nil, err
	}
	var rows []struct {
		ID       uuid.UUID `gorm:"column:id"`
		Exceeded bool      `gorm:"column:exceeded"`
	}
	err = r.d.Conn(ctx).Raw(`
		WITH locked AS (
		  SELECT id FROM nodes WHERE id IN ? ORDER BY id FOR NO KEY UPDATE
		), upd AS (
		  UPDATE nodes n
		     SET traffic_used_bytes = 0, traffic_exceeded_at = NULL, traffic_reset_at = now()
		    FROM jsonb_to_recordset(CAST(? AS jsonb)) AS d(id uuid, before timestamptz)
		   WHERE n.id = d.id AND n.id IN (SELECT id FROM locked)
		     AND n.deleted_at IS NULL AND n.traffic_reset_at < d.before
		  RETURNING n.id, (old.traffic_exceeded_at IS NOT NULL) AS exceeded
		)
		SELECT id, exceeded FROM upd ORDER BY id`, ids, payload).Scan(&rows).Error
	if err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		reset = append(reset, row.ID)
		if row.Exceeded {
			unblocked = append(unblocked, row.ID)
		}
	}
	return reset, unblocked, nil
}
