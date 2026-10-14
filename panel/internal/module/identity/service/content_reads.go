package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// DisplayNames 批量取用户的显示名（一条 SQL）：有昵称用昵称，否则用邮箱；查不到的 id 不在结果里。
// content 后台版本历史显示作者用。
func (s *Service) DisplayNames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	users, err := s.repo.UsersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(users))
	for _, u := range users {
		name := u.Email
		if u.DisplayName != nil && strings.TrimSpace(*u.DisplayName) != "" {
			name = strings.TrimSpace(*u.DisplayName)
		}
		out[u.ID] = name
	}
	return out, nil
}
