package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/repo"
)

// MyNodes 是门户线路预览：本人一份生效中、有有效凭据的订阅能拿到的线路，只有名称、协议与倍率；永不为 nil。
// 不是本人的、不生效的、没有有效凭据的一律 ErrNotFound；线路来源没装配回 ErrDeliveryUnavailable。
func (s *Service) MyNodes(ctx context.Context, subID uuid.UUID) ([]NodePreview, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := s.loadSub(ctx, subID, &p.UserID, repo.NoLock)
	if err != nil {
		return nil, err
	}
	if StateAt(sub.Status, sub.CurrentPeriodEnd, sub.RenewalClosedAt, s.clock()) != StateLive {
		return nil, ErrNotFound
	}
	_, ok, err := s.repo.ActiveCredential(ctx, sub.ID)
	if err != nil {
		return nil, subErr(err)
	}
	if !ok {
		return nil, ErrNotFound
	}
	if s.nodes == nil {
		return nil, ErrDeliveryUnavailable
	}
	groupID, err := s.users.UserGroupID(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.nodes.DeliverableNodes(ctx, NodeQuery{
		SubscriptionID: sub.ID,
		UserID:         sub.UserID,
		PlanVersionID:  sub.PlanVersionID,
		UserGroupID:    groupID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]NodePreview, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, NodePreview{Name: n.Name, Protocol: n.Type, TrafficRate: n.TrafficRate})
	}
	return out, nil
}
