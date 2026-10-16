package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// grantInput 是一次发放的上下文：码已经转成 used，落点已经选定。
type grantInput struct {
	user            uuid.UUID
	codeID          uuid.UUID
	t               Template
	opt             Option
	placed          bool
	targetPermanent bool
	// prep 只对套餐卡有意义：码转 used 之前由 preparePlan 备好
	prep planPrep
	now  time.Time
}

// errPlanUnavailable 是套餐卡绑定的套餐已下架或没有发布版本：卡不能用，事务回滚、码不会被用掉。
var errPlanUnavailable = errs.Newf(errs.ValidationFailed, "这张卡绑定的套餐已下架，请联系客服")

// errPriceArchived 是套餐卡指向的价格档已归档（改过价，或迁移归档了不合新规则的价）：拒绝兑换，码不被用掉，
// 管理员在后台给模板重选在售价之后就能兑。
var errPriceArchived = errs.Newf(errs.ValidationFailed, "这张卡对应的套餐价格已下架，请联系客服")

// grant 按卡型发放。所有写入都经 subscription、ledger 的 service，加入本事务。
func (s *Service) grant(ctx context.Context, in grantInput) (granted, error) {
	if in.t.Type == TypePlan {
		return s.grantPlan(ctx, in)
	}
	r := in.t.Rewards
	g := granted{}
	reward := Prize{Balance: r.Balance, TrafficBytes: r.TrafficBytes, ExpireDays: r.ExpireDays}
	reset := r.ResetQuota
	if in.t.Type == TypeMystery {
		p, err := drawPrize(r.Pool)
		if err != nil {
			return granted{}, err
		}
		reward, reset, g.prizeLabel = p, false, p.Label
	}
	var subID *uuid.UUID
	if in.placed && in.opt.SubscriptionID != nil {
		id := *in.opt.SubscriptionID
		subID = &id
	}
	needsSub := reward.ExpireDays > 0 || reset || reward.TrafficBytes > 0
	if needsSub && subID == nil {
		// 落点已在 Redeem 里校验；走到这里是盲盒抽中了落点没覆盖到的奖励，属于程序缺陷
		return granted{}, ErrNoPlacement
	}
	if reward.Balance.IsPositive() {
		txnID, err := s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindGiftCardBalance,
			SourceType: ledgersvc.SourceGiftCardCode,
			SourceID:   in.codeID,
			Memo:       "礼品卡兑换：" + in.t.Name,
			Legs: []ledgersvc.Leg{
				{Account: ledgersvc.PlatformAccount(ledgersvc.PlatformPromoExpense, ledgersvc.RefGift), Direction: ledgersvc.Debit, Amount: reward.Balance},
				{Account: ledgersvc.UserAccount(ledgersvc.UserBalance, in.user), Direction: ledgersvc.Credit, Amount: reward.Balance},
			},
		})
		if err != nil {
			return granted{}, err
		}
		g.balance, g.ledgerTxn = reward.Balance, &txnID
		g.summary = append(g.summary, "余额 +"+formatAmount(reward.Balance))
	}
	actor := subsvc.Actor{Kind: subsvc.ActorUser, ID: &in.user}
	user := in.user
	if reward.ExpireDays > 0 {
		if in.targetPermanent {
			g.skippedDays = reward.ExpireDays
			g.subID = subID
			g.summary = append(g.summary, "「永久」套餐不加时长，"+strconv.Itoa(reward.ExpireDays)+" 天已跳过")
		} else {
			res, err := s.subs.Extend(ctx, subsvc.Extension{
				SubscriptionID: *subID,
				UserID:         &user,
				Days:           reward.ExpireDays,
				Actor:          actor,
				Source:         subsvc.ExtendSourceGiftCard,
			})
			if err != nil {
				return granted{}, err
			}
			g.days, g.subID = reward.ExpireDays, subID
			line := "订阅延长 " + strconv.Itoa(reward.ExpireDays) + " 天，到期 " + res.PeriodEnd.UTC().Format("2006-01-02")
			if res.Rescued {
				line = "订阅已恢复，" + line
			}
			g.summary = append(g.summary, line)
		}
	}
	if reset {
		if _, err := s.subs.ResetTraffic(ctx, subsvc.TrafficReset{
			SubscriptionID: *subID,
			UserID:         &user,
			Reason:         subsvc.ResetGiftCard,
			Note:           in.t.Name,
		}); err != nil {
			return granted{}, err
		}
		g.reset, g.subID = true, subID
		g.summary = append(g.summary, "本期流量已重置")
	}
	if reward.TrafficBytes > 0 {
		_, err := s.subs.GrantBonus(ctx, subsvc.BonusGrant{
			SubscriptionID: *subID,
			UserID:         &user,
			Bytes:          reward.TrafficBytes,
			Source:         subsvc.BonusGiftCard,
			Note:           in.t.Name,
		})
		if err != nil {
			if errors.Is(err, subsvc.ErrUnlimited) && in.t.Type == TypeMystery {
				g.skippedTraffic = reward.TrafficBytes
				g.subID = subID
				g.summary = append(g.summary, "这份不限流量，流量 +"+formatBytes(reward.TrafficBytes)+" 已跳过")
			} else {
				return granted{}, err
			}
		} else {
			g.traffic, g.subID = reward.TrafficBytes, subID
			g.summary = append(g.summary, "本期流量 +"+formatBytes(reward.TrafficBytes))
		}
	}
	if len(g.summary) == 0 {
		// 盲盒抽中「谢谢参与」一类什么都不送的奖：模板保存时已拒绝，这里兜底给一句话
		g.summary = append(g.summary, "已兑换")
	}
	return g, nil
}

// cardPlan 查套餐卡绑定的套餐。
func (s *Service) cardPlan(ctx context.Context, t Template) (catalogsvc.Plan, error) {
	if t.Rewards.PlanID == nil {
		return catalogsvc.Plan{}, errPlanUnavailable
	}
	planID := *t.Rewards.PlanID
	plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{planID})
	if err != nil {
		return catalogsvc.Plan{}, err
	}
	p, ok := plans[planID]
	if !ok {
		return catalogsvc.Plan{}, errPlanUnavailable
	}
	return p, nil
}

// planPrep 是套餐卡在码转 used 之前就备好的东西：要动的那份订阅已经锁住，换掉时剩余价值已经算好。
// 任何会拒绝兑换的检查（套餐下架、归档拒续、订阅不对、这份订阅上有没完结的续费或换套餐单）都在这一步，
// 失败时码还是 unused，事务回滚后什么都没动。
type planPrep struct {
	plan  catalogsvc.Plan
	sub   subsvc.Subscription
	value decimal.Decimal
	price subsvc.PriceSnapshot
}

// preparePlan 按选定的落点备好套餐卡的发放：价格档必须在售（归档回 errPriceArchived）；续费要求套餐未归档；叠加与新开、换掉要求在售。
// 换掉：锁订阅之后、动任何东西之前先调 RemainingValue —— order 在这份订阅有没完结的续费或换套餐单时
// 回 409（ErrOpenChange），原样往上抛，卡不会被用掉；没装折算时不给换掉。
func (s *Service) preparePlan(ctx context.Context, user uuid.UUID, t Template, plan catalogsvc.Plan, opt Option, now time.Time) (planPrep, error) {
	// 先做落点与套餐的检查（归档拒续、下架等原因更具体），再核价格档在售
	prep, err := s.preparePlanTarget(ctx, user, t, plan, opt, now)
	if err != nil {
		return planPrep{}, err
	}
	if prep.price, err = s.giftPrice(ctx, t.Rewards.PriceID); err != nil {
		return planPrep{}, err
	}
	return prep, nil
}

// preparePlanTarget 按落点锁订阅并做各自的拒绝检查（价格档在售由 preparePlan 随后核）。
func (s *Service) preparePlanTarget(ctx context.Context, user uuid.UUID, t Template, plan catalogsvc.Plan, opt Option, now time.Time) (planPrep, error) {
	r := t.Rewards
	if r.PlanID == nil {
		return planPrep{}, ErrNoPlacement
	}
	planID := *r.PlanID
	owner := user
	switch opt.Kind {
	case KindNew:
		p, err := s.planFor(ctx, plan)
		return planPrep{plan: p}, err
	case KindRenew:
		if opt.SubscriptionID == nil {
			return planPrep{}, ErrChoiceStale
		}
		sub, err := s.subs.Lock(ctx, *opt.SubscriptionID, &owner)
		if err != nil {
			return planPrep{}, err
		}
		if sub.PlanID != planID {
			return planPrep{}, ErrChoiceStale
		}
		if plan.Kind != catalogsvc.KindSubscription {
			return planPrep{}, ErrChoiceStale
		}
		if plan.Status == catalogsvc.PlanArchived {
			return planPrep{}, ErrRenewArchived
		}
		return planPrep{plan: plan, sub: sub}, nil
	case KindStack:
		if opt.SubscriptionID == nil {
			return planPrep{}, ErrChoiceStale
		}
		sub, err := s.subs.Lock(ctx, *opt.SubscriptionID, &owner)
		if err != nil {
			return planPrep{}, err
		}
		if sub.PlanID != planID || plan.Kind != catalogsvc.KindOneTime {
			return planPrep{}, ErrChoiceStale
		}
		p, err := s.planFor(ctx, plan)
		if err != nil {
			return planPrep{}, err
		}
		return planPrep{plan: p, sub: sub}, nil
	case KindChange:
		if s.valuer == nil || opt.SubscriptionID == nil {
			// 没有折算就不给「换掉」的选项；走到这里是落点与装配对不上
			return planPrep{}, ErrChoiceStale
		}
		sub, err := s.subs.Lock(ctx, *opt.SubscriptionID, &owner)
		if err != nil {
			return planPrep{}, err
		}
		if sub.PlanID == planID {
			return planPrep{}, ErrChoiceStale
		}
		value, _, err := s.valuer.RemainingValue(ctx, sub.ID, now)
		if err != nil {
			return planPrep{}, err
		}
		if value.IsNegative() {
			value = decimal.Zero
		}
		if err := money.Check(value); err != nil {
			return planPrep{}, errs.Wrap(errs.Internal, err)
		}
		p, err := s.planFor(ctx, plan)
		if err != nil {
			return planPrep{}, err
		}
		return planPrep{plan: p, sub: sub, value: value}, nil
	default:
		return planPrep{}, ErrChoiceStale
	}
}

// endText 格式化到期文字：永久套餐显示「永久有效」，其余显示「到期 YYYY-MM-DD」。
func endText(end time.Time) string {
	if subsvc.IsPermanent(end) {
		return "永久有效"
	}
	return "到期 " + end.UTC().Format("2006-01-02")
}

// grantPlan 发放套餐卡：续同款、叠加、换掉那一份或另开一份（检查都已在 preparePlan 里做完）。
// 价格快照的金额一律记 0：卡是送的，日后换套餐按已付价值退款时不能把赠送算成用户付过的钱。
func (s *Service) grantPlan(ctx context.Context, in grantInput) (granted, error) {
	r := in.t.Rewards
	if r.PlanID == nil || !in.placed {
		return granted{}, ErrNoPlacement
	}
	planID := *r.PlanID
	plan, sub := in.prep.plan, in.prep.sub
	actor := subsvc.Actor{Kind: subsvc.ActorUser, ID: &in.user}
	g := granted{planID: &planID}
	price := in.prep.price
	switch in.opt.Kind {
	case KindNew:
		created, err := s.subs.Provision(ctx, subsvc.Provision{
			UserID:        in.user,
			PlanID:        planID,
			PlanVersionID: *plan.CurrentVersionID,
			Price:         price,
			Actor:         actor,
		})
		if err != nil {
			return granted{}, err
		}
		g.planMode, g.subID = PlanGrantNew, &created.ID
		g.summary = append(g.summary, "已开通「"+plan.Name+"」，"+endText(created.PeriodEnd))
	case KindRenew:
		res, err := s.subs.Renew(ctx, subsvc.Renewal{
			SubscriptionID: sub.ID,
			UserID:         in.user,
			Price:          price,
			PaidAt:         in.now,
			Actor:          actor,
			Source:         redeemSourceGiftCard,
		})
		if err != nil {
			return granted{}, err
		}
		id := res.SubscriptionID
		g.planMode, g.subID = PlanGrantRenewed, &id
		g.summary = append(g.summary, "「"+plan.Name+"」已续期，"+endText(res.PeriodEnd))
	case KindStack:
		codeID := in.codeID
		res, err := s.subs.Stack(ctx, subsvc.Stacking{
			SubscriptionID: sub.ID,
			UserID:         in.user,
			PlanVersionID:  *plan.CurrentVersionID,
			Price:          price,
			PaidAt:         in.now,
			GiftCodeID:     &codeID,
			Actor:          actor,
			Source:         redeemSourceGiftCard,
		})
		if err != nil {
			return granted{}, err
		}
		id := res.SubscriptionID
		// 兑换流水的 plan_mode 只有 new/renewed/changed 三种，叠加记作 renewed
		g.planMode, g.subID = PlanGrantRenewed, &id
		g.summary = append(g.summary, "「"+plan.Name+"」已叠加，"+endText(res.PeriodEnd))
	case KindChange:
		if err := s.grantPlanChange(ctx, in, price, &g); err != nil {
			return granted{}, err
		}
	default:
		return granted{}, ErrChoiceStale
	}
	return g, nil
}

// grantPlanChange 把选中的那一份换成卡上的套餐，再把 preparePlan 算好的剩余价值全额退进余额。
func (s *Service) grantPlanChange(ctx context.Context, in grantInput, price subsvc.PriceSnapshot, g *granted) error {
	plan, sub, value := in.prep.plan, in.prep.sub, in.prep.value
	codeID, owner := in.codeID, in.user
	res, err := s.subs.ChangePlan(ctx, subsvc.PlanChange{
		SubscriptionID: sub.ID,
		UserID:         in.user,
		PlanID:         plan.ID,
		PlanVersionID:  *plan.CurrentVersionID,
		Price:          price,
		GiftCodeID:     &codeID,
		Actor:          subsvc.Actor{Kind: subsvc.ActorUser, ID: &owner},
	})
	if err != nil {
		return err
	}
	id := res.SubscriptionID
	g.planMode, g.subID = PlanGrantChanged, &id
	g.summary = append(g.summary, "已换成「"+plan.Name+"」，"+endText(res.PeriodEnd))
	if value.IsPositive() {
		if _, err := s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindPlanChangeRefund,
			SourceType: ledgersvc.SourceGiftCardCode,
			SourceID:   in.codeID,
			Memo:       "套餐卡换套餐，原套餐剩余价值退回",
			Legs: []ledgersvc.Leg{
				{Account: ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefMain), Direction: ledgersvc.Debit, Amount: value},
				{Account: ledgersvc.UserAccount(ledgersvc.UserBalance, in.user), Direction: ledgersvc.Credit, Amount: value},
			},
		}); err != nil {
			return err
		}
		g.refund = value
		g.summary = append(g.summary, "原套餐剩余价值 "+formatAmount(value)+" 已退回余额")
	}
	return nil
}

// planFor 校验套餐卡绑定的套餐：必须在售且有发布版本。
func (s *Service) planFor(_ context.Context, plan catalogsvc.Plan) (catalogsvc.Plan, error) {
	if plan.Status != catalogsvc.PlanActive || plan.CurrentVersionID == nil {
		return catalogsvc.Plan{}, errPlanUnavailable
	}
	return plan, nil
}

// giftPrice 给套餐卡算价格快照（金额恒为 0）。价格档已归档或不存在回 errPriceArchived。
func (s *Service) giftPrice(ctx context.Context, priceID *uuid.UUID) (subsvc.PriceSnapshot, error) {
	if priceID == nil {
		return subsvc.PriceSnapshot{}, errPlanUnavailable
	}
	prices, err := s.catalog.PricesByIDs(ctx, []uuid.UUID{*priceID})
	if err != nil {
		return subsvc.PriceSnapshot{}, err
	}
	pr, ok := prices[*priceID]
	if !ok || pr.Status != catalogsvc.PriceActive {
		return subsvc.PriceSnapshot{}, errPriceArchived
	}
	id := pr.ID
	return subsvc.PriceSnapshot{
		PriceID:       &id,
		Amount:        decimal.Zero,
		Interval:      pr.BillingInterval,
		IntervalCount: pr.IntervalCount,
		ValidityDays:  pr.ValidityDays,
	}, nil
}
