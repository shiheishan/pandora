package repo

import (
	"context"

	"github.com/google/uuid"
)

// heldCountRow 是 HeldCountsByUser 查询的一行聚合结果。
type heldCountRow struct {
	UserID uuid.UUID
	N      int
}

// HeldCountsByUser 数每个用户手里还算「一份」的订阅：生效中的加能续回的，即续费窗口没关（renewal_closed_at 为空）的全部订阅。
// 口径同 service.StateAt：StateEnded 只看 renewal_closed_at 非空，其余都是 StateLive 或 StateRevivable
// （过期满 30 天但过期扫描还没关窗的，续费、换套餐、叠加照样放行，所以也算）。与时刻无关，不带 now。
// 一条 SQL，走 subscriptions_user_idx；一份都没有的用户不在结果里。
func (r *Repo) HeldCountsByUser(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	if len(userIDs) == 0 {
		return out, nil
	}
	var rows []heldCountRow
	err := r.d.Conn(ctx).Raw(
		`SELECT user_id, count(*) AS n FROM subscriptions
		  WHERE user_id IN ? AND renewal_closed_at IS NULL
		  GROUP BY user_id`,
		userIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	for _, row := range rows {
		out[row.UserID] = row.N
	}
	return out, nil
}
