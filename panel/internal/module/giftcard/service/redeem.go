package service

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/giftcard/model"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// redeemSourceGiftCard 是套餐卡续期写进 renewed 事件的来源。
const redeemSourceGiftCard = "gift_card"

// Redeem 兑换一张卡（门户）。顺序不能调：锁码 → 校验状态与有效期 → 校验模板、领取条件与每人限次 →
// 按当前订阅重新算落点并校验用户的选择（在发放与抽奖之前，失败回 422、卡不会被用掉）→
// 码 unused → used（防重，WHERE status = 'unused'）→ 发放 → 写兑换流水 → 审计 → 幂等完成，全在一个事务里。
// 过期的码顺手改成 expired 并提交，再回 ErrCodeUnusable。
func (s *Service) Redeem(ctx context.Context, in RedeemInput) (RedeemResult, error) {
	user, err := portalUser(ctx)
	if err != nil {
		return RedeemResult{}, err
	}
	code, ok := normalizeCode(in.Code)
	if !ok {
		return RedeemResult{}, ErrCodeUnusable
	}
	hash := s.hashCode(code)
	expired := false
	var out RedeemResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		expired = false
		c, found, err := s.repo.CodeByHash(ctx, hash, true)
		if err != nil {
			return gcErr(err)
		}
		if !found || c.Status != CodeUnused {
			return ErrCodeUnusable
		}
		now := s.clock()
		if c.ExpiresAt != nil && !c.ExpiresAt.After(now) {
			// 顺手改成 expired：不改的话后台看不出哪些码已经作废。提交这一步，事务外再回不可用
			if _, err := s.repo.UpdateCodeStatus(ctx, c.ID, CodeUnused, CodeExpired, nil, nil); err != nil {
				return gcErr(err)
			}
			expired = true
			return nil
		}
		t, err := s.loadTemplate(ctx, c.TemplateID, false)
		if err != nil {
			return err
		}
		if t.Status != TemplateActive {
			return ErrTemplateInactive
		}
		subs := &userSubs{s: s, user: user}
		if err := s.checkConditions(ctx, user, t, subs); err != nil {
			return err
		}
		if t.Limits.MaxUsePerUser > 0 || t.Limits.CooldownHours > 0 {
			if err := s.repo.LockUserTemplate(ctx, user, t.ID); err != nil {
				return gcErr(err)
			}
			if err := s.checkLimits(ctx, user, t, now); err != nil {
				return err
			}
		}
		// 落点在发放与抽奖之前选定：同一张卡失败回滚再重试，抽奖结果不会因此换
		o := offerOf(t)
		var opt Option
		placed, targetLive := false, false
		if o.Kind != offerNone {
			us, err := subs.get(ctx)
			if err != nil {
				return err
			}
			cands := candidatesOf(us)
			opts, _ := placementOptions(o, cands, s.valuer != nil)
			if opt, placed, err = resolveChoice(in.Choice, opts); err != nil {
				return err
			}
			targetLive = placed && opt.SubscriptionID != nil && liveIn(cands, *opt.SubscriptionID)
			if !placed && !o.trafficOnly() {
				return ErrNoPlacement
			}
		}
		// 套餐卡的拒绝检查（含换掉时的剩余价值与未完结订单）都在码转 used 之前做完
		var prep planPrep
		if t.Type == TypePlan && placed {
			if prep, err = s.preparePlan(ctx, user, t, opt, now); err != nil {
				return err
			}
		}
		// 防重：码先转 used（与发放同一事务），并发兑同一张码的后到者在行锁后看到 used
		moved, err := s.repo.UpdateCodeStatus(ctx, c.ID, CodeUnused, CodeUsed, &user, &now)
		if err != nil {
			return gcErr(err)
		}
		if !moved {
			return ErrCodeUnusable
		}
		g, err := s.grant(ctx, grantInput{user: user, codeID: c.ID, t: t, opt: opt, placed: placed, targetLive: targetLive, prep: prep, now: now})
		if err != nil {
			return err
		}
		if err := s.repo.InsertRedemption(ctx, redemptionModel(c, t, user, g, now)); err != nil {
			return gcErr(err)
		}
		codeID := c.ID
		if err := s.writeAudit(ctx, ActionRedeemed, ResourceCode, &codeID, auditGranted(t, g)); err != nil {
			return err
		}
		out = g.result(t)
		if in.Reply == nil {
			return nil
		}
		return s.completeIdem(ctx, func() (idemsvc.Response, error) { return in.Reply(out) }, nil)
	})
	if err != nil {
		return RedeemResult{}, err
	}
	if expired {
		return RedeemResult{}, ErrCodeUnusable
	}
	return out, nil
}

// liveIn 报告候选里这份订阅是不是生效中。
func liveIn(cands []candidate, id uuid.UUID) bool {
	for _, c := range cands {
		if c.SubscriptionID == id {
			return c.State == StateLive
		}
	}
	return false
}

// userSubs 在一次兑换里只取一次用户的订阅摘要（领取条件与落点共用）。
type userSubs struct {
	s      *Service
	user   uuid.UUID
	loaded bool
	val    subsSnapshot
}

// subsSnapshot 是 subscription 的 UserSubscriptions（别名，免得本文件到处写包名）。
type subsSnapshot = subsvc.UserSubscriptions

func (u *userSubs) get(ctx context.Context) (subsSnapshot, error) {
	if u.loaded {
		return u.val, nil
	}
	v, err := u.s.subs.ByUser(ctx, u.user)
	if err != nil {
		return subsSnapshot{}, err
	}
	u.val, u.loaded = v, true
	return v, nil
}

// checkConditions 校验领取条件。付费记录与邀请关系没装配时宁可不让兑（ErrConditionUnavailable）。
func (s *Service) checkConditions(ctx context.Context, user uuid.UUID, t Template, subs *userSubs) error {
	c := t.Conditions
	if c.NewUserOnly || c.PaidUserOnly {
		if s.paid == nil {
			return ErrConditionUnavailable
		}
		paid, err := s.paid.HasPaidOrder(ctx, user)
		if err != nil {
			return err
		}
		if c.NewUserOnly && paid {
			return errs.Newf(errs.ValidationFailed, "这张卡只能新用户使用")
		}
		if c.PaidUserOnly && !paid {
			return errs.Newf(errs.ValidationFailed, "这张卡需要有过付费记录才能使用")
		}
	}
	if len(c.AllowedPlanIDs) > 0 {
		us, err := subs.get(ctx)
		if err != nil {
			return err
		}
		allowed := make(map[uuid.UUID]bool, len(c.AllowedPlanIDs))
		for _, id := range c.AllowedPlanIDs {
			allowed[id] = true
		}
		ok := false
		for _, sub := range us.Subscriptions {
			if allowed[sub.PlanID] && sub.State.AcceptsChange() {
				ok = true
				break
			}
		}
		if !ok {
			return errs.Newf(errs.ValidationFailed, "你当前的套餐不在这张卡的适用范围内")
		}
	}
	if c.RequireInvite {
		if s.refs == nil {
			return ErrConditionUnavailable
		}
		invited, err := s.refs.HasReferrer(ctx, user)
		if err != nil {
			return err
		}
		if !invited {
			return errs.Newf(errs.ValidationFailed, "这张卡只对通过邀请注册的用户开放")
		}
	}
	return nil
}

// checkLimits 校验每人次数与冷却；调用方已取 (用户, 模板) 的咨询锁，并发兑同一模板的不同码排队数。
func (s *Service) checkLimits(ctx context.Context, user uuid.UUID, t Template, now time.Time) error {
	used, last, err := s.repo.UserTemplateUsage(ctx, user, t.ID)
	if err != nil {
		return gcErr(err)
	}
	if t.Limits.MaxUsePerUser > 0 && used >= int64(t.Limits.MaxUsePerUser) {
		return errs.Newf(errs.ValidationFailed, "这个活动你已经兑换过了，每人限 "+strconv.Itoa(t.Limits.MaxUsePerUser)+" 次")
	}
	if t.Limits.CooldownHours > 0 && last != nil {
		next := last.Add(time.Duration(t.Limits.CooldownHours) * time.Hour)
		if next.After(now) {
			return errs.Newf(errs.ValidationFailed, "兑换太频繁了，请在 "+next.UTC().Format("2006-01-02 15:04")+"（UTC）之后再试")
		}
	}
	return nil
}

// granted 是一次兑换实际发出的东西（写兑换流水、审计与回执）。
type granted struct {
	prizeLabel string
	balance    decimal.Decimal
	ledgerTxn  *uuid.UUID
	traffic    int64
	days       int
	reset      bool
	planID     *uuid.UUID
	planMode   string
	subID      *uuid.UUID
	refund     decimal.Decimal
	refundCur  money.Currency
	summary    []string
}

// result 把发放结果做成给用户的回执。
func (g granted) result(t Template) RedeemResult {
	r := RedeemResult{
		TemplateName: t.Name,
		Type:         t.Type,
		PrizeLabel:   g.prizeLabel,
		Balance:      g.balance,
		Currency:     t.Currency,
		TrafficBytes: g.traffic,
		ExpireDays:   g.days,
		QuotaReset:   g.reset,
		Summary:      g.summary,
	}
	if g.planMode != "" {
		r.PlanGranted = t.Name
	}
	return r
}

// redemptionModel 把发放结果转成兑换流水行。
// redeemedAt 显式写兑换时刻（与 used_at、冷却判断同一个时钟）。
func redemptionModel(c model.Code, t Template, user uuid.UUID, g granted, redeemedAt time.Time) *model.Redemption {
	m := &model.Redemption{
		RedeemedAt:     redeemedAt,
		CodeID:         c.ID,
		TemplateID:     t.ID,
		UserID:         user,
		Currency:       string(t.Currency),
		BalanceAmount:  decPtr(g.balance),
		LedgerTxnID:    g.ledgerTxn,
		TrafficBytes:   int64Ptr(g.traffic),
		ExpireDays:     intPtr(g.days),
		QuotaReset:     g.reset,
		PlanID:         g.planID,
		SubscriptionID: g.subID,
		RefundAmount:   decPtr(g.refund),
	}
	if g.prizeLabel != "" {
		label := g.prizeLabel
		m.PrizeLabel = &label
	}
	if g.planMode != "" {
		mode := g.planMode
		m.PlanMode = &mode
	}
	if m.RefundAmount != nil {
		cur := string(g.refundCur)
		m.RefundCurrency = &cur
	}
	return m
}

// auditGranted 是兑换审计的 Data：发了什么（金额写成字符串），不记卡密。
func auditGranted(t Template, g granted) map[string]any {
	d := map[string]any{"template": t.Name, "type": t.Type, "currency": string(t.Currency)}
	if g.prizeLabel != "" {
		d["prize_label"] = g.prizeLabel
	}
	if g.balance.IsPositive() {
		d["balance"] = g.balance.StringFixed(money.Scale)
	}
	if g.traffic > 0 {
		d["traffic_bytes"] = g.traffic
	}
	if g.days > 0 {
		d["expire_days"] = g.days
	}
	if g.reset {
		d["quota_reset"] = true
	}
	if g.planMode != "" {
		d["plan_mode"] = g.planMode
	}
	if g.subID != nil {
		d["subscription_id"] = g.subID.String()
	}
	if g.refund.IsPositive() {
		d["refund"] = g.refund.StringFixed(money.Scale)
		d["refund_currency"] = string(g.refundCur)
	}
	return d
}

// MyRedemptions 门户「我的兑换记录」（最近 100 条）；卡密只给掩码。
func (s *Service) MyRedemptions(ctx context.Context) ([]MyRedemption, error) {
	user, err := portalUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.MyRedemptions(ctx, user, 100)
	if err != nil {
		return nil, gcErr(err)
	}
	out := make([]MyRedemption, 0, len(rows))
	for _, r := range rows {
		m := MyRedemption{
			TemplateName: r.TemplateName,
			Type:         r.Type,
			CodeHint:     r.CodeMask,
			Currency:     money.Currency(r.Currency),
			RedeemedAt:   r.RedeemedAt,
		}
		if r.PrizeLabel != nil {
			m.PrizeLabel = *r.PrizeLabel
		}
		if r.BalanceAmount != nil {
			m.Balance = *r.BalanceAmount
		}
		if r.TrafficBytes != nil {
			m.TrafficBytes = *r.TrafficBytes
		}
		if r.ExpireDays != nil {
			m.ExpireDays = *r.ExpireDays
		}
		out = append(out, m)
	}
	return out, nil
}
