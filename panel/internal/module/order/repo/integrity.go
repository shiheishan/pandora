package repo

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// PaidUnfulfilledRows 找停在 paid 的订单：paid 只在结算事务里短暂出现（同一事务随后转 fulfilled），提交后还看得到就是半截状态。
// 走 orders_status_idx（status 前缀）。样例是订单 id。
func (r *Repo) PaidUnfulfilledRows(ctx context.Context) ([]integrity.Row, error) {
	var out []integrity.Row
	err := r.d.Conn(ctx).Raw(`
		SELECT id::text AS id, count(*) OVER () AS n
		  FROM orders
		 WHERE status = 'paid'
		 ORDER BY id DESC
		 LIMIT ?`, integrity.SampleLimit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// ReservationMismatchRows 找预留与订单状态脱节的订单：held 的预留挂在不是待付的订单上（券与余额冻结一直占着），
// 或待付订单的预留不是 held（建单在同一事务里插 held 预留，待付期间只有结算、取消、超时会动它，它们都同事务改订单状态）。
// 前半走 order_reservations_expiry_idx（held 的部分索引），后半走 orders_status_idx（pending_payment），两边都只扫在途订单。
// 样例是订单 id。
func (r *Repo) ReservationMismatchRows(ctx context.Context) ([]integrity.Row, error) {
	var out []integrity.Row
	err := r.d.Conn(ctx).Raw(`
		SELECT x.id::text AS id, count(*) OVER () AS n
		  FROM (
		        SELECT r.order_id AS id
		          FROM order_reservations r
		          JOIN orders o ON o.id = r.order_id
		         WHERE r.state = 'held' AND o.status <> 'pending_payment'
		        UNION
		        SELECT o.id
		          FROM orders o
		          LEFT JOIN order_reservations r ON r.order_id = o.id
		         WHERE o.status = 'pending_payment' AND r.state IS DISTINCT FROM 'held') x
		 ORDER BY x.id DESC
		 LIMIT ?`, integrity.SampleLimit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// UnsettledAmong 返回 ids 里状态不是 paid / fulfilled 的订单 id（不存在的 id 不返回）。按主键查，一条 SQL；
// 数组以文本字面量传（GORM 会把切片展开成参数列表，几千个 id 会撞参数上限）。
func (r *Repo) UnsettledAmong(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return []uuid.UUID{}, nil
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String()
	}
	var out []uuid.UUID
	err := r.d.Conn(ctx).Raw(`
		SELECT id FROM orders
		 WHERE id = ANY (CAST(? AS uuid[])) AND status NOT IN ('paid', 'fulfilled')`,
		"{"+strings.Join(parts, ",")+"}").Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
