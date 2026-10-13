package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ServersBeatBetween 返回未删除、最后心跳在 (after, upTo] 里的服务器 id（升序），给在线巡检判「刚跨过窗口」用。
func (r *Repo) ServersBeatBetween(ctx context.Context, after, upTo time.Time) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.d.Conn(ctx).Raw(`SELECT id FROM servers WHERE deleted_at IS NULL AND last_heartbeat_at > ? AND last_heartbeat_at <= ? ORDER BY id`,
		after, upTo).Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []uuid.UUID{}, nil
	}
	return ids, nil
}
