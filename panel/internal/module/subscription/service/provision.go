package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// maxLabelTries 是开通时给备注名加序号去重的最多次数；试满仍撞名就不起名。
const maxLabelTries = 20

// Provision 开通一份新订阅：建 active 订阅（周期从现在起一整期；不取付款时刻，免得晚到的付款开出一份已过半的订阅）、按套餐版本建配额行、起备注名、
// 签发订阅链接凭据、写 activated 事件。
// 在 ctx 的事务里执行（没有就自己开）；提交后通知名单变化。
func (s *Service) Provision(ctx context.Context, in Provision) (Subscription, error) {
	if in.UserID == uuid.Nil {
		return Subscription{}, errs.Wrap(errs.Internal, errMissing("UserID"))
	}
	if err := validatePrice(in.Price); err != nil {
		return Subscription{}, err
	}
	if err := validateActor(in.Actor); err != nil {
		return Subscription{}, err
	}
	label, err := NormalizeLabel(in.Label)
	if err != nil {
		return Subscription{}, err
	}
	v, err := s.versionFor(ctx, in.PlanID, in.PlanVersionID)
	if err != nil {
		return Subscription{}, err
	}
	plan, err := s.planOf(ctx, in.PlanID)
	if err != nil {
		return Subscription{}, err
	}
	v = withKind(v, plan.Kind)
	start := s.clock()
	end := PeriodEndFor(start, in.Price)
	m, err := s.provisionAt(ctx, in, v, label, start, end, nil)
	if err != nil {
		return Subscription{}, err
	}
	return toSubscription(m), nil
}

// provisionAt 在 ctx 的事务里建一份 active 订阅：周期 [start, end)，按版本 v 建配额，起备注名、签凭据、写 activated 事件（payload 合并 extra）、
// 通知名单。调用方已校验 in 的价格、操作者与备注名。
func (s *Service) provisionAt(ctx context.Context, in Provision, v catalogsvc.Version, label string, start, end time.Time, extra map[string]any) (model.Subscription, error) {
	var out model.Subscription
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		m := model.Subscription{
			UserID:                in.UserID,
			PlanID:                in.PlanID,
			PlanVersionID:         in.PlanVersionID,
			PriceID:               in.Price.PriceID,
			Status:                StatusActive,
			CurrentPeriodStart:    start,
			CurrentPeriodEnd:      end,
			SnapshotAmount:        in.Price.Amount,
			SnapshotInterval:      in.Price.Interval,
			SnapshotIntervalCount: in.Price.IntervalCount,
			SourceOrderID:         in.SourceOrderID,
		}
		if err := s.repo.CreateSubscription(ctx, &m); err != nil {
			return subErr(err)
		}
		s.rtChanged(ctx, tableSubscriptions, realtime.OpInsert, m.ID, m.UserID)
		if err := s.repo.InsertQuotas(ctx, quotaRows(m.ID, v.Quotas, start, end)); err != nil {
			return subErr(err)
		}
		if label != "" {
			if err := s.provisionLabel(ctx, &m, label); err != nil {
				return err
			}
		}
		if err := s.issueCredential(ctx, m); err != nil {
			return err
		}
		payload := map[string]any{
			"plan_version_id": in.PlanVersionID,
			"period_start":    start,
			"period_end":      end,
		}
		for k, val := range extra {
			payload[k] = val
		}
		if err := s.writeEvent(ctx, m.ID, EventActivated, nil, StatusActive, in.Actor, in.SourceOrderID, payload); err != nil {
			return err
		}
		s.notifyRoster(ctx, m.ID)
		out = m
		return nil
	})
	return out, err
}

// quotaRows 按套餐版本的配额定义建一整套配额行，周期从 start 起，cycle 行跟到 cycleEnd。
func quotaRows(subID uuid.UUID, defs []catalogsvc.Quota, start, cycleEnd time.Time) []model.QuotaBalance {
	rows := make([]model.QuotaBalance, 0, len(defs))
	for _, q := range defs {
		rows = append(rows, model.QuotaBalance{
			SubscriptionID: subID,
			Metric:         q.Metric,
			Period:         q.Period,
			PeriodStart:    start,
			PeriodEnd:      QuotaPeriodEnd(q.Period, start, cycleEnd),
			LimitValue:     q.Limit,
		})
	}
	return rows
}

// provisionLabel 给新开的订阅起备注名：与本人另一份撞名时依次试「 2」「 3」…，试满仍撞就不起名。
func (s *Service) provisionLabel(ctx context.Context, m *model.Subscription, label string) error {
	for n := 1; n <= maxLabelTries; n++ {
		candidate := LabelWithSuffix(label, n)
		ok, err := s.repo.SetLabel(ctx, m.ID, m.UserID, &candidate)
		if err != nil {
			return subErr(err)
		}
		if ok {
			m.Label = &candidate
			return nil
		}
	}
	return nil
}
