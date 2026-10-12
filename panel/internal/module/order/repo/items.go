package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/order/model"
)

// CreateItem 插入订单行（金额先过 checkMoney；空 jsonb 快照填 "[]"）。
func (r *Repo) CreateItem(ctx context.Context, it *model.Item) error {
	if err := checkMoney(it.UnitAmount, it.LineAmount); err != nil {
		return err
	}
	if it.SnapshotEntitlements == "" {
		it.SnapshotEntitlements = "[]"
	}
	if it.SnapshotQuotas == "" {
		it.SnapshotQuotas = "[]"
	}
	return mapErr(r.d.Conn(ctx).Create(it).Error)
}

// ItemsByOrders 批量读订单行，按订单、创建时间、id 排序。
func (r *Repo) ItemsByOrders(ctx context.Context, orderIDs []uuid.UUID) ([]model.Item, error) {
	if len(orderIDs) == 0 {
		return []model.Item{}, nil
	}
	var out []model.Item
	err := r.d.Conn(ctx).Where("order_id IN ?", orderIDs).
		Order("order_id, created_at, id").Find(&out).Error
	return out, mapErr(err)
}
