package service

// 节点配置文档（p5-routing 实现）。

import (
	"context"

	"github.com/google/uuid"
)

// ServerConfigs 一次算出这些服务器上每个该跑的节点（repo.ServedOnServerCond）的生效配置文档：
// 服务器 id → 文档（按节点 id 升序）。会话层在配置纪元变了、节点新建流时调。不存在或已删除的服务器不在结果里。
func (s *Service) ServerConfigs(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID][]NodeConfigDoc, error) {
	panic("p5-routing: ServerConfigs")
}
