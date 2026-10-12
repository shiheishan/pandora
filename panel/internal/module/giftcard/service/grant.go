package service

import (
	"context"
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
	user   uuid.UUID
	codeID uuid.UUID
	t      Template
	opt    Option
	placed bool
	// targetLive 是落点那份订阅在选定时是否生效中
	targetLive bool
	now        time.Time
}

// errPlanUnavailable 是套餐卡绑定的套餐已下架或没有发布版本：卡不能用，事务回滚、码不会被用掉。
var errPlanUnavailable = errs.Newf(errs.ValidationFailed, "这张卡绑定的套餐已下架，请联系客服")

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
	// 流量包只能挂到生效中的订阅：落点是一份已过期的（加时长会先救回它）而这次没有加时长时，
	// 流量包留作未分配，免得盲盒抽中纯流量时整笔失败、回滚后重抽
	packSub := subID
	if !in.targetLive && reward.ExpireDays == 0 {
		packSub = nil
	}
	needsSub := reward.ExpireDays > 0 || reset
	if needsSub && subID == nil {
		// 落点已在 Redeem 里校验；走到这里是盲盒抽中了落点没覆盖到的奖励，属于程序缺陷
		return granted{}, ErrNoPlacement
	}
	if reward.Balance.IsPositive() {
		txnID, err := s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindGiftCardBalance,
			Currency:   in.t.Currency,
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
		g.summary = append(g.summary, "余额 +"+formatAmount(reward.Balance, in.t.Currency))
	}
	actor := subsvc.Actor{Kind: subsvc.ActorUser, ID: &in.user}
	user := in.user
	if reward.ExpireDays > 0 {
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
		if _, err := s.subs.GrantTrafficPack(ctx, subsvc.PackGrant{
			UserID:         in.user,
			SubscriptionID: packSub,
			Source:         subsvc.PackSourceGiftCard,
			SourceID:       in.codeID,
			Bytes:          reward.TrafficBytes,
		}); err != nil {
			return granted{}, err
		}
		g.traffic = reward.TrafficBytes
		if packSub != nil {
			g.subID = packSub
			g.summary = append(g.summary, "流量 +"+formatBytes(reward.TrafficBytes))
		} else {
			g.summary = append(g.summary, "流量 +"+formatBytes(reward.TrafficBytes)+"（暂存，开通订阅后自动加上）")
		}
	}
	if len(g.summary) == 0 {
		// 盲盒抽中「谢谢参与」一类什么都不送的奖：模板保存时已拒绝，这里兜底给一句话
		g.summary = append(g.summary, "已兑换")
	}
	return g, nil
}

// grantPlan 发放套餐卡：续同款、换掉那一份或另开一份。
// 价格快照的金额一律记 0：卡是送的，日后换套餐按已付价值退款时不能把赠送算成用户付过的钱。
func (s *Service) grantPlan(ctx context.Context, in grantInput) (granted, error) {
	r := in.t.Rewards
	if r.PlanID == nil || !in.placed {
		return granted{}, ErrNoPlacement
	}
	planID := *r.PlanID
	plan, err := s.planFor(ctx, planID)
	if err != nil {
		return granted{}, err
	}
	actor := subsvc.Actor{Kind: subsvc.ActorUser, ID: &in.user}
	g := granted{planID: &planID}
	switch in.opt.Kind {
	case KindNew:
		price, err := s.giftPrice(ctx, r.PriceID, nil)
		if err != nil {
			return granted{}, err
		}
		sub, err := s.subs.Provision(ctx, subsvc.Provision{
			UserID:        in.user,
			PlanID:        planID,
			PlanVersionID: *plan.CurrentVersionID,
			Price:         price,
			Actor:         actor,
		})
		if err != nil {
			return granted{}, err
		}
		g.planMode, g.subID = PlanGrantNew, &sub.ID
		g.summary = append(g.summary, "已开通「"+plan.Name+"」，到期 "+sub.PeriodEnd.UTC().Format("2006-01-02"))
	case KindRenew:
		owner := in.user
		sub, err := s.subs.Lock(ctx, *in.opt.SubscriptionID, &owner)
		if err != nil {
			return granted{}, err
		}
		if sub.PlanID != planID {
			return granted{}, ErrChoiceStale
		}
		price, err := s.giftPrice(ctx, r.PriceID, &sub.Price)
		if err != nil {
			return granted{}, err
		}
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
		g.summary = append(g.summary, "「"+plan.Name+"」已续期，到期 "+res.PeriodEnd.UTC().Format("2006-01-02"))
	case KindChange:
		if err := s.grantPlanChange(ctx, in, plan, &g); err != nil {
			return granted{}, err
		}
	default:
		return granted{}, ErrChoiceStale
	}
	return g, nil
}

// grantPlanChange 把选中的那一份换成卡上的套餐：先按换之前的状态算没用完的已付价值，换完全额退进余额。
func (s *Service) grantPlanChange(ctx context.Context, in grantInput, plan catalogsvc.Plan, g *granted) error {
	if s.valuer == nil {
		// 没有折算就不给「换掉」的选项；走到这里是落点与装配对不上
		return ErrChoiceStale
	}
	owner := in.user
	sub, err := s.subs.Lock(ctx, *in.opt.SubscriptionID, &owner)
	if err != nil {
		return err
	}
	if sub.PlanID == plan.ID {
		return ErrChoiceStale
	}
	value, cur, _, err := s.valuer.RemainingValue(ctx, sub.ID, in.now)
	if err != nil {
		return err
	}
	if value.IsNegative() {
		value = decimal.Zero
	}
	price, err := s.giftPrice(ctx, in.t.Rewards.PriceID, nil)
	if err != nil {
		return err
	}
	codeID := in.codeID
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
	g.summary = append(g.summary, "已换成「"+plan.Name+"」，到期 "+res.PeriodEnd.UTC().Format("2006-01-02"))
	if value.IsPositive() {
		if err := money.Check(value); err != nil {
			return err
		}
		if _, err := s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindPlanChangeRefund,
			Currency:   cur,
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
		g.refund, g.refundCur = value, cur
		g.summary = append(g.summary, "原套餐剩余价值 "+formatAmount(value, cur)+" 已退回余额")
	}
	return nil
}

// planFor 取套餐卡绑定的套餐：必须在售且有发布版本。
func (s *Service) planFor(ctx context.Context, planID uuid.UUID) (catalogsvc.Plan, error) {
	plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{planID})
	if err != nil {
		return catalogsvc.Plan{}, err
	}
	p, ok := plans[planID]
	if !ok || p.Status != catalogsvc.PlanActive || p.CurrentVersionID == nil {
		return catalogsvc.Plan{}, errPlanUnavailable
	}
	return p, nil
}

// giftPrice 给套餐卡算价格快照（金额恒为 0）。卡上有价格档用它的币种与周期；没有时续期沿用订阅自己的
// （fallback 非 nil），否则按站点币种 month×1（subscription 审查 N5）。
func (s *Service) giftPrice(ctx context.Context, priceID *uuid.UUID, fallback *subsvc.PriceSnapshot) (subsvc.PriceSnapshot, error) {
	if priceID != nil {
		prices, err := s.catalog.PricesByIDs(ctx, []uuid.UUID{*priceID})
		if err != nil {
			return subsvc.PriceSnapshot{}, err
		}
		pr, ok := prices[*priceID]
		if !ok {
			return subsvc.PriceSnapshot{}, errPlanUnavailable
		}
		id := pr.ID
		return subsvc.PriceSnapshot{PriceID: &id, Currency: pr.Currency, Amount: decimal.Zero, Interval: pr.BillingInterval, IntervalCount: pr.IntervalCount}, nil
	}
	if fallback != nil {
		p := *fallback
		p.Amount = decimal.Zero
		return p, nil
	}
	cur, err := s.ledger.DefaultCurrency(ctx)
	if err != nil {
		return subsvc.PriceSnapshot{}, err
	}
	return subsvc.PriceSnapshot{Currency: cur, Amount: decimal.Zero, Interval: "month", IntervalCount: 1}, nil
}
