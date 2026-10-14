package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// ActionTrialGranted 是注册试用开通的审计动作。
const ActionTrialGranted = "subscription.trial_granted"

// GrantSignupTrial 给刚注册的用户开一份试用订阅（identity 在注册完成的同一事务里调用）。每人终身一次（subscription_trials 主键兜底）。
// 试用不建订单，所以不计佣、不进收入。配置不可用（套餐不存在或已归档、没有已发布的当前版本、hours 越界）时记 Warn 日志并跳过，返回 nil；
// 只有数据库等基础设施故障才返回错误（调用方会整笔回滚注册）。
func (s *Service) GrantSignupTrial(ctx context.Context, userID, planID uuid.UUID, hours int) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		if hours < 1 || hours > 8760 {
			plog.From(ctx).Warn("注册试用时长超出范围，跳过开通",
				zap.String("user_id", userID.String()),
				zap.Int("hours", hours))
			return nil
		}
		exists, err := s.repo.TrialExists(ctx, userID)
		if err != nil {
			return subErr(err)
		}
		if exists {
			return nil
		}

		plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{planID})
		if err != nil {
			return err
		}
		plan, ok := plans[planID]
		if !ok || plan.CurrentVersionID == nil || plan.Status == catalogsvc.PlanArchived {
			plog.From(ctx).Warn("注册试用套餐不存在、已归档或未配置当前版本，跳过开通",
				zap.String("user_id", userID.String()),
				zap.String("plan_id", planID.String()))
			return nil
		}
		v, err := s.versionFor(ctx, planID, *plan.CurrentVersionID)
		if err != nil {
			if errors.Is(err, ErrVersionUnavailable) {
				plog.From(ctx).Warn("注册试用套餐版本不可用，跳过开通",
					zap.String("user_id", userID.String()),
					zap.String("plan_id", planID.String()),
					zap.Error(err))
				return nil
			}
			return err
		}

		site, err := s.settings.Site(ctx)
		if err != nil {
			return err
		}

		intervalCount := (hours + 23) / 24
		if intervalCount < 1 {
			intervalCount = 1
		}
		snap := PriceSnapshot{
			PriceID:       nil,
			Currency:      money.Currency(site.Currency),
			Amount:        decimal.Zero,
			Interval:      "day",
			IntervalCount: intervalCount,
		}

		label, _ := NormalizeLabel(plan.Name)
		start := s.clock()
		end := start.Add(time.Duration(hours) * time.Hour).Truncate(time.Microsecond)

		m, err := s.provisionAt(ctx, Provision{
			UserID:        userID,
			PlanID:        planID,
			PlanVersionID: v.ID,
			Price:         snap,
			Actor:         Actor{Kind: ActorSystem},
		}, v, label, start, end, map[string]any{"source": "signup_trial", "hours": hours})
		if err != nil {
			return err
		}

		if err := s.repo.InsertTrial(ctx, &model.SubscriptionTrial{
			UserID:         userID,
			SubscriptionID: m.ID,
			PlanID:         planID,
			Hours:          hours,
		}); err != nil {
			return subErr(err)
		}

		return s.writeAudit(ctx, ActionTrialGranted, m.ID, map[string]any{
			"user_id":    userID.String(),
			"plan_id":    planID.String(),
			"hours":      hours,
			"period_end": end,
		})
	})
}
