package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
)

// PlanByID 按 id 读套餐，lock 决定是否加行锁。
func (r *Repo) PlanByID(ctx context.Context, id uuid.UUID, lock Lock) (model.Plan, bool, error) {
	var p model.Plan
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM plans WHERE id = ?"+lock.suffix(), id,
	).Scan(&p).Error
	if err != nil {
		return model.Plan{}, false, err
	}
	if p.ID == uuid.Nil {
		return model.Plan{}, false, nil
	}
	return p, true, nil
}

// ListPlans 列出全部套餐（含已归档），按 sort_order, created_at, id 升序。
func (r *Repo) ListPlans(ctx context.Context) ([]model.Plan, error) {
	var plans []model.Plan
	err := r.d.Conn(ctx).
		Order("sort_order ASC, created_at ASC, id ASC").
		Find(&plans).Error
	if err != nil {
		return nil, err
	}
	if plans == nil {
		return []model.Plan{}, nil
	}
	return plans, nil
}

// SellablePlans 列出 status='active' 且 current_version_id 非空的套餐，按 sort_order, created_at, id 升序（门户目录与下单目标用）。
func (r *Repo) SellablePlans(ctx context.Context) ([]model.Plan, error) {
	var plans []model.Plan
	err := r.d.Conn(ctx).
		Where("status = ? AND current_version_id IS NOT NULL", "active").
		Order("sort_order ASC, created_at ASC, id ASC").
		Find(&plans).Error
	if err != nil {
		return nil, err
	}
	if plans == nil {
		return []model.Plan{}, nil
	}
	return plans, nil
}

// PlansByIDs 批量读套餐（一条 SQL）。
func (r *Repo) PlansByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Plan, error) {
	if len(ids) == 0 {
		return []model.Plan{}, nil
	}
	var plans []model.Plan
	err := r.d.Conn(ctx).Where("id IN ?", ids).Find(&plans).Error
	if err != nil {
		return nil, err
	}
	if plans == nil {
		return []model.Plan{}, nil
	}
	return plans, nil
}

// InsertPlan 插入套餐，回填 ID 等列（用 Create）。
func (r *Repo) InsertPlan(ctx context.Context, p *model.Plan) error {
	return mapErr(r.d.Conn(ctx).Create(p).Error)
}

// UpdatePlanProfile 整体改写套餐资料并把 row_version 加一；只改 status 为 draft 或 active 且 row_version = expected 的行。
func (r *Repo) UpdatePlanProfile(ctx context.Context, id uuid.UUID, expected int64, p *model.Plan) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE plans SET
		code = ?, name = ?, description = ?, visibility = ?, visible_from = ?, visible_until = ?,
		allow_new_purchase = ?, allow_renewal = ?, allow_upgrade = ?,
		purchase_limit_per_user = ?, stock_total = ?, sort_order = ?,
		highlights = ?::jsonb, recommended = ?,
		row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND row_version = ? AND status IN ('draft', 'active')`,
		p.Code, p.Name, p.Description, p.Visibility, p.VisibleFrom, p.VisibleUntil,
		p.AllowNewPurchase, p.AllowRenewal, p.AllowUpgrade,
		p.PurchaseLimitPerUser, p.StockTotal, p.SortOrder,
		p.Highlights, p.Recommended,
		id, expected,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ArchivePlan 归档：status='archived', allow_new_purchase=false, row_version+1, updated_at=now()；
// 条件 id = ? AND row_version = expected AND status IN ('draft','active')。
func (r *Repo) ArchivePlan(ctx context.Context, id uuid.UUID, expected int64) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE plans SET
		status = 'archived', allow_new_purchase = false,
		row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND row_version = ? AND status IN ('draft', 'active')`,
		id, expected,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ActivateVersion 发布后切换当前版本：current_version_id = versionID, status='active', row_version+1, updated_at=now()；
// 条件 id = ? AND row_version = expected AND status IN ('draft','active')。
func (r *Repo) ActivateVersion(ctx context.Context, planID, versionID uuid.UUID, expected int64) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE plans SET
		current_version_id = ?, status = 'active',
		row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND row_version = ? AND status IN ('draft', 'active')`,
		versionID, planID, expected,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// SetVisibleGroups 把套餐可见的用户组整体替换成 groupIDs：先 DELETE FROM plan_visible_groups WHERE plan_id = ?，
// groupIDs 非空时再用 Create 批量插入 model.PlanVisibleGroup。外键冲突（组不存在）经 mapErr 返回 ErrForeignKey。
func (r *Repo) SetVisibleGroups(ctx context.Context, planID uuid.UUID, groupIDs []uuid.UUID) error {
	tx := r.d.Conn(ctx)
	if err := tx.Exec("DELETE FROM plan_visible_groups WHERE plan_id = ?", planID).Error; err != nil {
		return mapErr(err)
	}
	if len(groupIDs) == 0 {
		return nil
	}
	rows := make([]model.PlanVisibleGroup, len(groupIDs))
	for i, gid := range groupIDs {
		rows[i] = model.PlanVisibleGroup{PlanID: planID, UserGroupID: gid}
	}
	return mapErr(tx.Create(&rows).Error)
}

// visibleGroupRow 是 VisibleGroups 查询的一行。
type visibleGroupRow struct {
	PlanID      uuid.UUID `gorm:"column:plan_id"`
	UserGroupID uuid.UUID `gorm:"column:user_group_id"`
}

// VisibleGroups 批量取套餐可见的用户组：plan_id → 组 id 列表（按 user_group_id 升序）。没有组的套餐不在 map 里。
func (r *Repo) VisibleGroups(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	if len(planIDs) == 0 {
		return map[uuid.UUID][]uuid.UUID{}, nil
	}
	var rows []visibleGroupRow
	err := r.d.Conn(ctx).Raw(
		"SELECT plan_id, user_group_id FROM plan_visible_groups WHERE plan_id IN ? ORDER BY plan_id, user_group_id ASC",
		planIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]uuid.UUID)
	for _, row := range rows {
		out[row.PlanID] = append(out[row.PlanID], row.UserGroupID)
	}
	return out, nil
}

// ReserveStock 预留 qty 份库存：一条 UPDATE，返回是否改了一行（false 表示不限量、售罄或不存在，由 StockLimited 区分）。
func (r *Repo) ReserveStock(ctx context.Context, planID uuid.UUID, qty int) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE plans SET stock_reserved = stock_reserved + ?, updated_at = now()
		WHERE id = ? AND stock_total IS NOT NULL AND stock_sold + stock_reserved + ? <= stock_total`,
		qty, planID, qty,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// StockLimited 读套餐是否限量：found=false 表示套餐不存在；limited = stock_total IS NOT NULL。
func (r *Repo) StockLimited(ctx context.Context, planID uuid.UUID) (limited bool, found bool, err error) {
	var row struct {
		ID         uuid.UUID `gorm:"column:id"`
		StockTotal *int      `gorm:"column:stock_total"`
	}
	if err = r.d.Conn(ctx).Raw(
		"SELECT id, stock_total FROM plans WHERE id = ?", planID,
	).Scan(&row).Error; err != nil {
		return false, false, err
	}
	if row.ID == uuid.Nil {
		return false, false, nil
	}
	return row.StockTotal != nil, true, nil
}

// ReleaseStock 释放预留：UPDATE plans SET stock_reserved = stock_reserved - ?, updated_at = now() WHERE id = ? AND stock_reserved >= ?。
func (r *Repo) ReleaseStock(ctx context.Context, planID uuid.UUID, qty int) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE plans SET stock_reserved = stock_reserved - ?, updated_at = now()
		WHERE id = ? AND stock_reserved >= ?`,
		qty, planID, qty,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// CaptureStock 预留转已售：SET stock_reserved = stock_reserved - ?, stock_sold = stock_sold + ?, updated_at = now() WHERE id = ? AND stock_reserved >= ?。
func (r *Repo) CaptureStock(ctx context.Context, planID uuid.UUID, qty int) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE plans SET
		stock_reserved = stock_reserved - ?, stock_sold = stock_sold + ?, updated_at = now()
		WHERE id = ? AND stock_reserved >= ?`,
		qty, qty, planID, qty,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}
