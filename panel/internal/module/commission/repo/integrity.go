package repo

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// EntriesSince 取 created_at 不早于 since、没作废的佣金条目与它的订单（作废已冲回计提，这条佣金不再算钱；
// 运维手册里这项的修法就是转待复核后作废）。
// commission_entries 没有单列的 created_at 索引，顺序扫；条目只在有邀请人的付费单上产生（每单至多三级），表远小于订单表。
func (r *Repo) EntriesSince(ctx context.Context, since time.Time) ([]integrity.OrderRef, error) {
	var out []integrity.OrderRef
	err := r.d.Conn(ctx).Raw(`
		SELECT id, order_id FROM commission_entries WHERE created_at >= ? AND status <> 'voided'`, since).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
