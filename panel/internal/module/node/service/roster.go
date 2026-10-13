package service

// 名单（p5-delivery 实现）。

import (
	"context"

	"github.com/google/uuid"
)

// Rosters 一次算出这些服务器上每个该跑的节点（repo.ServedOnServerCond）的完整名单：服务器 id → 名单（按节点 id 升序）。
// 会话层在名单纪元变了、节点新建流时调；一批服务器共用一次查询，同一内容的名单只编码一次。
// 不存在或已删除的服务器不在结果里。
func (s *Service) Rosters(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID][]NodeRoster, error) {
	panic("p5-delivery: Rosters")
}
