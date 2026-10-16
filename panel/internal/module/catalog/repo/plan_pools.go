package repo

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
)

// PoolPlanRow 是「池被哪个套餐引用」的一行。
type PoolPlanRow struct {
	PoolID   uuid.UUID `gorm:"column:pool_id"`
	PlanID   uuid.UUID `gorm:"column:plan_id"`
	PlanName string    `gorm:"column:plan_name"`
	OnSale   bool      `gorm:"column:on_sale"`
	Archived bool      `gorm:"column:archived"`
}

// planPoolScanRow 是 PlanPools 查询的一行。
type planPoolScanRow struct {
	PlanID uuid.UUID `gorm:"column:plan_id"`
	PoolID uuid.UUID `gorm:"column:pool_id"`
}

// PlanPools 批量取套餐绑定的池（一条 SQL）：套餐 id → 池 id（升序）。没绑的套餐不在 map 里。
func (r *Repo) PlanPools(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	if len(planIDs) == 0 {
		return map[uuid.UUID][]uuid.UUID{}, nil
	}
	var rows []planPoolScanRow
	err := r.d.Conn(ctx).Raw(
		"SELECT plan_id, pool_id FROM plan_node_pools WHERE plan_id IN ? ORDER BY plan_id, pool_id",
		planIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[uuid.UUID][]uuid.UUID)
	for _, row := range rows {
		out[row.PlanID] = append(out[row.PlanID], row.PoolID)
	}
	return out, nil
}

// PoolPlanRows 批量取池被套餐引用的情况（一条 SQL），按 pool_id、plan_name、plan_id 排序。
// 在售 = 套餐 active 且有当前版本。
func (r *Repo) PoolPlanRows(ctx context.Context, poolIDs []uuid.UUID) ([]PoolPlanRow, error) {
	if len(poolIDs) == 0 {
		return []PoolPlanRow{}, nil
	}
	var rows []PoolPlanRow
	err := r.d.Conn(ctx).Raw(`SELECT pnp.pool_id, pnp.plan_id, pl.name AS plan_name,
		(pl.status = 'active' AND pl.current_version_id IS NOT NULL) AS on_sale,
		(pl.status = 'archived') AS archived
		FROM plan_node_pools pnp
		JOIN plans pl ON pl.id = pnp.plan_id
		WHERE pnp.pool_id IN ?
		ORDER BY pnp.pool_id, pl.name, pnp.plan_id`, poolIDs).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []PoolPlanRow{}, nil
	}
	return rows, nil
}

// DeleteArchivedPoolBindings 删掉已归档套餐对这个池的绑定，返回被删绑定的套餐 id（升序）。在调用方事务里执行（删池时由 node 调）。
func (r *Repo) DeleteArchivedPoolBindings(ctx context.Context, poolID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.d.Conn(ctx).Raw(`DELETE FROM plan_node_pools pnp USING plans pl
 WHERE pnp.pool_id = ? AND pl.id = pnp.plan_id AND pl.status = 'archived'
RETURNING pnp.plan_id`, poolID).Scan(&ids).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if len(ids) == 0 {
		return []uuid.UUID{}, nil
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i].String() < ids[j].String()
	})
	return ids, nil
}

// ReplacePlanPools 整体替换套餐的绑池：先 DELETE 该套餐全部行，poolIDs 非空再用 Create 批量插入。
func (r *Repo) ReplacePlanPools(ctx context.Context, planID uuid.UUID, poolIDs []uuid.UUID) error {
	tx := r.d.Conn(ctx)
	if err := tx.Exec("DELETE FROM plan_node_pools WHERE plan_id = ?", planID).Error; err != nil {
		return mapErr(err)
	}
	if len(poolIDs) == 0 {
		return nil
	}
	rows := make([]model.PlanNodePool, len(poolIDs))
	for i, pid := range poolIDs {
		rows[i] = model.PlanNodePool{PlanID: planID, PoolID: pid}
	}
	return mapErr(tx.Create(&rows).Error)
}
