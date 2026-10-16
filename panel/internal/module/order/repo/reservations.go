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
