// 节点池与池的用户组名单（池限定用户组的判定在 service.poolAdmits，名单与线路共用）。
package repo

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

var (
	// ErrPoolCodeTaken 是节点池标识重复（node_pools_code_key）。
	ErrPoolCodeTaken = errors.New("node: 节点池标识已存在")
	// ErrPoolReferenced 是删池时仍被引用（外键 RESTRICT：节点或套餐版本）。
	ErrPoolReferenced = errors.New("node: 节点池仍被引用")
	// ErrPoolGroupMissing 是名单里的用户组不存在（外键）。
	ErrPoolGroupMissing = errors.New("node: 用户组不存在")
)

// mapPoolErr：唯一冲突（gorm.ErrDuplicatedKey 或 pgconn.PgError.Code == "23505"）→ onUnique；
// 外键冲突（gorm.ErrForeignKeyViolated 或 "23503"）→ onFK；onUnique / onFK 为 nil 时原样返回；其余原样返回。
func mapPoolErr(err, onUnique, onFK error) error {
	if err == nil {
		return nil
	}
	isUnique := errors.Is(err, gorm.ErrDuplicatedKey)
	isFK := errors.Is(err, gorm.ErrForeignKeyViolated)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			isUnique = true
		case "23503":
			isFK = true
		}
	}
	if isUnique && onUnique != nil {
		return onUnique
	}
	if isFK && onFK != nil {
		return onFK
	}
	return err
}

// PoolNodeCount 是一个池的节点数：Nodes 未删除的，Active 其中在役的。
type PoolNodeCount struct {
	Nodes  int64
	Active int64
}

// PoolMemberRow 是池里的一个未删除节点。
type PoolMemberRow struct {
	PoolID uuid.UUID
	ID     uuid.UUID
	Name   string
	NodeNo int64
}

type poolNodeCountRow struct {
	PoolID uuid.UUID `gorm:"column:pool_id"`
	Nodes  int64     `gorm:"column:nodes"`
	Active int64     `gorm:"column:active"`
}

type poolDeliverableCountRow struct {
	PoolID uuid.UUID `gorm:"column:pool_id"`
	N      int64     `gorm:"column:n"`
}

type poolUserGroupRow struct {
	PoolID      uuid.UUID `gorm:"column:pool_id"`
	UserGroupID uuid.UUID `gorm:"column:user_group_id"`
}

type poolIDRow struct {
	ID uuid.UUID `gorm:"column:id"`
}

// ListPools 返回全部节点池。
func (r *Repo) ListPools(ctx context.Context) ([]model.NodePool, error) {
	var pools []model.NodePool
	err := r.d.Conn(ctx).Order("name, created_at, id").Find(&pools).Error
	if err != nil {
		return nil, err
	}
	if pools == nil {
		return []model.NodePool{}, nil
	}
	return pools, nil
}

// PickablePools 返回可选池：默认排除 disabled；include 非空时额外包含这些 id（即使已停用）。
func (r *Repo) PickablePools(ctx context.Context, include []uuid.UUID) ([]model.NodePool, error) {
	q := r.d.Conn(ctx).Model(&model.NodePool{})
	if len(include) > 0 {
		q = q.Where("status <> ? OR id IN ?", "disabled", include)
	} else {
		q = q.Where("status <> ?", "disabled")
	}
	var pools []model.NodePool
	err := q.Order("name, id").Find(&pools).Error
	if err != nil {
		return nil, err
	}
	if pools == nil {
		return []model.NodePool{}, nil
	}
	return pools, nil
}

// PoolNodeCounts 批量统计各池未删节点数与在役数。
func (r *Repo) PoolNodeCounts(ctx context.Context, poolIDs []uuid.UUID) (map[uuid.UUID]PoolNodeCount, error) {
	if len(poolIDs) == 0 {
		return map[uuid.UUID]PoolNodeCount{}, nil
	}
	var rows []poolNodeCountRow
	err := r.d.Conn(ctx).Raw(
		`SELECT pool_id, count(*) AS nodes, count(*) FILTER (WHERE serving_status = 'active') AS active
		 FROM nodes WHERE pool_id IN ? AND deleted_at IS NULL GROUP BY pool_id`,
		poolIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]PoolNodeCount, len(rows))
	for _, row := range rows {
		out[row.PoolID] = PoolNodeCount{Nodes: row.Nodes, Active: row.Active}
	}
	return out, nil
}

// DeliverableCountsByPool 批量统计各池可下发节点数；套餐页每个池的 deliverable_nodes、套餐列表的可服务节点数都只用它，口径一致。
func (r *Repo) DeliverableCountsByPool(ctx context.Context, poolIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	if len(poolIDs) == 0 {
		return map[uuid.UUID]int64{}, nil
	}
	var rows []poolDeliverableCountRow
	err := r.d.Conn(ctx).Raw(
		`SELECT n.pool_id, count(*) AS n FROM nodes n JOIN servers s ON s.id = n.server_id
		 WHERE n.pool_id IN ? AND `+DeliverableNodeCond+` GROUP BY n.pool_id`,
		poolIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int64, len(rows))
	for _, row := range rows {
		out[row.PoolID] = row.N
	}
	return out, nil
}

// PoolMembers 批量列出各池未删成员节点。
func (r *Repo) PoolMembers(ctx context.Context, poolIDs []uuid.UUID) (map[uuid.UUID][]PoolMemberRow, error) {
	if len(poolIDs) == 0 {
		return map[uuid.UUID][]PoolMemberRow{}, nil
	}
	var rows []PoolMemberRow
	err := r.d.Conn(ctx).Raw(
		`SELECT pool_id, id, name, node_no FROM nodes WHERE pool_id IN ? AND deleted_at IS NULL ORDER BY sort_order, node_no`,
		poolIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]PoolMemberRow)
	for _, row := range rows {
		out[row.PoolID] = append(out[row.PoolID], row)
	}
	return out, nil
}

// PoolUserGroupIDs 批量读取各池「仅限用户组」名单。
func (r *Repo) PoolUserGroupIDs(ctx context.Context, poolIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	if len(poolIDs) == 0 {
		return map[uuid.UUID][]uuid.UUID{}, nil
	}
	var rows []poolUserGroupRow
	err := r.d.Conn(ctx).Raw(
		`SELECT pool_id, user_group_id FROM node_pool_user_groups WHERE pool_id IN ? ORDER BY pool_id, user_group_id`,
		poolIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]uuid.UUID)
	for _, row := range rows {
		out[row.PoolID] = append(out[row.PoolID], row.UserGroupID)
	}
	return out, nil
}

// InsertPool 插入节点池（回填 ID 与时间）。
func (r *Repo) InsertPool(ctx context.Context, p *model.NodePool) error {
	err := r.d.Conn(ctx).Create(p).Error
	return mapPoolErr(err, ErrPoolCodeTaken, nil)
}

// PoolByIDForUpdate 按 id 加行锁读取节点池。
func (r *Repo) PoolByIDForUpdate(ctx context.Context, id uuid.UUID) (model.NodePool, bool, error) {
	var p model.NodePool
	err := r.d.Conn(ctx).Raw("SELECT * FROM node_pools WHERE id = ? FOR UPDATE", id).Scan(&p).Error
	if err != nil {
		return model.NodePool{}, false, err
	}
	if p.ID == uuid.Nil {
		return model.NodePool{}, false, nil
	}
	return p, true, nil
}

// UpdatePool 更新节点池展示字段。
func (r *Repo) UpdatePool(ctx context.Context, id uuid.UUID, name, region, status string) error {
	return r.d.Conn(ctx).Exec(
		`UPDATE node_pools SET name = ?, region = ?, status = ?, updated_at = now() WHERE id = ?`,
		name, region, status, id,
	).Error
}

// ReplacePoolUserGroups 整体替换池的用户组名单（空切片表示不限定组）。
func (r *Repo) ReplacePoolUserGroups(ctx context.Context, poolID uuid.UUID, groupIDs []uuid.UUID) error {
	tx := r.d.Conn(ctx)
	if err := tx.Exec("DELETE FROM node_pool_user_groups WHERE pool_id = ?", poolID).Error; err != nil {
		return mapPoolErr(err, nil, ErrPoolGroupMissing)
	}
	if len(groupIDs) == 0 {
		return nil
	}
	rows := make([]model.NodePoolUserGroup, len(groupIDs))
	for i, gid := range groupIDs {
		rows[i] = model.NodePoolUserGroup{PoolID: poolID, UserGroupID: gid}
	}
	err := tx.Create(&rows).Error
	return mapPoolErr(err, nil, ErrPoolGroupMissing)
}

// LivePoolNodeCount 统计池内未删节点数。
func (r *Repo) LivePoolNodeCount(ctx context.Context, poolID uuid.UUID) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw(
		`SELECT count(*) FROM nodes WHERE pool_id = ? AND deleted_at IS NULL`,
		poolID,
	).Scan(&n).Error
	return n, err
}

// DetachDeletedPoolNodes 将已软删节点从池中解绑，否则外键会挡住删池。
func (r *Repo) DetachDeletedPoolNodes(ctx context.Context, poolID uuid.UUID) error {
	return r.d.Conn(ctx).Exec(
		`UPDATE nodes SET pool_id = NULL WHERE pool_id = ? AND deleted_at IS NOT NULL`,
		poolID,
	).Error
}

// DeletePool 删除节点池；删到一行返回 true。
func (r *Repo) DeletePool(ctx context.Context, id uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec("DELETE FROM node_pools WHERE id = ?", id)
	if res.Error != nil {
		return false, mapPoolErr(res.Error, nil, ErrPoolReferenced)
	}
	return res.RowsAffected == 1, nil
}

// LockAssignablePools 对可分配池加 FOR SHARE 行锁；ORDER BY 在加锁之下按 id 顺序加锁，避免并发互等死锁，并挡住删池与停用。
func (r *Repo) LockAssignablePools(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return []uuid.UUID{}, nil
	}
	var rows []poolIDRow
	err := r.d.Conn(ctx).Raw(
		`SELECT id FROM node_pools WHERE id IN ? AND status <> 'disabled' ORDER BY id FOR SHARE`,
		ids,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out, nil
}

// HasDeliverableNode 判断是否存在可下发节点；发布检查用，锁住找到的节点与服务器到事务结束。
func (r *Repo) HasDeliverableNode(ctx context.Context, poolIDs []uuid.UUID) (bool, error) {
	if len(poolIDs) == 0 {
		return false, nil
	}
	var rows []poolIDRow
	err := r.d.Conn(ctx).Raw(
		`SELECT n.id FROM nodes n JOIN servers s ON s.id = n.server_id WHERE n.pool_id IN ? AND `+DeliverableNodeCond+
			` ORDER BY n.id LIMIT 1 FOR SHARE OF n, s`,
		poolIDs,
	).Scan(&rows).Error
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}
