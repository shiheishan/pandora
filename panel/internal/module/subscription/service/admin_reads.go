package service

// 本文件实现 identity 后台用户列表、用户详情要的订阅数据端口（OpsSubscriptions.AdminSubs）。

import (
	"context"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// AdminSubs 批量取用户的全部订阅（后台用户列表、详情）；每个入参用户都在结果里。
func (s *Service) AdminSubs(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]identitysvc.AdminUserSubs, error) {
	if len(userIDs) == 0 {
		return map[uuid.UUID]identitysvc.AdminUserSubs{}, nil
	}
	summaries, _, err := s.summarize(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	onlineBySub := s.adminOnlineCounts(ctx, summaries)
	out := make(map[uuid.UUID]identitysvc.AdminUserSubs, len(userIDs))
	for _, uid := range userIDs {
		us := summaries[uid]
		subs := make([]identitysvc.AdminSub, 0, len(us.Subscriptions))
		for _, sm := range us.Subscriptions {
			subs = append(subs, adminSubFrom(sm, onlineBySub[sm.ID]))
		}
		var current *identitysvc.AdminSub
		if us.Current != nil {
			for i := range subs {
				if subs[i].ID == us.Current.ID {
					cur := subs[i]
					current = &cur
					break
				}
			}
		}
		out[uid] = identitysvc.AdminUserSubs{
			Subscriptions: subs,
			Current:       current,
		}
	}
	return out, nil
}

// adminOnlineCounts 收拢全部订阅 id，至多调用一次 OnlineDeviceCounts；失败或没装配时全部按 0。
func (s *Service) adminOnlineCounts(ctx context.Context, summaries map[uuid.UUID]UserSubscriptions) map[uuid.UUID]int {
	if s.online == nil {
		return map[uuid.UUID]int{}
	}
	ids := make([]uuid.UUID, 0)
	for _, us := range summaries {
		for _, sm := range us.Subscriptions {
			ids = append(ids, sm.ID)
		}
	}
	if len(ids) == 0 {
		return map[uuid.UUID]int{}
	}
	counts, err := s.online.OnlineDeviceCounts(ctx, ids)
	if err != nil {
		plog.From(ctx).Warn("读取在线设备数失败，按 0 显示", zap.Error(err))
		return map[uuid.UUID]int{}
	}
	return counts
}

func adminSubFrom(sm Summary, online int) identitysvc.AdminSub {
	quotas := make([]identitysvc.AdminQuota, 0, len(sm.Quotas))
	for _, q := range sm.Quotas {
		quotas = append(quotas, identitysvc.AdminQuota{
			Metric:    q.Metric,
			Period:    q.Period,
			Limit:     q.Limit,
			Consumed:  q.Consumed,
			Remaining: q.Remaining,
		})
	}
	return identitysvc.AdminSub{
		ID:                  sm.ID,
		Label:               sm.Label,
		PlanName:            sm.PlanName,
		PlanVersion:         sm.PlanVersion,
		Status:              sm.Status,
		Live:                sm.State == StateLive,
		PeriodStart:         sm.PeriodStart,
		PeriodEnd:           sm.PeriodEnd,
		Amount:              sm.Price.Amount,
		Quotas:              quotas,
		DeviceLimitOverride: sm.DeviceLimit,
		PlanMaxDevices:      sm.PlanMaxDevices,
		OnlineDevices:       online,
	}
}
