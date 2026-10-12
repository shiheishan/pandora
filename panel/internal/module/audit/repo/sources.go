package repo

import (
	"context"

	"github.com/google/uuid"
)

// SharesIPHash 判断两个操作者在审计里是否出现过相同的来源 IP 哈希。
// 两边各自按 audit_events_actor_idx（actor_id）取出去重后的 ip_hash 再求交集；
// 成本与两人各自的审计条数成正比，不依赖 ip_hash 上的索引（表上没有）。
func (r *Repo) SharesIPHash(ctx context.Context, a, b uuid.UUID) (bool, error) {
	var ok bool
	err := r.d.Conn(ctx).Raw(`
SELECT EXISTS (
  SELECT 1
    FROM (SELECT DISTINCT ip_hash FROM audit_events WHERE actor_id = ? AND ip_hash IS NOT NULL) x
    JOIN (SELECT DISTINCT ip_hash FROM audit_events WHERE actor_id = ? AND ip_hash IS NOT NULL) y
      ON x.ip_hash = y.ip_hash)`, a, b).Scan(&ok).Error
	return ok, err
}
