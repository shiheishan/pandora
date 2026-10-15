package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/identity/model"
)

// LockReferralGraph 在当前事务里拿改邀请关系的全局事务级咨询锁（防两个并发改动一起造出环）。
func (r *Repo) LockReferralGraph(ctx context.Context) error {
	return r.d.Conn(ctx).Exec("SELECT pg_advisory_xact_lock(hashtextextended('identity:referrals', 0))").Error
}

// ReferrerChainContains 从 start 开始沿生效邀请关系往上找（start 的邀请人、邀请人的邀请人……最多 maxDepth 层），碰到 target 返回 true。
func (r *Repo) ReferrerChainContains(ctx context.Context, start, target uuid.UUID, maxDepth int) (bool, error) {
	const sql = `WITH RECURSIVE up(uid, depth) AS (
  SELECT referrer_user_id, 1 FROM effective_referrals WHERE referee_user_id = ?
  UNION ALL
  SELECT e.referrer_user_id, up.depth + 1 FROM effective_referrals e JOIN up ON e.referee_user_id = up.uid WHERE up.depth < ?
)
SELECT EXISTS (SELECT 1 FROM up WHERE uid = ?)`

	var exists bool
	err := r.d.Conn(ctx).Raw(sql, start, maxDepth, target).Scan(&exists).Error
	return exists, err
}

// InsertReferralChange 追加一条改邀请人记录。
func (r *Repo) InsertReferralChange(ctx context.Context, c *model.ReferralChange) error {
	return mapErr(r.d.Conn(ctx).Create(c).Error)
}
