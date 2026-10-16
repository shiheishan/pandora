package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
)

// InsertNextVersion 给套餐建下一个草稿版本（版本号 = 该套餐现有最大版本号 + 1，没有则 1），其余列取库里缺省值，返回整行。
func (r *Repo) InsertNextVersion(ctx context.Context, planID uuid.UUID, createdBy *uuid.UUID) (model.PlanVersion, error) {
	var v model.PlanVersion
	err := r.d.Conn(ctx).Raw(`INSERT INTO plan_versions (plan_id, version, created_by)
		SELECT ?, coalesce(max(version), 0) + 1, ? FROM plan_versions WHERE plan_id = ?
		RETURNING *`, planID, createdBy, planID).Scan(&v).Error
	if err != nil {
		return model.PlanVersion{}, mapErr(err)
	}
	return v, nil
}

// VersionByID 读属于 planID 的版本 versionID（WHERE plan_id = ? AND id = ?），lock 决定行锁。
func (r *Repo) VersionByID(ctx context.Context, planID, versionID uuid.UUID, lock Lock) (model.PlanVersion, bool, error) {
	var v model.PlanVersion
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM plan_versions WHERE plan_id = ? AND id = ?"+lock.suffix(),
		planID, versionID,
	).Scan(&v).Error
	if err != nil {
		return model.PlanVersion{}, false, err
	}
	if v.ID == uuid.Nil {
		return model.PlanVersion{}, false, nil
	}
	return v, true, nil
}

// VersionsByPlan 列出套餐的全部版本，按 version 降序。
func (r *Repo) VersionsByPlan(ctx context.Context, planID uuid.UUID) ([]model.PlanVersion, error) {
	var versions []model.PlanVersion
	err := r.d.Conn(ctx).
		Where("plan_id = ?", planID).
		Order("version DESC").
		Find(&versions).Error
	if err != nil {
		return nil, err
	}
	if versions == nil {
		return []model.PlanVersion{}, nil
	}
	return versions, nil
}

// VersionsByIDs 批量读版本（一条 SQL）。
func (r *Repo) VersionsByIDs(ctx context.Context, ids []uuid.UUID) ([]model.PlanVersion, error) {
	if len(ids) == 0 {
		return []model.PlanVersion{}, nil
	}
	var versions []model.PlanVersion
	err := r.d.Conn(ctx).Where("id IN ?", ids).Find(&versions).Error
	if err != nil {
		return nil, err
	}
	if versions == nil {
		return []model.PlanVersion{}, nil
	}
	return versions, nil
}

// draftVersionIDRow 是 DraftVersionIDs 查询的一行。
type draftVersionIDRow struct {
	PlanID    uuid.UUID `gorm:"column:plan_id"`
	VersionID uuid.UUID `gorm:"column:id"`
}

// DraftVersionIDs 批量取套餐的草稿版本：plan_id → 版本 id（每个套餐至多一个）。没有草稿的不在 map 里。
func (r *Repo) DraftVersionIDs(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	if len(planIDs) == 0 {
		return map[uuid.UUID]uuid.UUID{}, nil
	}
	var rows []draftVersionIDRow
	err := r.d.Conn(ctx).Raw(
		"SELECT plan_id, id FROM plan_versions WHERE plan_id IN ? AND status = 'draft'",
		planIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]uuid.UUID, len(rows))
	for _, row := range rows {
		out[row.PlanID] = row.VersionID
	}
	return out, nil
}

// UpdateVersionUsage 改草稿版本的设备数与限速并把 row_version 加一；条件 id = ? AND status = 'draft'（草稿只由本模块在套餐行锁下改，不另验乐观锁）。
func (r *Repo) UpdateVersionUsage(ctx context.Context, versionID uuid.UUID, maxDevices, throttleKbps *int) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE plan_versions SET
		max_devices = ?, throttle_kbps = ?, row_version = row_version + 1
		WHERE id = ? AND status = 'draft'`,
		maxDevices, throttleKbps, versionID,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// PublishVersion 发布草稿：status='published', frozen_at=now(), row_version+1；条件 id = ? AND status = 'draft'。
func (r *Repo) PublishVersion(ctx context.Context, versionID uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE plan_versions SET
		status = 'published', frozen_at = now(), row_version = row_version + 1
		WHERE id = ? AND status = 'draft'`,
		versionID,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ReplaceVersionChildren 整体替换草稿版本的权益与配额：先 DELETE 两张表里 plan_version_id = ? 的行，
// 再分别用 Create 批量插入 ents、quotas（为空就不插）。插入前把每行的 PlanVersionID 设为 versionID。
func (r *Repo) ReplaceVersionChildren(ctx context.Context, versionID uuid.UUID, ents []model.Entitlement, quotas []model.QuotaDefinition) error {
	tx := r.d.Conn(ctx)
	if err := tx.Exec("DELETE FROM entitlements WHERE plan_version_id = ?", versionID).Error; err != nil {
		return mapErr(err)
	}
	if err := tx.Exec("DELETE FROM quota_definitions WHERE plan_version_id = ?", versionID).Error; err != nil {
		return mapErr(err)
	}
	if len(ents) > 0 {
		for i := range ents {
			ents[i].PlanVersionID = versionID
		}
		if err := tx.Create(&ents).Error; err != nil {
			return mapErr(err)
		}
	}
	if len(quotas) > 0 {
		for i := range quotas {
			quotas[i].PlanVersionID = versionID
		}
		if err := tx.Create(&quotas).Error; err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// Entitlements 批量取权益：plan_version_id → 行（按 code 升序）。
func (r *Repo) Entitlements(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID][]model.Entitlement, error) {
	if len(versionIDs) == 0 {
		return map[uuid.UUID][]model.Entitlement{}, nil
	}
	var rows []model.Entitlement
	err := r.d.Conn(ctx).
		Where("plan_version_id IN ?", versionIDs).
		Order("plan_version_id ASC, code ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]model.Entitlement)
	for _, row := range rows {
		out[row.PlanVersionID] = append(out[row.PlanVersionID], row)
	}
	return out, nil
}

// Quotas 批量取配额定义：plan_version_id → 行（按 metric, period 升序）。
func (r *Repo) Quotas(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID][]model.QuotaDefinition, error) {
	if len(versionIDs) == 0 {
		return map[uuid.UUID][]model.QuotaDefinition{}, nil
	}
	var rows []model.QuotaDefinition
	err := r.d.Conn(ctx).
		Where("plan_version_id IN ?", versionIDs).
		Order("plan_version_id ASC, metric ASC, period ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]model.QuotaDefinition)
	for _, row := range rows {
		out[row.PlanVersionID] = append(out[row.PlanVersionID], row)
	}
	return out, nil
}

// versionIDRow 是 VersionIDsByPlans 查询的一行。
type versionIDRow struct {
	PlanID uuid.UUID `gorm:"column:plan_id"`
	ID     uuid.UUID `gorm:"column:id"`
}

// VersionIDsByPlans 批量取套餐的全部版本 id（任何状态，一条 SQL）：套餐 id → 版本 id（按版本号升序）。没有版本的套餐不在 map 里。
func (r *Repo) VersionIDsByPlans(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	if len(planIDs) == 0 {
		return map[uuid.UUID][]uuid.UUID{}, nil
	}
	var rows []versionIDRow
	err := r.d.Conn(ctx).Raw(
		"SELECT plan_id, id FROM plan_versions WHERE plan_id IN ? ORDER BY plan_id, version",
		planIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]uuid.UUID)
	for _, row := range rows {
		out[row.PlanID] = append(out[row.PlanID], row.ID)
	}
	return out, nil
}
