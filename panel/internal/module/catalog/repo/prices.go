package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// InsertPrice 插入一档价格（用 Create，回填 ID、row_version、created_at）；写前 money.Check(p.UnitAmount)。
func (r *Repo) InsertPrice(ctx context.Context, p *model.Price) error {
	if err := money.Check(p.UnitAmount); err != nil {
		return err
	}
	return mapErr(r.d.Conn(ctx).Create(p).Error)
}

// PriceByID 按 id 读价格，lock 决定行锁。
func (r *Repo) PriceByID(ctx context.Context, id uuid.UUID, lock Lock) (model.Price, bool, error) {
	var p model.Price
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM prices WHERE id = ?"+lock.suffix(), id,
	).Scan(&p).Error
	if err != nil {
		return model.Price{}, false, err
	}
	if p.ID == uuid.Nil {
		return model.Price{}, false, nil
	}
	return p, true, nil
}

// PricesByIDs 批量读价格（一条 SQL）。
func (r *Repo) PricesByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Price, error) {
	if len(ids) == 0 {
		return []model.Price{}, nil
	}
	var rows []model.Price
	err := r.d.Conn(ctx).Where("id IN ?", ids).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Price{}, nil
	}
	return rows, nil
}

// PricesByPlans 读这些套餐的全部价格（含已归档），按 plan_id, created_at DESC, id 排序。
func (r *Repo) PricesByPlans(ctx context.Context, planIDs []uuid.UUID) ([]model.Price, error) {
	if len(planIDs) == 0 {
		return []model.Price{}, nil
	}
	var rows []model.Price
	err := r.d.Conn(ctx).
		Where("plan_id IN ?", planIDs).
		Order("plan_id, created_at DESC, id").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Price{}, nil
	}
	return rows, nil
}

// ActivePrices 读这些套餐的在售价格（status='active'），按 plan_id, unit_amount, interval_count, id 升序；lock 为 ForShare/ForUpdate 时锁住这些行。
func (r *Repo) ActivePrices(ctx context.Context, planIDs []uuid.UUID, lock Lock) ([]model.Price, error) {
	if len(planIDs) == 0 {
		return []model.Price{}, nil
	}
	var rows []model.Price
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM prices WHERE plan_id IN ? AND status = 'active' ORDER BY plan_id, unit_amount, interval_count, id"+lock.suffix(),
		planIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Price{}, nil
	}
	return rows, nil
}

// LockActivePublicPrices 锁住套餐在这些币种下的在售公开价（status='active' AND user_group_id IS NULL AND currency IN ?），按 id 升序 FOR UPDATE。
func (r *Repo) LockActivePublicPrices(ctx context.Context, planID uuid.UUID, currencies []string) ([]model.Price, error) {
	if len(currencies) == 0 {
		return []model.Price{}, nil
	}
	var rows []model.Price
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM prices WHERE plan_id = ? AND status = 'active' AND user_group_id IS NULL AND currency IN ? ORDER BY id FOR UPDATE",
		planID, currencies,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Price{}, nil
	}
	return rows, nil
}

// ArchivePrice 归档：status='archived', row_version = row_version + 1；条件 id = ? AND row_version = expected AND status = 'active'。
func (r *Repo) ArchivePrice(ctx context.Context, id uuid.UUID, expected int64) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE prices SET status = 'archived', row_version = row_version + 1 WHERE id = ? AND row_version = ? AND status = 'active'",
		id, expected,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}
