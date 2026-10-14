package repo

import (
	"context"

	"github.com/google/uuid"
)

// ServedNodeRow 是一台服务器与它上面一个该跑的节点（ServedOnServerCond）。
// 服务器存在、未删除但没有该跑的节点时只有一行，NodeID 为 nil。
type ServedNodeRow struct {
	ServerID uuid.UUID  `gorm:"column:server_id"`
	NodeID   *uuid.UUID `gorm:"column:node_id"`
	// PoolID 为 nil 是节点没划进节点池（不服务任何人）
	PoolID *uuid.UUID `gorm:"column:pool_id"`
}

// ServedNodes 一条 SQL 取这些服务器上该跑的节点与它们所在的池，按服务器、节点 id 排序；
// 不存在或已删除的服务器不出现。节点按 nodes_server_idx 取。
func (r *Repo) ServedNodes(ctx context.Context, serverIDs []uuid.UUID) ([]ServedNodeRow, error) {
	if len(serverIDs) == 0 {
		return []ServedNodeRow{}, nil
	}
	var rows []ServedNodeRow
	err := r.d.Conn(ctx).Raw(`
		SELECT s.id AS server_id, n.id AS node_id, n.pool_id
		  FROM servers s
		  LEFT JOIN nodes n ON n.server_id = s.id AND (`+ServedOnServerCond+`)
		 WHERE s.id IN ? AND s.deleted_at IS NULL
		 ORDER BY s.id, n.id`, serverIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []ServedNodeRow{}, nil
	}
	return rows, nil
}
