package service

import (
	"context"

	"github.com/google/uuid"
)

// Contact 是用户的联系信息：工单队列、详情与可指派客服目录用。
type Contact struct {
	Email       string
	DisplayName *string
}

// Contacts 批量取用户邮箱与显示名（一条 SQL）；不存在的 id 不在结果里，ids 为空不查库。
func (s *Service) Contacts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Contact, error) {
	out := make(map[uuid.UUID]Contact, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.repo.Contacts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = Contact{Email: row.Email, DisplayName: row.DisplayName}
	}
	return out, nil
}
