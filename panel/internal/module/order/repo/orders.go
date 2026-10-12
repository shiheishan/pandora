package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/order/model"
)

// CreateOrder 插入订单（八个金额字段先过 checkMoney），并回填 id、时间戳。
func (r *Repo) CreateOrder(ctx context.Context, o *model.Order) error {
	if err := checkMoney(
		o.SubtotalAmount, o.DiscountAmount, o.ProrationCreditAmount, o.WaivedAmount,
		o.TotalAmount, o.BalanceApplied, o.PayableAmount, o.PaidAmount,
	); err != nil {
		return err
	}
	if err := r.d.Conn(ctx).Create(o).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// OrderByID 按 id 读订单（可加行锁）；不存在返回 false。
func (r *Repo) OrderByID(ctx context.Context, id uuid.UUID, lock Lock) (model.Order, bool, error) {
	var o model.Order
	err := r.d.Conn(ctx).Raw("SELECT * FROM orders WHERE id = ?"+lock.suffix(), id).Scan(&o).Error
	if err != nil {
		return model.Order{}, false, mapErr(err)
	}
	if o.ID == uuid.Nil {
		return model.Order{}, false, nil
	}
	return o, true, nil
}

// OrderForUser 按 id 与用户读订单（可加行锁）；不存在或不属于该用户返回 false。
func (r *Repo) OrderForUser(ctx context.Context, id, userID uuid.UUID, lock Lock) (model.Order, bool, error) {
	var o model.Order
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM orders WHERE id = ? AND user_id = ?"+lock.suffix(),
		id, userID,
	).Scan(&o).Error
	if err != nil {
		return model.Order{}, false, mapErr(err)
	}
	if o.ID == uuid.Nil {
		return model.Order{}, false, nil
	}
	return o, true, nil
}

// OrdersByIDs 批量读订单（不加锁、不保证顺序）。
func (r *Repo) OrdersByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Order, error) {
	if len(ids) == 0 {
		return []model.Order{}, nil
	}
	var out []model.Order
	err := r.d.Conn(ctx).Where("id IN ?", ids).Find(&out).Error
	return out, mapErr(err)
}

// UpdateOrder 带旧状态条件更新订单若干列（paid_amount 须为 decimal.Decimal 且过 money.Check）；
// 自动写入 updated_at 与 state_version+1；返回是否恰好改到一行。
func (r *Repo) UpdateOrder(ctx context.Context, id uuid.UUID, fromStatus string, cols map[string]any) (bool, error) {
	if v, ok := cols["paid_amount"]; ok {
		d, isDec := v.(decimal.Decimal)
		if !isDec {
			return false, fmt.Errorf("order: paid_amount 必须是 decimal.Decimal，得到 %T", v)
		}
		if err := checkMoney(d); err != nil {
			return false, err
		}
	}
	cols["updated_at"] = now()
	cols["state_version"] = gorm.Expr("state_version + 1")
	res := r.d.Conn(ctx).Model(&model.Order{}).
		Where("id = ? AND status = ?", id, fromStatus).Updates(cols)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// PendingNewOrderForPlan 找用户在某套餐下最近一笔待支付的新购单。
func (r *Repo) PendingNewOrderForPlan(ctx context.Context, userID, planID uuid.UUID) (model.Order, bool, error) {
	var o model.Order
	err := r.d.Conn(ctx).Raw(`
SELECT * FROM orders
 WHERE kind = 'new' AND status = 'pending_payment' AND user_id = ?
   AND EXISTS (SELECT 1 FROM order_items oi WHERE oi.order_id = orders.id AND oi.plan_id = ?)
 ORDER BY created_at DESC
 LIMIT 1`, userID, planID).Scan(&o).Error
	if err != nil {
		return model.Order{}, false, mapErr(err)
	}
	if o.ID == uuid.Nil {
		return model.Order{}, false, nil
	}
	return o, true, nil
}

// OpenChangeOrderExists 该订阅是否已有未完结的续费或换套餐单。
func (r *Repo) OpenChangeOrderExists(ctx context.Context, subscriptionID uuid.UUID) (bool, error) {
	var n int64
	err := r.d.Conn(ctx).Raw(`
SELECT COUNT(*) FROM orders
 WHERE subscription_id = ?
   AND kind IN ('renewal', 'upgrade')
   AND status IN ('pending_payment', 'paid')`, subscriptionID).Scan(&n).Error
	if err != nil {
		return false, mapErr(err)
	}
	return n > 0, nil
}

// OpenChangeSubscriptions 是 subIDs 里挂着未完结续费或变更单的订阅（OpenChangeOrderExists 的批量版，报价与开单预览用）。
func (r *Repo) OpenChangeSubscriptions(ctx context.Context, subIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(subIDs))
	if len(subIDs) == 0 {
		return out, nil
	}
	rows, err := r.d.Conn(ctx).Raw(`
SELECT DISTINCT subscription_id FROM orders
 WHERE subscription_id IN ?
   AND kind IN ('renewal', 'upgrade')
   AND status IN ('pending_payment', 'paid')`, subIDs).Rows()
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, mapErr(err)
		}
		out[id] = true
	}
	return out, mapErr(rows.Err())
}

// HasPaidOrder 判断用户有没有付过钱的订单（已支付或已履约、应付大于 0；余额全额付的单应付为 0，不算）。
func (r *Repo) HasPaidOrder(ctx context.Context, userID uuid.UUID) (bool, error) {
	var ok bool
	err := r.d.Conn(ctx).Raw(`
SELECT EXISTS (SELECT 1 FROM orders
 WHERE user_id = ?
   AND status IN ('paid', 'fulfilled')
   AND payable_amount > 0)`, userID).Scan(&ok).Error
	if err != nil {
		return false, mapErr(err)
	}
	return ok, nil
}

// ExpiryCandidates 扫描已到期的 held 预留对应、仍待支付的订单 id（SKIP LOCKED）。
func (r *Repo) ExpiryCandidates(ctx context.Context, at time.Time, limit int) ([]uuid.UUID, error) {
	var rows []struct{ OrderID uuid.UUID }
	err := r.d.Conn(ctx).Raw(`
SELECT r.order_id FROM order_reservations r
 JOIN orders o ON o.id = r.order_id
 WHERE r.state = 'held' AND r.expires_at <= ? AND o.status = 'pending_payment'
 ORDER BY r.expires_at, r.id
 LIMIT ?
 FOR UPDATE OF o SKIP LOCKED`, at, limit).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.OrderID)
	}
	return ids, nil
}
