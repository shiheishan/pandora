package service

import (
	"context"

	"github.com/google/uuid"
)

// GroupPoolRef 是一个节点池的 id 与名称。
type GroupPoolRef struct {
	ID   uuid.UUID
	Name string
}

// ExclusivePoolsByGroup 返回各用户组被列入仅限名单的节点池（按池名、id 升序）；没被列入的组不在结果里。给后台用户组列表用（经 app 适配器）。
func (s *Service) ExclusivePoolsByGroup(ctx context.Context) (map[uuid.UUID][]GroupPoolRef, error) {
	rows, err := s.repo.PoolsByUserGroup(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]GroupPoolRef)
	for _, row := range rows {
		out[row.UserGroupID] = append(out[row.UserGroupID], GroupPoolRef{
			ID:   row.PoolID,
			Name: row.Name,
		})
	}
	return out, nil
}
