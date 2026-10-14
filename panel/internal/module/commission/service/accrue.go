package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/commission/model"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// 转人工复核的原因（review_reason，库里限 200 字）。
const (
	reviewSameSource         = "推荐人与购买人来自同一来源地址" // 旧文案
	reviewSourceCheckFailed  = "同源检查失败，需人工确认推荐人与购买人是否同源"
	reviewSourceCheckMissing = "同源检查未装配，需人工确认推荐人与购买人是否同源"
	// reviewUnderfundedPrefix 是解冻时待结科目余额不足转复核的原因开头（后面跟金额与币种，见 settleOne）。
	reviewUnderfundedPrefix = "待结佣金科目余额不足"
)

// OnOrderPaid 实现 ordersvc.PaidHook：在订单结算事务里给推荐人计提一条冻结中的佣金。
// 没有推荐人、费率为 0、「首单」口径下不是首单、佣金截断后为 0、这一单已经计提过，都是正常情况，返回 nil。
//
// 锁序：order 调用时已持有平台收入科目；这里「首单」口径再加被推荐人咨询锁，记账只再锁推荐人的待结佣金科目
// （都排在平台收入之后），不碰套餐、券、订阅。
func (s *Service) OnOrderPaid(ctx context.Context, o ordersvc.PaidOrder) error {
	if !o.Total.IsPositive() || o.Kind == ordersvc.KindTopup {
		return nil
	}
	refs, err := s.needRefs()
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		referrer, err := refs.ReferrerOf(ctx, o.UserID)
		if err != nil || referrer == nil || *referrer == o.UserID {
			return err
		}
		cfg, err := s.settings.Commission(ctx)
		if err != nil {
			return err
		}
		if cfg.RatePercent <= 0 {
			return nil
		}
		// 支付回调重放、人工补结算时同一单可能再进来：先查，已有就不再记账
		exists, err := s.repo.EntryExists(ctx, o.OrderID, *referrer)
		if err != nil || exists {
			return err
		}
		if cfg.Scope == settingssvc.CommissionScopeFirstOrder {
			// 「首单」按佣金记录判：被推荐人已有别的订单计过佣，这一单就不再计。
			// 已作废（voided）的条目也算「已有首单」（用户 10-13 定）：首单佣金被复核作废后，第二单不再计佣。
			// 先锁被推荐人：不同币种的两张单不共用平台收入科目锁，不加这把锁会并发各计一条
			if err := s.repo.LockReferee(ctx, o.UserID); err != nil {
				return err
			}
			earlier, err := s.repo.RefereeHasOtherEntry(ctx, o.UserID, o.OrderID)
			if err != nil || earlier {
				return err
			}
		}
		rateBP := RateBP(cfg.RatePercent)
		amount := Amount(o.Total, rateBP)
		if !amount.IsPositive() {
			return nil
		}

		reviewReason, err := s.sourceReview(ctx, *referrer, o)
		if err != nil {
			return err
		}

		txnID, err := s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindCommissionAccrued,
			Currency:   o.Currency,
			SourceType: ledgersvc.SourceOrder,
			SourceID:   o.OrderID,
			Memo:       "分销佣金计提",
			Legs: []ledgersvc.Leg{
				{Account: ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefMain), Direction: ledgersvc.Debit, Amount: amount},
				{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionPending, *referrer), Direction: ledgersvc.Credit, Amount: amount},
			},
		})
		if err != nil {
			return err
		}

		paidAt := o.PaidAt
		if paidAt.IsZero() {
			paidAt = s.clock()
		}
		var paymentID *uuid.UUID
		if o.PaymentID != uuid.Nil {
			pid := o.PaymentID
			paymentID = &pid
		}
		// UNIQUE (order_id, referrer_user_id) 兜底：并发重放时第二笔撞索引，整笔结算回滚
		return s.repo.InsertEntry(ctx, &model.Entry{
			ReferrerUserID:   *referrer,
			RefereeUserID:    o.UserID,
			OrderID:          o.OrderID,
			OrderNo:          o.OrderNo,
			PaymentID:        paymentID,
			Currency:         string(o.Currency),
			BaseAmount:       o.Total,
			RateBP:           rateBP,
			CommissionAmount: amount,
			Status:           EntryPending,
			FrozenUntil:      paidAt.Add(time.Duration(cfg.FreezeDays) * 24 * time.Hour),
			ReviewRequired:   reviewReason != nil,
			ReviewReason:     reviewReason,
			AccrualTxnID:     txnID,
		})
	})
}

// sourceReview 做同源检查，返回复核原因（不需复核时为 nil）。
// 失败即关：没装配、查询出错都照样计提，但转人工复核、不自动解冻；查询放在保存点里，出错不连累结算事务。
// 只有保存点本身失败（事务已不可用）时返回错误。
func (s *Service) sourceReview(ctx context.Context, referrer uuid.UUID, o ordersvc.PaidOrder) (*string, error) {
	reason := func(r string) *string { return &r }
	if s.sources == nil {
		plog.From(ctx).Warn("commission: 同源检查未装配，佣金转人工复核", zap.String("order_id", o.OrderID.String()))
		return reason(reviewSourceCheckMissing), nil
	}
	var same bool
	probeErr, txErr := s.repo.Probe(ctx, func(ctx context.Context) error {
		var err error
		same, err = s.sources.SharesSource(ctx, referrer, o.UserID)
		return err
	})
	if txErr != nil {
		return nil, txErr
	}
	if probeErr != nil {
		plog.From(ctx).Warn("commission: 同源检查失败，佣金转人工复核",
			zap.String("order_id", o.OrderID.String()), zap.Error(probeErr))
		return reason(reviewSourceCheckFailed), nil
	}
	if same {
		return reason(reviewSameSource), nil
	}
	return nil, nil
}
