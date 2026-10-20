// 在线升级任务的读写；状态更新一律带旧状态条件，影响 0 行由 service 当作已被别处处理。
package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

// upgradeColumns 是读 server_upgrades 一行的列清单（别名 u）。
const upgradeColumns = `u.id, u.server_id, u.target_version, u.target_serial, u.target_sha256,
 u.from_version, u.from_sha256, u.status, u.reason_code, u.detail, u.created_by,
 u.created_at, u.sent_at, u.staged_at, u.finished_at, u.deadline_at`

// ServersByIDs 按 id 批量读未删除的服务器；ids 为空返回空切片不查库。
func (r *Repo) ServersByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Server, error) {
	if len(ids) == 0 {
		return []model.Server{}, nil
	}
	var out []model.Server
	err := r.d.Conn(ctx).Raw(
		"SELECT "+serverColumns+" FROM servers s WHERE s.id IN ? AND s.deleted_at IS NULL", ids,
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []model.Server{}
	}
	return out, nil
}

// ServerUpgradeInsert 是新建升级任务的一行。
type ServerUpgradeInsert struct {
	ServerID                uuid.UUID
	TargetVersion           string
	TargetSerial            int64
	TargetSHA256            string
	FromVersion, FromSHA256 *string
	CreatedBy               *uuid.UUID
	CreatedAt, DeadlineAt   time.Time
}

type upgradeInsertReturned struct {
	ID       uuid.UUID `gorm:"column:id"`
	ServerID uuid.UUID `gorm:"column:server_id"`
}

// InsertServerUpgrades 批量插入 sent 任务；未结束任务已存在的服务器被部分唯一索引挡下，不在返回里。
func (r *Repo) InsertServerUpgrades(ctx context.Context, rows []ServerUpgradeInsert) (map[uuid.UUID]uuid.UUID, error) {
	out := make(map[uuid.UUID]uuid.UUID)
	if len(rows) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(rows))
	args := make([]any, 0, len(rows)*10)
	for i, row := range rows {
		placeholders[i] = "(?, ?, ?, ?, ?, ?, 'sent', ?, ?, ?)"
		args = append(args,
			row.ServerID, row.TargetVersion, row.TargetSerial, row.TargetSHA256,
			row.FromVersion, row.FromSHA256, row.CreatedBy, row.CreatedAt, row.DeadlineAt,
		)
	}
	q := `INSERT INTO server_upgrades
		(server_id, target_version, target_serial, target_sha256, from_version, from_sha256, status, created_by, created_at, deadline_at)
		VALUES ` + strings.Join(placeholders, ", ") + `
		ON CONFLICT (server_id) WHERE status IN ('sent', 'staged') DO NOTHING
		RETURNING id, server_id`
	var returned []upgradeInsertReturned
	if err := r.d.Conn(ctx).Raw(q, args...).Scan(&returned).Error; err != nil {
		return nil, err
	}
	for _, row := range returned {
		out[row.ServerID] = row.ID
	}
	return out, nil
}

// LatestServerUpgrades 取每台服务器最近一次升级任务；ids 为空返回空 map。
func (r *Repo) LatestServerUpgrades(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID]model.ServerUpgrade, error) {
	out := make(map[uuid.UUID]model.ServerUpgrade)
	if len(serverIDs) == 0 {
		return out, nil
	}
	var rows []model.ServerUpgrade
	err := r.d.Conn(ctx).Raw(
		`SELECT DISTINCT ON (u.server_id) `+upgradeColumns+`
		 FROM server_upgrades u WHERE u.server_id IN ?
		 ORDER BY u.server_id, u.created_at DESC, u.id DESC`, serverIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ServerID] = row
	}
	return out, nil
}

// MarkServerUpgradeSent 记任务排进发送队列的时刻；sent_at 已有则不改。
func (r *Repo) MarkServerUpgradeSent(ctx context.Context, id uuid.UUID, at time.Time) error {
	return r.d.Conn(ctx).Exec(
		`UPDATE server_upgrades SET sent_at = ? WHERE id = ? AND sent_at IS NULL`, at, id,
	).Error
}

// StageServerUpgrade 把 sent 任务标为 staged；返回是否改到行。
func (r *Repo) StageServerUpgrade(ctx context.Context, id, serverID uuid.UUID, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE server_upgrades SET status = 'staged', staged_at = ?
		 WHERE id = ? AND server_id = ? AND status = 'sent'`, at, id, serverID,
	)
	return res.RowsAffected > 0, res.Error
}

// FinishServerUpgrade 把未结束任务标为结束态；返回是否改到行。
func (r *Repo) FinishServerUpgrade(ctx context.Context, id, serverID uuid.UUID, from []string, to string, reason, detail *string, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE server_upgrades SET status = ?, reason_code = ?, detail = ?, finished_at = ?
		 WHERE id = ? AND server_id = ? AND status IN ?`,
		to, reason, detail, at, id, serverID, from,
	)
	return res.RowsAffected > 0, res.Error
}

// SucceedServerUpgradeBySHA 按节点自报摘要把未结束且目标一致的任务标为 succeeded；返回是否改到行。
func (r *Repo) SucceedServerUpgradeBySHA(ctx context.Context, serverID uuid.UUID, sha string, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE server_upgrades SET status = 'succeeded', reason_code = NULL, finished_at = ?
		 WHERE server_id = ? AND status IN ('sent', 'staged') AND target_sha256 = ?`,
		at, serverID, sha,
	)
	return res.RowsAffected > 0, res.Error
}

// TimeoutServerUpgrades 把过了 deadline 的未结束任务标为 timed_out；返回涉及的 server_id（可能重复）。
func (r *Repo) TimeoutServerUpgrades(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.d.Conn(ctx).Raw(`
		UPDATE server_upgrades SET status = 'timed_out', finished_at = ?
		WHERE id IN (
			SELECT id FROM server_upgrades
			WHERE status IN ('sent', 'staged') AND deadline_at < ?
			ORDER BY deadline_at LIMIT ? FOR UPDATE SKIP LOCKED
		) AND status IN ('sent', 'staged')
		RETURNING server_id`, now, now, limit).Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	if ids == nil {
		return []uuid.UUID{}, nil
	}
	return ids, nil
}
