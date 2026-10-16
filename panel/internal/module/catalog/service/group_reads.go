package service

import (
	"context"

	"github.com/google/uuid"
)

// GroupRefCount 是一个用户组被套餐可见名单（含已归档套餐）引用的个数。
type GroupRefCount struct {
	Plans int64
}

// GroupRefCounts 返回各用户组的引用个数；没被引用的组不在结果里。给后台用户组列表用（经 app 适配器）。
func (s *Service) GroupRefCounts(ctx context.Context) (map[uuid.UUID]GroupRefCount, error) {
	rows, err := s.repo.GroupRefCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]GroupRefCount, len(rows))
	for _, row := range rows {
		out[row.UserGroupID] = GroupRefCount{Plans: row.Plans}
	}
	return out, nil
}
