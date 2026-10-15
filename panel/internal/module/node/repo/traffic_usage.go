package repo

import (
	"context"

	"github.com/google/uuid"
)

// NodeUsageAdd 是一个节点本次要累加的实际字节。
type NodeUsageAdd struct {
	NodeID uuid.UUID `json:"node_id"`
	Bytes  int64     `json:"bytes"`
}

// AddNodeUsage 在调用方事务里把实际字节累加到 nodes.traffic_used_bytes（饱和到 bigint 上限），同一条 UPDATE 里按上限维护
// traffic_exceeded_at（有上限且累加后 >= 上限：保留原超限时刻或写 now()；否则 NULL，与 nodes_traffic_exceeded_shape 一致）。
// 返回这次由未超限变成超限的节点 id（升序）。一条语句、一次往返：locked 先按 id 升序 FOR NO KEY UPDATE 锁行（锁序固定；
// 与后台改节点的 FOR UPDATE 互斥，改上限与累加不会互相覆盖），upd 的 RETURNING 用 PG18 的 old/new 比较前后超限标记。
// 不存在的节点 id 自然没有行。adds 为空直接返回。
func (r *Repo) AddNodeUsage(ctx context.Context, adds []NodeUsageAdd) ([]uuid.UUID, error) {
	if len(adds) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(adds))
	for _, a := range adds {
		ids = append(ids, a.NodeID)
	}
	payload, err := jsonArray(adds)
	if err != nil {
		return nil, err
	}
	sum := satSumSQL("n.traffic_used_bytes", "a.bytes")
	var out []uuid.UUID
	err = r.d.Conn(ctx).Raw(`
		WITH locked AS (
		  SELECT id FROM nodes WHERE id IN ? ORDER BY id FOR NO KEY UPDATE
		), upd AS (
		  UPDATE nodes n
		     SET traffic_used_bytes = `+sum+`,
		         traffic_exceeded_at = CASE
		           WHEN n.traffic_limit_bytes IS NOT NULL AND (`+sum+`) >= n.traffic_limit_bytes
		           THEN coalesce(n.traffic_exceeded_at, now())
		           ELSE NULL
		         END
		    FROM jsonb_to_recordset(CAST(? AS jsonb)) AS a(node_id uuid, bytes bigint)
		   WHERE n.id = a.node_id AND n.id IN (SELECT id FROM locked)
		  RETURNING n.id, (old.traffic_exceeded_at IS NULL AND new.traffic_exceeded_at IS NOT NULL) AS newly
		)
		SELECT id FROM upd WHERE newly ORDER BY id`, ids, payload).
		Scan(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}
