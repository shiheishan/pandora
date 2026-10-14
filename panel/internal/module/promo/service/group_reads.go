package service

import (
	"context"

	"github.com/google/uuid"
)

// CouponCountsByGroup 返回各用户组被优惠券限定的个数（不分券状态）；没被限定的组不在结果里。
// 供后台用户组列表显示各组关联券数使用（经 app 适配器调用）。
func (s *Service) CouponCountsByGroup(ctx context.Context) (map[uuid.UUID]int64, error) {
	return s.repo.CouponCountsByGroup(ctx)
}
