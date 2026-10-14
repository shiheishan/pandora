package repo

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/order/model"
)

// CreateReservation 插入订单预留行。
func (r *Repo) CreateReservation(ctx context.Context, res *model.Reservation) error {
	return mapErr(r.d.Conn(ctx).Create(res).Error)
}

// ReservationByOrder 按订单 id 读预留（可加行锁）；不存在返回 false。
func (r *Repo) ReservationByOrder(ctx context.Context, orderID uuid.UUID, lock Lock) (model.Reservation, bool, error) {
	var res model.Reservation
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM order_reservations WHERE order_id = ?"+lock.suffix(),
		orderID,
	).Scan(&res).Error
	if err != nil {
		return model.Reservation{}, false, mapErr(err)
	}
	if res.ID == uuid.Nil {
		return model.Reservation{}, false, nil
	}
	return res, true, nil
}

// UpdateReservation 带旧状态条件更新预留状态；to 为 captured 或 released 时写入对应时间列；返回是否恰好改到一行。
func (r *Repo) UpdateReservation(ctx context.Context, id uuid.UUID, from, to string, at time.Time, reason *string) (bool, error) {
	cols := map[string]any{
		"state":      to,
		"updated_at": now(),
	}
	switch to {
	case "captured":
		cols["captured_at"] = at
	case "released":
		cols["released_at"] = at
		cols["release_reason"] = reason
	default:
		return false, errors.New("order: 预留目标状态只能是 captured 或 released")
	}
	res := r.d.Conn(ctx).Model(&model.Reservation{}).
		Where("id = ? AND state = ?", id, from).Updates(cols)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// InsertReservationEvent 追加一条预留流水。
func (r *Repo) InsertReservationEvent(ctx context.Context, e *model.ReservationEvent) error {
	return mapErr(r.d.Conn(ctx).Create(e).Error)
}

// CreateStockReservation 插入库存预留明细（只追加）。
func (r *Repo) CreateStockReservation(ctx context.Context, sr *model.StockReservation) error {
	return mapErr(r.d.Conn(ctx).Create(sr).Error)
}

// StockReservations 读某预留下的库存明细，按 plan_id 排序。
func (r *Repo) StockReservations(ctx context.Context, reservationID uuid.UUID) ([]model.StockReservation, error) {
	var out []model.StockReservation
	err := r.d.Conn(ctx).Where("reservation_id = ?", reservationID).
		Order("plan_id").Find(&out).Error
	return out, mapErr(err)
}

// CreateLimitReservation 插入限购预留明细（只追加）。
func (r *Repo) CreateLimitReservation(ctx context.Context, lr *model.LimitReservation) error {
	return mapErr(r.d.Conn(ctx).Create(lr).Error)
}

// LimitReservations 读某预留下的限购明细，按 plan_id、user_id 排序。
func (r *Repo) LimitReservations(ctx context.Context, reservationID uuid.UUID) ([]model.LimitReservation, error) {
	var out []model.LimitReservation
	err := r.d.Conn(ctx).Where("reservation_id = ?", reservationID).
		Order("plan_id, user_id").Find(&out).Error
	return out, mapErr(err)
}

// EnsureCounter 确保限购计数行存在（已存在则不动）。
func (r *Repo) EnsureCounter(ctx context.Context, planID, userID uuid.UUID) error {
	err := r.d.Conn(ctx).Exec(
		`INSERT INTO plan_purchase_counters (plan_id, user_id) VALUES (?, ?) ON CONFLICT (plan_id, user_id) DO NOTHING`,
		planID, userID,
	).Error
	return mapErr(err)
}

// CounterForUpdate 按套餐与用户读限购计数并加 FOR UPDATE；不存在返回 false。
func (r *Repo) CounterForUpdate(ctx context.Context, planID, userID uuid.UUID) (model.PurchaseCounter, bool, error) {
	var c model.PurchaseCounter
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM plan_purchase_counters WHERE plan_id = ? AND user_id = ? FOR UPDATE",
		planID, userID,
	).Scan(&c).Error
	if err != nil {
		return model.PurchaseCounter{}, false, mapErr(err)
	}
	if c.PlanID == uuid.Nil {
		return model.PurchaseCounter{}, false, nil
	}
	return c, true, nil
}

// AdjustCounter 在计数非负前提下增减 purchased、reserved；返回是否恰好改到一行。
func (r *Repo) AdjustCounter(ctx context.Context, planID, userID uuid.UUID, dPurchased, dReserved int) (bool, error) {
	ts := now()
	res := r.d.Conn(ctx).Exec(`
UPDATE plan_purchase_counters
   SET purchased = purchased + ?, reserved = reserved + ?, updated_at = ?
 WHERE plan_id = ? AND user_id = ?
   AND purchased + ? >= 0 AND reserved + ? >= 0`,
		dPurchased, dReserved, ts, planID, userID, dPurchased, dReserved,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}
