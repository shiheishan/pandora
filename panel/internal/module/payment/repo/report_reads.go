package repo

import (
	"context"
	"time"
)

// StuckCallbackCount 数收到早于 receivedBefore、仍未处理（processing_status = pending）的支付回调。
func (r *Repo) StuckCallbackCount(ctx context.Context, receivedBefore time.Time) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw(
		`SELECT count(*) FROM payment_events WHERE processing_status = 'pending' AND received_at < ?`,
		receivedBefore,
	).Scan(&n).Error
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}
