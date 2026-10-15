package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/order/model"
)

// PendingResetOrderForSubscription 取这份订阅最近一张待付的流量重置单；没有回 false。
func (r *Repo) PendingResetOrderForSubscription(ctx context.Context, subscriptionID uuid.UUID) (model.Order, bool, error) {
	var o model.Order
	err := r.d.Conn(ctx).Raw(`
SELECT * FROM orders
 WHERE kind = 'traffic_reset' AND status = 'pending_payment' AND subscription_id = ?
 ORDER BY created_at DESC
 LIMIT 1`, subscriptionID).Scan(&o).Error
	if err != nil {
		return model.Order{}, false, mapErr(err)
	}
	if o.ID == uuid.Nil {
		return model.Order{}, false, nil
	}
	return o, true, nil
}
