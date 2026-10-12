package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/ledger/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// InsertHold 插入一条冻结记录（回填 ID）。同一订单已有冻结时返回 ErrDuplicate。写前 money.Check(h.Amount)。
func (r *Repo) InsertHold(ctx context.Context, h *model.Hold) error {
	if err := money.Check(h.Amount); err != nil {
		return err
	}
	return mapErr(r.d.Conn(ctx).Create(h).Error)
}

// LockHoldByOrder 按订单取冻结记录并加行锁（FOR UPDATE）；没有时 found 为 false。
func (r *Repo) LockHoldByOrder(ctx context.Context, orderID uuid.UUID) (h model.Hold, found bool, err error) {
	var rows []model.Hold
	if err := r.d.Conn(ctx).Raw(
		"SELECT * FROM balance_holds WHERE order_id = ? FOR UPDATE", orderID,
	).Scan(&rows).Error; err != nil {
		return model.Hold{}, false, err
	}
	if len(rows) == 0 {
		return model.Hold{}, false, nil
	}
	return rows[0], true, nil
}

// HoldByOrder 按订单取冻结记录（不加锁）；没有时 found 为 false。
func (r *Repo) HoldByOrder(ctx context.Context, orderID uuid.UUID) (h model.Hold, found bool, err error) {
	var rows []model.Hold
	if err := r.d.Conn(ctx).Raw(
		"SELECT * FROM balance_holds WHERE order_id = ?", orderID,
	).Scan(&rows).Error; err != nil {
		return model.Hold{}, false, err
	}
	if len(rows) == 0 {
		return model.Hold{}, false, nil
	}
	return rows[0], true, nil
}

// MarkHoldCaptured 把 held 的冻结改成 captured 并记扣款交易；返回是否改到一行（不是 held 时为 false）。
func (r *Repo) MarkHoldCaptured(ctx context.Context, id, txnID uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE balance_holds SET status = 'captured', capture_txn_id = ?, captured_at = now(), updated_at = now() WHERE id = ? AND status = 'held'",
		txnID, id,
	)
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// MarkHoldReleased 把 held 的冻结改成 released 并记释放交易；返回是否改到一行。
func (r *Repo) MarkHoldReleased(ctx context.Context, id, txnID uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE balance_holds SET status = 'released', release_txn_id = ?, released_at = now(), updated_at = now() WHERE id = ? AND status = 'held'",
		txnID, id,
	)
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}
