package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/commission/model"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/google/uuid"
)

// reviewSameSource 是同源复核的原因（旧文案）。
const reviewSameSource = "推荐人与购买人来自同一来源地址"

// OnOrderPaid 实现 ordersvc.PaidHook：在订单结算事务里给推荐人计提一条冻结中的佣金。
// 没有推荐人、费率为 0、「首单」口径下不是首单、佣金截断后为 0、这一单已经计提过，都是正常情况，返回 nil。
//
// 锁序：order 调用时已持有平台收入科目；这里记账只再锁推荐人的待结佣金科目（排在平台收入之后），不碰套餐、券、订阅。
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
			// 「首单」按佣金记录判：被推荐人已有别的订单计过佣，这一单就不再计
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

		var reviewReason *string
		if s.sources != nil {
			same, err := s.sources.SharesSource(ctx, *referrer, o.UserID)
			if err != nil {
				return err
			}
			if same {
				r := reviewSameSource
				reviewReason = &r
			}
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
