package repo

import (
	"context"

	"github.com/google/uuid"
)

// planNameRow 是 PlanNamesByIDs 查询的一行。
type planNameRow struct {
	ID   uuid.UUID `gorm:"column:id"`
	Name string    `gorm:"column:name"`
}

// PlanNamesByIDs 批量取套餐名（一条 SQL）；不存在的 id 不在 map 里。
func (r *Repo) PlanNamesByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string)
	if len(ids) == 0 {
		return out, nil
	}
	var rows []planNameRow
	if err := r.d.Conn(ctx).Raw("SELECT id, name FROM plans WHERE id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, mapErr(err)
	}
	for _, row := range rows {
		out[row.ID] = row.Name
	}
	return out, nil
}

// VersionLabel 是套餐版本审计可读名所需字段。
type VersionLabel struct {
	PlanName string
	Version  int
}

// versionLabelRow 是 VersionLabelsByIDs 查询的一行。
type versionLabelRow struct {
	ID       uuid.UUID `gorm:"column:id"`
	PlanName string    `gorm:"column:plan_name"`
	Version  int       `gorm:"column:version"`
}

// VersionLabelsByIDs 批量取套餐版本标签（一条 SQL，JOIN plans）；不存在的 id 不在 map 里。
func (r *Repo) VersionLabelsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]VersionLabel, error) {
	out := make(map[uuid.UUID]VersionLabel)
	if len(ids) == 0 {
		return out, nil
	}
	var rows []versionLabelRow
	if err := r.d.Conn(ctx).Raw(
		`SELECT v.id, p.name AS plan_name, v.version FROM plan_versions v JOIN plans p ON p.id = v.plan_id WHERE v.id IN ?`,
		ids,
	).Scan(&rows).Error; err != nil {
		return nil, mapErr(err)
	}
	for _, row := range rows {
		out[row.ID] = VersionLabel{PlanName: row.PlanName, Version: row.Version}
	}
	return out, nil
}
