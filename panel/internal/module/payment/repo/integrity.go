package repo

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// SettledPaymentsSince 取 paid_at 不早于 since、没有挂账案（late_payment_cases.payment_id 唯一）的收款与它的订单：
// 这些收款入账时 Settler 已结算订单，订单此刻应是 paid / fulfilled。
// paid_at 没有单列索引：PG 18 可在 payments_provider_paid_idx (provider_id, paid_at DESC) 上做 skip scan（渠道只有几个），
// 否则顺序扫 payments；挂账案按 payment_id 唯一索引反连接。窗口有界（report 传近 7 天）。
func (r *Repo) SettledPaymentsSince(ctx context.Context, since time.Time) ([]integrity.OrderRef, error) {
	var out []integrity.OrderRef
	err := r.d.Conn(ctx).Raw(`
		SELECT p.id, p.order_id
		  FROM payments p
		 WHERE p.paid_at >= ?
		   AND NOT EXISTS (SELECT 1 FROM late_payment_cases c WHERE c.payment_id = p.id)`, since).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
