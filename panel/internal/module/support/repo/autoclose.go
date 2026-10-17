package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AutoCloseRow 是一张待自动关闭的工单。
type AutoCloseRow struct {
	ID     uuid.UUID `gorm:"column:id"`
	UserID uuid.UUID `gorm:"column:user_id"`
}

// LockAutoCloseCandidates 取 status = pending_user 且 updated_at <= before 的工单，按 updated_at、id 升序最多 limit 张，
// FOR UPDATE SKIP LOCKED（走迁移 00044 的 tickets_auto_close_idx）。
func (r *Repo) LockAutoCloseCandidates(ctx context.Context, before time.Time, limit int) ([]AutoCloseRow, error) {
	var rows []AutoCloseRow
	err := r.d.Conn(ctx).Raw(`
SELECT id, user_id
  FROM tickets
 WHERE status = 'pending_user'
   AND updated_at <= ?
 ORDER BY updated_at, id
 LIMIT ?
 FOR UPDATE SKIP LOCKED`, before, limit).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		rows = []AutoCloseRow{}
	}
	return rows, nil
}
