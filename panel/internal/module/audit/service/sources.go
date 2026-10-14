package service

import (
	"context"

	"github.com/google/uuid"
)

// SharesSource 判断两个用户是否来自同一来源地址：两人的审计事件里出现过相同的 ip_hash（commission 同源复核用）。
// 同一人或任一方为空 id 时回 false。
func (s *Service) SharesSource(ctx context.Context, a, b uuid.UUID) (bool, error) {
	if a == uuid.Nil || b == uuid.Nil || a == b {
		return false, nil
	}
	return s.repo.SharesIPHash(ctx, a, b)
}
