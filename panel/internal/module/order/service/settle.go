package service

import (
	"context"
	"errors"
	"fmt"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	"github.com/aegispanel/aegis/internal/module/order/repo"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// errAmountMismatch 是收款金额或币种与订单应付不符：普通错误，payment 整笔回滚（渠道会重推）。
var errAmountMismatch = errs.Newf(errs.Conflict, "收款金额与订单应付不符")

// SettlePayment 实现 paymentsvc.Settler：在 payment 入账的同一事务（与它开的保存点）里结算订单。
// 订单此刻不收这笔钱时返回 paymentsvc.NotPayable，由 payment 转挂账；其他错误整笔回滚。
func (s *Service) SettlePayment(ctx context.Context, in paymentsvc.SettleInput) error {
	o, ok, err := s.repo.OrderByID(ctx, in.OrderID, repo.ForNoKeyUpdate)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("order: 结算的订单不存在 %s", in.OrderID)
	}
	switch o.Status {
	case StatusPending:
	case StatusPaid, StatusFulfilled:
		return paymentsvc.NotPayable(paymentsvc.CaseExcessCapture, o.Status)
	case StatusCancelled, StatusExpired:
		return paymentsvc.NotPayable(paymentsvc.CaseReleasedOrder, o.Status)
	default:
		return fmt.Errorf("order: 订单状态 %q 不能结算", o.Status)
	}
	if string(in.Currency) != o.Currency || !o.PayableAmount.IsPositive() || !in.Amount.Equal(o.PayableAmount) {
		return errAmountMismatch
	}
	now := s.clock()

	if o.Kind == KindRenewal {
		if o.SubscriptionID == nil {
			return fmt.Errorf("order: 续费订单缺订阅 %s", o.ID)
		}
		sub, err := s.subs.Lock(ctx, *o.SubscriptionID, &o.UserID)
		if err != nil {
			return err
		}
		if !sub.StateAt(now).AcceptsChange() {
			return paymentsvc.NotPayable(paymentsvc.CaseIneligibleSubscription, o.Status)
		}
	}

	if err := s.postSettlement(ctx, o, in); err != nil {
		return err
	}
	if err := s.captureReservation(ctx, o, Actor{Kind: ActorPayment}); err != nil {
		return err
	}
	paid, err := s.markPaid(ctx, o, now)
	if err != nil {
		return err
	}
	if s.hook != nil && o.Kind != KindTopup && o.TotalAmount.IsPositive() {
		if err := s.hook.OnOrderPaid(ctx, PaidOrder{
			OrderID:   o.ID,
			OrderNo:   o.OrderNo,
			UserID:    o.UserID,
			Kind:      o.Kind,
			Currency:  money.Currency(o.Currency),
			Total:     o.TotalAmount,
			Paid:      in.Amount,
			PaidAt:    now,
			PaymentID: in.PaymentID,
		}); err != nil {
			return err
		}
	}
	if _, err := s.fulfil(ctx, paid, now, subsvc.Actor{Kind: subsvc.ActorPayment}); err != nil {
		return err
	}
	return s.audit.Write(ctx, auditsvc.Event{
		Action:       ActionPaymentSucceeded,
		ResourceType: ResourceOrder,
		ResourceID:   &o.ID,
		Data: map[string]any{
			"order_no":   o.OrderNo,
			"kind":       o.Kind,
			"amount":     in.Amount.StringFixed(2),
			"fee":        in.Fee.StringFixed(2),
			"currency":   o.Currency,
			"provider":   in.ProviderCode,
			"method":     in.Method,
			"payment_id": in.PaymentID.String(),
		},
	})
}

// postSettlement 记这笔收款的账：充值单记 balance_topup（借渠道净收款与手续费、贷用户余额，科目与 ledger 的 order_paid 一致）；
// 其他订单记 order_paid 并扣掉余额冻结。冻结已被释放说明订单正在被释放，转挂账 released_order。
func (s *Service) postSettlement(ctx context.Context, o model.Order, in paymentsvc.SettleInput) error {
	cur := money.Currency(o.Currency)
	if o.Kind == KindTopup {
		var legs []ledgersvc.Leg
		if net := in.Amount.Sub(in.Fee); net.IsPositive() {
			legs = append(legs, ledgersvc.Leg{
				Account:   ledgersvc.PlatformAccount(ledgersvc.ChannelCash, in.ProviderCode),
				Direction: ledgersvc.Debit,
				Amount:    net,
			})
		}
		if in.Fee.IsPositive() {
			legs = append(legs, ledgersvc.Leg{
				Account:   ledgersvc.PlatformAccount(ledgersvc.PlatformFeeExpense, in.ProviderCode),
				Direction: ledgersvc.Debit,
				Amount:    in.Fee,
			})
		}
		legs = append(legs, ledgersvc.Leg{
			Account:   ledgersvc.UserAccount(ledgersvc.UserBalance, o.UserID),
			Direction: ledgersvc.Credit,
			Amount:    in.Amount,
		})
		_, err := s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindBalanceTopup,
			Currency:   cur,
			SourceType: ledgersvc.SourceOrder,
			SourceID:   o.ID,
			Memo:       "余额充值",
			Legs:       legs,
		})
		return err
	}
	_, err := s.ledger.PostOrderPaid(ctx, ledgersvc.OrderPaid{
		OrderID:  o.ID,
		UserID:   o.UserID,
		Currency: cur,
		Total:    o.TotalAmount,
		Paid:     in.Amount,
		Fee:      in.Fee,
		Provider: in.ProviderCode,
	})
	if errors.Is(err, ledgersvc.ErrHoldReleased) {
		return paymentsvc.NotPayable(paymentsvc.CaseReleasedOrder, o.Status)
	}
	return err
}
