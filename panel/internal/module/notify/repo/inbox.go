package repo

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

const eventKeysBatch = 1000

// InsertInbox 写一条站内信。
func (r *Repo) InsertInbox(ctx context.Context, it *model.InboxItem) error {
	return r.d.Conn(ctx).Create(it).Error
}

// ListInbox 按 created_at、id 倒序取本人最近 limit 条站内信；onlyUnread 只取未读。
func (r *Repo) ListInbox(ctx context.Context, userID uuid.UUID, onlyUnread bool, limit int) ([]model.InboxItem, error) {
	q := r.d.Conn(ctx).Where("user_id = ?", userID)
	if onlyUnread {
		q = q.Where("read_at IS NULL")
	}
	out := []model.InboxItem{}
	if err := q.Order("created_at DESC, id DESC").Limit(limit).Find(&out).Error; err != nil {
		return nil, err
	}
	if out == nil {
		return []model.InboxItem{}, nil
	}
	return out, nil
}

// CountUnread 本人未读站内信数。
func (r *Repo) CountUnread(ctx context.Context, userID uuid.UUID) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Model(&model.InboxItem{}).
		Where("user_id = ? AND read_at IS NULL", userID).Count(&n).Error
	return n, err
}

// MarkInboxRead 把本人的一条未读站内信标为已读；不存在、不属于本人或已读都不算错。
func (r *Repo) MarkInboxRead(ctx context.Context, userID, id uuid.UUID) error {
	return r.d.Conn(ctx).Exec(
		`UPDATE notification_inbox SET read_at = now()
		  WHERE id = ? AND user_id = ? AND read_at IS NULL`, id, userID,
	).Error
}

// MarkAllInboxRead 把本人全部未读站内信标为已读，返回行数。
func (r *Repo) MarkAllInboxRead(ctx context.Context, userID uuid.UUID) (int64, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE notification_inbox SET read_at = now()
		  WHERE user_id = ? AND read_at IS NULL`, userID,
	)
	return res.RowsAffected, res.Error
}

// PurgeInbox 删 created_at < before 的站内信，每次至多 batch 行。
func (r *Repo) PurgeInbox(ctx context.Context, before time.Time, batch int) (int, error) {
	res := r.d.Conn(ctx).Exec(
		`DELETE FROM notification_inbox WHERE id IN (
		   SELECT id FROM notification_inbox WHERE created_at < ? ORDER BY created_at LIMIT ?)`,
		before, batch)
	return int(res.RowsAffected), res.Error
}

// ExistingEventKeys 返回 keys 里已存在的去重键。
func (r *Repo) ExistingEventKeys(ctx context.Context, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	for i := 0; i < len(keys); i += eventKeysBatch {
		end := i + eventKeysBatch
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[i:end]
		var found []string
		if err := r.d.Conn(ctx).Model(&model.Event{}).
			Where("dedupe_key IN ?", chunk).
			Pluck("dedupe_key", &found).Error; err != nil {
			return nil, err
		}
		for _, k := range found {
			out[k] = true
		}
	}
	return out, nil
}

// InsertEvent 写一个去重键；已存在返回 false（不报错）。
func (r *Repo) InsertEvent(ctx context.Context, e *model.Event) (bool, error) {
	res := r.d.Conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(e)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// PurgeEvents 删 created_at < before 的去重键，每次至多 batch 行。
func (r *Repo) PurgeEvents(ctx context.Context, before time.Time, batch int) (int, error) {
	res := r.d.Conn(ctx).Exec(
		`DELETE FROM notification_events WHERE dedupe_key IN (
		   SELECT dedupe_key FROM notification_events WHERE created_at < ? ORDER BY created_at LIMIT ?)`,
		before, batch)
	return int(res.RowsAffected), res.Error
}
