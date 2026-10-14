package repo

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// ListPacks 列出流量包；status 为空串时全部，否则只列该状态。排序：status = 'archived'（在售在前）, sort_order, created_at, id。
func (r *Repo) ListPacks(ctx context.Context, status string) ([]model.TrafficPack, error) {
	q := r.d.Conn(ctx).Model(&model.TrafficPack{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var rows []model.TrafficPack
	err := q.Order("status = 'archived', sort_order, created_at, id").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.TrafficPack{}, nil
	}
	return rows, nil
}

// PackByID 按 id 读流量包，lock 决定行锁。
func (r *Repo) PackByID(ctx context.Context, id uuid.UUID, lock Lock) (model.TrafficPack, bool, error) {
	var p model.TrafficPack
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM traffic_packs WHERE id = ?"+lock.suffix(), id,
	).Scan(&p).Error
	if err != nil {
		return model.TrafficPack{}, false, err
	}
	if p.ID == uuid.Nil {
		return model.TrafficPack{}, false, nil
	}
	return p, true, nil
}

// PacksByIDs 批量读流量包（一条 SQL）。
func (r *Repo) PacksByIDs(ctx context.Context, ids []uuid.UUID) ([]model.TrafficPack, error) {
	if len(ids) == 0 {
		return []model.TrafficPack{}, nil
	}
	var rows []model.TrafficPack
	err := r.d.Conn(ctx).Where("id IN ?", ids).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.TrafficPack{}, nil
	}
	return rows, nil
}

// InsertPack 插入流量包（用 Create，回填 ID）；写前 money.Check(p.UnitAmount)。
func (r *Repo) InsertPack(ctx context.Context, p *model.TrafficPack) error {
	if err := money.Check(p.UnitAmount); err != nil {
		return err
	}
	return mapErr(r.d.Conn(ctx).Create(p).Error)
}

// UpdatePack 改名称、容量、币种、金额、推荐、排序，并 updated_at = now()；条件 id = ? AND updated_at = expectedUpdatedAt。写前 money.Check。
func (r *Repo) UpdatePack(ctx context.Context, id uuid.UUID, expectedUpdatedAt time.Time, p *model.TrafficPack) (bool, error) {
	if err := money.Check(p.UnitAmount); err != nil {
		return false, err
	}
	res := r.d.Conn(ctx).Exec(
		`UPDATE traffic_packs SET name = ?, traffic_bytes = ?, currency = ?, unit_amount = ?, recommended = ?, sort_order = ?, updated_at = now()
		 WHERE id = ? AND updated_at = ?`,
		p.Name, p.TrafficBytes, p.Currency, p.UnitAmount, p.Recommended, p.SortOrder, id, expectedUpdatedAt,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// SetPackStatus 上下架：status = to, updated_at = now()；条件 id = ? AND updated_at = expectedUpdatedAt AND status = from。
func (r *Repo) SetPackStatus(ctx context.Context, id uuid.UUID, expectedUpdatedAt time.Time, from, to string) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE traffic_packs SET status = ?, updated_at = now() WHERE id = ? AND updated_at = ? AND status = ?",
		to, id, expectedUpdatedAt, from,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}
