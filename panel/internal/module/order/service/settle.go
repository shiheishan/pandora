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
)

// errAmountMismatch 是收款金额与订单应付不符：普通错误，payment 整笔回滚（渠道会重推）。
var errAmountMismatch = errs.Newf(errs.Conflict, "收款金额与订单应付不符")

// SettlePayment 实现 paymentsvc.Settler：在 payment 入账的同一事务（与它开的保存点）里结算订单。
// 订单此刻不收这笔钱时返回 paymentsvc.NotPayable，由 payment 转挂账；其他错误整笔回滚。
//
// 订单行套餐已归档（新购、续费、叠加、换套餐）回 NotPayable(CaseArchivedPlan)，payment 随后调 CloseArchivedOrder 关单、钱转余额。
//
// 锁序（与建单「订阅 → 套餐 / 券 → 科目」、取消「订单 → 券 → 用户科目」同向，避免 40P01）：
// 订单 → 订阅（续费、换套餐、叠加、流量重置）→ 预留（券）→ 账务科目（含平台收入）→ 订单状态 → PaidHook → 履约。
// 记账回 ErrHoldReleased 转 NotPayable 时，payment 回滚到保存点，前面已确认的预留一并撤销。
func (s *Service) SettlePayment(ctx context.Context, in paymentsvc.SettleInput) error {
	o, ok, err := s.repo.OrderByID(ctx, in.OrderID, repo.ForNoKeyUpdate)
	if err != nil {
		return err
	}
	if !ok {
		return errs.Newf(errs.NotFound, "订单不存在")
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
	if !o.PayableAmount.IsPositive() || !in.Amount.Equal(o.PayableAmount) {
		return errAmountMismatch
	}
	now := s.clock()

	// 买套餐的单（新购、续费、叠加、换套餐）：订单行套餐已归档就不开通，钱由 payment 转用户余额（用户 2026-10-16 定）。
	// 归档不取消待付单，所以「归档前下单、归档后付款」只能在这里挡；在锁订阅之前判断。
	var item model.Item
	if planOrder(o.Kind) {
		if item, err = s.onlyItem(ctx, o); err != nil {
			return err
		}
		archived, err := s.planArchivedFor(ctx, item)
		if err != nil {
			return err
		}
		if archived {
			return paymentsvc.NotPayable(paymentsvc.CaseArchivedPlan, o.Status)
		}
	}

	// 续费、换套餐、叠加、流量重置先锁目标订阅（履约时 Renew / ChangePlan / Stack 会再锁同一行，同一事务内不冲突）
	if o.Kind == KindRenewal || o.Kind == KindUpgrade || o.Kind == KindStack || o.Kind == KindTrafficReset {
		if o.SubscriptionID == nil {
			return fmt.Errorf("order: %s 订单缺订阅 %s", o.Kind, o.ID)
		}
		sub, err := s.subs.Lock(ctx, *o.SubscriptionID, &o.UserID)
		if err != nil {
			return err
		}
		// 续费、换套餐、叠加查订阅是否停用：停用了钱进挂账（流量重置另由 resetPayable 判）
		if o.Kind != KindTrafficReset && !sub.StateAt(now).AcceptsChange() {
			return paymentsvc.NotPayable(paymentsvc.CaseIneligibleSubscription, o.Status)
		}
		// 换套餐单冻结的剩余价值只对建单时的那一段周期成立：之后订阅被套餐卡换过套餐、或过期后被救回或续回（周期起点都会
		// 挪到那一刻），再按冻结的数换一次、退一次就重复了。周期起点晚于建单时刻即视为已变，钱进挂账。
		// 两个时刻都由应用时钟写入（订单 created_at 由 GORM 在插入时取，周期起点由 subscription 取 now）。
		if o.Kind == KindUpgrade && sub.PeriodStart.After(o.CreatedAt) {
			return paymentsvc.NotPayable(paymentsvc.CaseIneligibleSubscription, o.Status)
		}
		if o.Kind == KindRenewal || o.Kind == KindStack {
			if item.PlanID == nil || *item.PlanID != sub.PlanID {
				return paymentsvc.NotPayable(paymentsvc.CaseIneligibleSubscription, o.Status)
			}
		}
		// 流量重置单：订阅已不在生效中、已没有限额的流量配额、或已换成别的套餐版本（价钱按建单时的套餐定），钱进挂账
		if o.Kind == KindTrafficReset {
			payable, err := s.resetPayable(ctx, o, sub, now)
			if err != nil {
				return err
			}
			if !payable {
				return paymentsvc.NotPayable(paymentsvc.CaseIneligibleSubscription, o.Status)
			}
		}
	}

	if err := s.captureReservation(ctx, o, Actor{Kind: ActorPayment}); err != nil {
		return err
	}
	if err := s.postSettlement(ctx, o, in); err != nil {
		return err
	}
	paid, err := s.markPaid(ctx, o, now)
	if err != nil {
		return err
	}
	if s.hook != nil && o.Kind != KindTopup && o.TotalAmount.IsPositive() {
		// Paid 仍是 in.Amount（不含手续费，佣金基数不变）
		if err := s.hook.OnOrderPaid(ctx, PaidOrder{
			OrderID:   o.ID,
			OrderNo:   o.OrderNo,
			UserID:    o.UserID,
			Kind:      o.Kind,
			Total:     o.TotalAmount,
			Paid:      in.Amount,
			PaidAt:    now,
			PaymentID: in.PaymentID,
		}); err != nil {
			return err
		}
	}
	done, err := s.fulfil(ctx, paid, now, subsvc.Actor{Kind: subsvc.ActorPayment})
	if err != nil {
		return err
	}
	if err := s.emitOrderPaid(ctx, done, false); err != nil {
		return err
	}
	if err := s.emitSubscriptionProvisioned(ctx, done); err != nil {
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
			"surcharge":  in.Surcharge.StringFixed(2),
			"provider":   in.ProviderCode,
			"method":     in.Method,
			"payment_id": in.PaymentID.String(),
		},
	})
}

// postSettlement 记这笔收款的账：充值单记 balance_topup（借渠道净收款与手续费、贷用户余额，科目与 ledger 的 order_paid 一致）；
// 其他订单记 order_paid 并扣掉余额冻结。冻结已被释放说明订单正在被释放，转挂账 released_order。
func (s *Service) postSettlement(ctx context.Context, o model.Order, in paymentsvc.SettleInput) error {
	if o.Kind == KindTopup {
		// 充值只把应付记进余额，加收的手续费记手续费收入
		var legs []ledgersvc.Leg
		if net := in.Amount.Add(in.Surcharge).Sub(in.Fee); net.IsPositive() {
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
		if in.Surcharge.IsPositive() {
			legs = append(legs, ledgersvc.Leg{
				Account:   ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefSurcharge),
				Direction: ledgersvc.Credit,
				Amount:    in.Surcharge,
			})
		}
		_, err := s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindBalanceTopup,
			SourceType: ledgersvc.SourceOrder,
			SourceID:   o.ID,
			Memo:       "余额充值",
			Legs:       legs,
		})
		return err
	}
	_, err := s.ledger.PostOrderPaid(ctx, ledgersvc.OrderPaid{
		OrderID:   o.ID,
		UserID:    o.UserID,
		Total:     o.TotalAmount,
		Paid:      in.Amount,
		Fee:       in.Fee,
		Surcharge: in.Surcharge,
		Provider:  in.ProviderCode,
	})
	if errors.Is(err, ledgersvc.ErrHoldReleased) {
		return paymentsvc.NotPayable(paymentsvc.CaseReleasedOrder, o.Status)
	}
	return err
}
