package repo

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
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

// PlanFilter 是后台套餐列表的筛选；空串不限。Q 已去首尾空白，按名称或代码模糊匹配（不区分大小写）。
type PlanFilter struct {
	Kind   string
	Status string
	Q      string
}

// likeEscape 转义 LIKE 的通配符（默认转义符是反斜杠）。
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListPlans 列出套餐（含已归档），按 created_at, id 升序。
func (r *Repo) ListPlans(ctx context.Context, f PlanFilter) ([]model.Plan, error) {
	var plans []model.Plan
	q := r.d.Conn(ctx).Model(&model.Plan{})
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Q != "" {
		pat := "%" + likeEscape(f.Q) + "%"
		q = q.Where("(name ILIKE ? OR code ILIKE ?)", pat, pat)
	}
	if err := q.Order("created_at ASC, id ASC").Find(&plans).Error; err != nil {
		return nil, err
	}
	if plans == nil {
		return []model.Plan{}, nil
	}
	return plans, nil
}

// SellablePlans 列出 status='active' 且 current_version_id 非空的套餐，推荐的在前，再按 created_at, id 升序（门户目录与下单目标用）。
func (r *Repo) SellablePlans(ctx context.Context) ([]model.Plan, error) {
	var plans []model.Plan
	err := r.d.Conn(ctx).
		Where("status = ? AND current_version_id IS NOT NULL", "active").
		Order("recommended DESC, created_at ASC, id ASC").
		Find(&plans).Error
	if err != nil {
		return nil, err
	}
	if plans == nil {
		return []model.Plan{}, nil
	}
	return plans, nil
}

// CodesWithPrefix 取等于 base 或以 "base-" 开头的套餐代码（新建套餐自动生成代码时查重用）。base 只含小写字母、数字与连字符。
func (r *Repo) CodesWithPrefix(ctx context.Context, base string) ([]string, error) {
	var codes []string
	err := r.d.Conn(ctx).Raw(
		"SELECT code FROM plans WHERE code = ? OR code LIKE ?", base, likeEscape(base)+"-%",
	).Scan(&codes).Error
	if err != nil {
		return nil, err
	}
	return codes, nil
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

// planStatusRow 是 PlanStatuses 查询的一行。
type planStatusRow struct {
	ID     uuid.UUID `gorm:"column:id"`
	Status string    `gorm:"column:status"`
}

// PlanStatuses 批量取套餐状态（一条 SQL，只读 id 与 status）：套餐 id → 状态；不存在的 id 不在 map 里。
func (r *Repo) PlanStatuses(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []planStatusRow
	if err := r.d.Conn(ctx).Raw(`SELECT id, status FROM plans WHERE id IN ?`, ids).Scan(&rows).Error; err != nil {
		return nil, mapErr(err)
	}
	for _, row := range rows {
		out[row.ID] = row.Status
	}
	return out, nil
}

// InsertPlan 插入套餐，回填 ID 等列（用 Create）；有重置价时写前 money.Check。
func (r *Repo) InsertPlan(ctx context.Context, p *model.Plan) error {
	if p.ResetPrice != nil {
		if err := money.Check(*p.ResetPrice); err != nil {
			return err
		}
	}
	return mapErr(r.d.Conn(ctx).Create(p).Error)
}

// planEditable 是改套餐行的公共条件：乐观锁对上、没归档。
const planEditable = " WHERE id = ? AND row_version = ? AND status IN ('draft', 'active')"

// UpdatePlanBasic 改名称与说明并把 row_version 加一。
func (r *Repo) UpdatePlanBasic(ctx context.Context, id uuid.UUID, expected int64, name string, description *string) (bool, error) {
	return r.execOne(ctx, `UPDATE plans SET name = ?, description = ?,
		row_version = row_version + 1, updated_at = now()`+planEditable,
		name, description, id, expected)
}

// UpdatePlanSales 改销售设置（可见性、三个开关、推荐）并把 row_version 加一；可见组另由 SetVisibleGroups 换。
func (r *Repo) UpdatePlanSales(ctx context.Context, id uuid.UUID, expected int64, p *model.Plan) (bool, error) {
	return r.execOne(ctx, `UPDATE plans SET visibility = ?,
		allow_new_purchase = ?, allow_renewal = ?, allow_upgrade = ?, recommended = ?,
		row_version = row_version + 1, updated_at = now()`+planEditable,
		p.Visibility, p.AllowNewPurchase, p.AllowRenewal, p.AllowUpgrade, p.Recommended, id, expected)
}

// UpdatePlanReset 改流量重置价格与次数上限（都为 nil = 不卖）并把 row_version 加一；写前 money.Check。
func (r *Repo) UpdatePlanReset(ctx context.Context, id uuid.UUID, expected int64, price *decimal.Decimal, limit *int) (bool, error) {
	if price != nil {
		if err := money.Check(*price); err != nil {
			return false, err
		}
	}
	return r.execOne(ctx, `UPDATE plans SET reset_price = ?, reset_limit = ?,
		row_version = row_version + 1, updated_at = now()`+planEditable,
		price, limit, id, expected)
}

// BumpPlanRow 只把 row_version 加一（改价、改线路、改用量这些不落在 plans 行上的改动，也走套餐的乐观锁）。
func (r *Repo) BumpPlanRow(ctx context.Context, id uuid.UUID, expected int64) (bool, error) {
	return r.execOne(ctx, `UPDATE plans SET row_version = row_version + 1, updated_at = now()`+planEditable, id, expected)
}

// execOne 执行一条 UPDATE，返回是否恰好改了一行。
func (r *Repo) execOne(ctx context.Context, q string, args ...any) (bool, error) {
	res := r.d.Conn(ctx).Exec(q, args...)
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
