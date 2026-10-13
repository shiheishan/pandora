package repo

import (
	"context"

	"github.com/google/uuid"
)

// planNodePoolRow 是 plan_node_pools 的一行（只用于写入）。
type planNodePoolRow struct {
	PlanVersionID uuid.UUID `gorm:"column:plan_version_id;type:uuid"`
	PoolID        uuid.UUID `gorm:"column:pool_id;type:uuid"`
}

func (planNodePoolRow) TableName() string { return "plan_node_pools" }

// PoolPlanRow 是「池被哪个版本、哪个套餐引用」的一行。
type PoolPlanRow struct {
	PoolID        uuid.UUID
	PlanVersionID uuid.UUID
	PlanName      string
	OnSale        bool
}

// versionPoolScanRow 是 VersionPools 查询的一行。
type versionPoolScanRow struct {
	PlanVersionID uuid.UUID `gorm:"column:plan_version_id"`
	PoolID        uuid.UUID `gorm:"column:pool_id"`
}

// VersionPools 批量取版本绑定的池：版本 id → 池 id（升序）。
func (r *Repo) VersionPools(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	if len(versionIDs) == 0 {
		return map[uuid.UUID][]uuid.UUID{}, nil
	}
	var rows []versionPoolScanRow
	err := r.d.Conn(ctx).Raw(
		"SELECT plan_version_id, pool_id FROM plan_node_pools WHERE plan_version_id IN ? ORDER BY plan_version_id, pool_id",
		versionIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[uuid.UUID][]uuid.UUID)
	for _, row := range rows {
		out[row.PlanVersionID] = append(out[row.PlanVersionID], row.PoolID)
	}
	return out, nil
}

// PoolPlanRows 批量取池被引用的情况（一条 SQL），按 pool_id、plan_name 排序。
func (r *Repo) PoolPlanRows(ctx context.Context, poolIDs []uuid.UUID) ([]PoolPlanRow, error) {
	if len(poolIDs) == 0 {
		return []PoolPlanRow{}, nil
	}
	var rows []PoolPlanRow
	err := r.d.Conn(ctx).Raw(`SELECT pnp.pool_id, pnp.plan_version_id, pl.name AS plan_name,
		(pv.status = 'published' AND pl.status = 'active' AND pl.current_version_id = pv.id) AS on_sale
		FROM plan_node_pools pnp
		JOIN plan_versions pv ON pv.id = pnp.plan_version_id
		JOIN plans pl ON pl.id = pv.plan_id
		WHERE pnp.pool_id IN ?
		ORDER BY pnp.pool_id, pl.name, pnp.plan_version_id`, poolIDs).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []PoolPlanRow{}, nil
	}
	return rows, nil
}

// ReplaceVersionPools 整体替换版本的绑池：先 DELETE 该版本全部行，poolIDs 非空再用 Create 批量插入。
func (r *Repo) ReplaceVersionPools(ctx context.Context, versionID uuid.UUID, poolIDs []uuid.UUID) error {
	tx := r.d.Conn(ctx)
	if err := tx.Exec("DELETE FROM plan_node_pools WHERE plan_version_id = ?", versionID).Error; err != nil {
		return mapErr(err)
	}
	if len(poolIDs) == 0 {
		return nil
	}
	rows := make([]planNodePoolRow, len(poolIDs))
	for i, pid := range poolIDs {
		rows[i] = planNodePoolRow{PlanVersionID: versionID, PoolID: pid}
	}
	if err := tx.Create(&rows).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// BumpDraftVersionRow 把草稿版本的 row_version 加一。
func (r *Repo) BumpDraftVersionRow(ctx context.Context, versionID uuid.UUID, expected int64) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE plan_versions SET row_version = row_version + 1
		WHERE id = ? AND row_version = ? AND status = 'draft'`, versionID, expected)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}
