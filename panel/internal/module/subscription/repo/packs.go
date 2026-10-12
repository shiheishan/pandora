package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// InsertPackGrant 插入一条流量包余额。
func (r *Repo) InsertPackGrant(ctx context.Context, g *model.PackGrant) error {
	return mapErr(r.d.Conn(ctx).Create(g).Error)
}

// PackGrantExists 判断某个来源是否已经发过流量包（(source, source_id) 唯一）。
func (r *Repo) PackGrantExists(ctx context.Context, source string, sourceID uuid.UUID) (bool, error) {
	var n int64
	err := r.d.Conn(ctx).Model(&model.PackGrant{}).Where("source = ? AND source_id = ?", source, sourceID).Count(&n).Error
	return n > 0, mapErr(err)
}

// PackGrantsByUser 读用户最近的流量包（含已用完的），创建时间倒序。
func (r *Repo) PackGrantsByUser(ctx context.Context, userID uuid.UUID, limit int) ([]model.PackGrant, error) {
	var out []model.PackGrant
	err := r.d.Conn(ctx).Where("user_id = ?", userID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&out).Error
	return out, mapErr(err)
}

// PackRemainingByUser 汇总用户全部流量包的剩余字节。
func (r *Repo) PackRemainingByUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw(
		"SELECT coalesce(sum(granted_bytes - consumed_bytes), 0)::bigint FROM traffic_pack_grants WHERE user_id = ?",
		userID,
	).Scan(&n).Error
	return n, mapErr(err)
}

// remainingRow 是按某一列分组的剩余字节。
type remainingRow struct {
	Key       uuid.UUID `gorm:"column:key"`
	Remaining int64     `gorm:"column:remaining"`
}

// PackRemainingBySubscriptions 按订阅汇总已分配流量包的剩余字节；没有剩余的订阅不在结果里。
func (r *Repo) PackRemainingBySubscriptions(ctx context.Context, subIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	out := make(map[uuid.UUID]int64, len(subIDs))
	if len(subIDs) == 0 {
		return out, nil
	}
	var rows []remainingRow
	err := r.d.Conn(ctx).Raw(
		`SELECT subscription_id AS key, sum(granted_bytes - consumed_bytes)::bigint AS remaining
		   FROM traffic_pack_grants
		  WHERE subscription_id IN ? AND consumed_bytes < granted_bytes
		  GROUP BY subscription_id`, subIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	for _, row := range rows {
		out[row.Key] = row.Remaining
	}
	return out, nil
}

// UnattachedRemainingByUsers 按用户汇总未分配流量包的剩余字节；没有剩余的用户不在结果里。
func (r *Repo) UnattachedRemainingByUsers(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	out := make(map[uuid.UUID]int64, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	var rows []remainingRow
	err := r.d.Conn(ctx).Raw(
		`SELECT user_id AS key, sum(granted_bytes - consumed_bytes)::bigint AS remaining
		   FROM traffic_pack_grants
		  WHERE user_id IN ? AND subscription_id IS NULL AND consumed_bytes < granted_bytes
		  GROUP BY user_id`, userIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	for _, row := range rows {
		out[row.Key] = row.Remaining
	}
	return out, nil
}

// OpenPackGrants 读用户挂在 subID（nil 表示未分配）下还有剩余的流量包，按创建先后；lock 为真时加 FOR UPDATE。
func (r *Repo) OpenPackGrants(ctx context.Context, userID uuid.UUID, subID *uuid.UUID, lock bool) ([]model.PackGrant, error) {
	q := `SELECT * FROM traffic_pack_grants
	       WHERE user_id = ? AND subscription_id IS NOT DISTINCT FROM ?::uuid AND consumed_bytes < granted_bytes
	       ORDER BY created_at, id`
	if lock {
		q += " FOR UPDATE"
	}
	var out []model.PackGrant
	err := r.d.Conn(ctx).Raw(q, userID, subID).Scan(&out).Error
	return out, mapErr(err)
}

// AttachPackGrants 把一组流量包挂到订阅 to 上；改到的行数不对返回错误。
func (r *Repo) AttachPackGrants(ctx context.Context, ids []uuid.UUID, to uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	res := r.d.Conn(ctx).Model(&model.PackGrant{}).Where("id IN ?", ids).
		Updates(map[string]any{"subscription_id": to, "updated_at": now()})
	if res.Error != nil {
		return mapErr(res.Error)
	}
	if res.RowsAffected != int64(len(ids)) {
		return fmt.Errorf("subscription: 挂接流量包改到 %d 行，应为 %d 行", res.RowsAffected, len(ids))
	}
	return nil
}

// InsertPackTransfers 批量追加流量包转移记录。
func (r *Repo) InsertPackTransfers(ctx context.Context, rows []model.PackTransfer) error {
	if len(rows) == 0 {
		return nil
	}
	return mapErr(r.d.Conn(ctx).Create(&rows).Error)
}
