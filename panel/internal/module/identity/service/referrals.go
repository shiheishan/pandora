package service

import (
	"context"

	"github.com/google/uuid"
)

// ReferrerOf 返回用户的邀请人，没有时返回 nil（commission 计提用；在调用方事务里读）。
func (s *Service) ReferrerOf(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error) {
	ref, ok, err := s.repo.Referrer(ctx, userID)
	if err != nil || !ok {
		return nil, err
	}
	return &ref.ID, nil
}

// InviteeCount 返回某人邀请来的人数。
func (s *Service) InviteeCount(ctx context.Context, referrerID uuid.UUID) (int64, error) {
	return s.repo.ReferralCount(ctx, referrerID)
}

// ReferralCount 返回全站邀请关系总数（后台佣金概览用）。
func (s *Service) ReferralCount(ctx context.Context) (int64, error) {
	return s.repo.TotalReferrals(ctx)
}
