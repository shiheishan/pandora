package service

import (
	"context"

	"github.com/google/uuid"
)

// ActiveUserGroups 批量取 active 账号所在的用户组：键只含 active 账号（停用、封禁、不存在的都不在结果里），
// 值为 nil 表示默认组（没有分组）。节点名单（node 模块）用；ids 多于一万个时分批查。
func (s *Service) ActiveUserGroups(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*uuid.UUID, error) {
	out := make(map[uuid.UUID]*uuid.UUID, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.repo.ActiveUserGroups(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.UserGroupID
	}
	return out, nil
}
