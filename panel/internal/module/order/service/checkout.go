package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
)

// draft 是一张待建订单（在建单事务里组装好再交给 place）。
type draft struct {
	Kind           string
	UserID         uuid.UUID
	Subtotal       decimal.Decimal
	CouponPlanID   *uuid.UUID // 券适用范围：买套餐的订单给套餐 id；充值与流量重置不用券
	SubscriptionID *uuid.UUID
	Label          *string
	Item           *model.Item // 充值为 nil；OrderID 由 place 填
	Checkout       Checkout
	// ProrationCredit 是换套餐时原订阅的剩余价值（其余种类为 0）
	ProrationCredit decimal.Decimal
	// AllowWaive 为真时付不了的零头可以免（只有门户换套餐）
	AllowWaive bool
	// Manual 非 nil 是后台人工单：不用券、不用余额
	Manual *manualDraft
	Actor  Actor
	// AuditAction 为空时 place 不写审计（人工单在线下结清之后自己写）
	AuditAction string
	AuditData   map[string]any
}

// manualDraft 是人工单的部分：赠送全额减免；线下已收款不受最低额限制（建单后由调用方在同一事务里结清）。
type manualDraft struct {
	Grant     bool
	Offline   bool
	Reason    string
	CreatedBy uuid.UUID
}

// balanceFor 算一单用多少余额、还要付多少：
//
//	充值、应付为 0      不用余额
//	人工单              不用余额；待用户支付的应付低于最低额回 422（用户付不了），赠送与线下已收款不受限
//	门户                PlanBalance；换套餐的零头可以免（最多 0.99）；免不了又付不了回 422
func (s *Service) balanceFor(ctx context.Context, d draft, total decimal.Decimal) (BalancePlan, error) {
	if d.Kind == KindTopup || !total.IsPositive() {
		return BalancePlan{Applied: decimal.Zero, Payable: total}, nil
	}
	if d.Manual != nil {
		if !d.Manual.Grant && !d.Manual.Offline {
			minPay, err := s.payments.MinPayment(ctx)
			if err != nil {
				return BalancePlan{}, err
			}
			if manualDueBelowMinimum(total, minPay) {
				return BalancePlan{}, errManualBelowMinimum
			}
		}
		return BalancePlan{Applied: decimal.Zero, Payable: total}, nil
	}
	minPay, err := s.payments.MinPayment(ctx)
	if err != nil {
		return BalancePlan{}, err
	}
	requested := d.Checkout.UseBalance
	available := decimal.Zero
	// 余额开关打开，或应付低于最低额（Forced / BelowMinimum 与开关无关）时要知道余额
	if requested.IsPositive() || total.LessThan(minPay) {
		available, err = s.ledger.Balance(ctx, d.UserID)
		if err != nil {
			return BalancePlan{}, err
		}
	}
	bal := WaiveSmallDue(PlanBalance(total, available, requested, minPay), d.AllowWaive)
	if bal.BelowMinimum {
		return BalancePlan{}, ErrBelowMinimum
	}
	return bal, nil
}

// place 在调用方事务里落单，返回结果与落库后的订单行。
func (s *Service) place(ctx context.Context, d draft) (Created, model.Order, error) {
	now := s.clock()

	var discount decimal.Decimal
	var couponID *uuid.UUID
	var couponCode string
	if d.Kind != KindTopup && d.Manual == nil {
		couponCode = promosvc.NormalizeCode(d.Checkout.CouponCode)
		if d.Kind == KindTrafficReset {
			if couponCode != "" {
				return Created{}, model.Order{}, promosvc.ErrCouponNotForReset
			}
		} else if couponCode != "" {
			applied, err := s.promo.Evaluate(ctx, couponCode, promosvc.Target{
				UserID:   d.UserID,
				PlanID:   d.CouponPlanID,
				Subtotal: d.Subtotal,
			})
			if err != nil {
				return Created{}, model.Order{}, err
			}
			if applied != nil {
				discount = applied.Discount
				couponID = &applied.CouponID
			}
		}
	}

	// 人工赠送：新价全额减免（不带券）。不把原价记进 total：赠送没有收入
	if d.Manual != nil && d.Manual.Grant {
		discount = d.Subtotal
	}

	// total 是免零头之前的应付（报价的 total 同口径，expect 按它比）；免掉的零头单独记 waived_amount
	total := OrderTotal(d.Subtotal, discount, d.ProrationCredit)

	bal, err := s.balanceFor(ctx, d, total)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	// 线下已收款而应付为 0：没有东西可收，建单之前就拒（不先履约再整笔回滚）
	if d.Manual != nil && d.Manual.Offline && !bal.Payable.IsPositive() {
		if d.Kind == KindUpgrade {
			return Created{}, model.Order{}, errOfflineChangeNothingDue
		}
		return Created{}, model.Order{}, errOfflineNothingDue
	}
	if err := checkExpect(d.Checkout.Expect, total, bal, now); err != nil {
		return Created{}, model.Order{}, err
	}
	waived := bal.Waived

	orderNo, err := newOrderNo(now)
	if err != nil {
		return Created{}, model.Order{}, err
	}

	o := model.Order{
		OrderNo:               orderNo,
		UserID:                d.UserID,
		Kind:                  d.Kind,
		Status:                StatusPending,
		SubtotalAmount:        d.Subtotal,
		DiscountAmount:        discount,
		ProrationCreditAmount: d.ProrationCredit,
		WaivedAmount:          waived,
		TotalAmount:           total.Sub(waived),
		BalanceApplied:        bal.Applied,
		PayableAmount:         bal.Payable,
		PaidAmount:            decimal.Zero,
		CouponID:              couponID,
		SubscriptionID:        d.SubscriptionID,
		SubscriptionLabel:     d.Label,
		StateVersion:          1,
		ExpiresAt:             now.Add(PaymentWindow),
	}
	if d.Manual != nil {
		by := d.Manual.CreatedBy
		reason := d.Manual.Reason
		o.CreatedBy = &by
		o.ManualReason = &reason
	}

	if _, err := s.placeOrder(ctx, &o, d.Actor); err != nil {
		return Created{}, model.Order{}, err
	}

	if couponCode != "" {
		reserved, err := s.promo.Reserve(ctx, couponCode, promosvc.Target{
			UserID:   d.UserID,
			PlanID:   d.CouponPlanID,
			Subtotal: d.Subtotal,
		}, promosvc.OrderRef{ID: o.ID, No: o.OrderNo})
		if err != nil {
			return Created{}, model.Order{}, err
		}
		if reserved == nil {
			return Created{}, model.Order{}, ErrQuoteChanged
		}
		if couponID != nil && reserved.CouponID != *couponID {
			return Created{}, model.Order{}, ErrQuoteChanged
		}
		if !reserved.Discount.Equal(discount) {
			return Created{}, model.Order{}, ErrQuoteChanged
		}
		couponID = &reserved.CouponID
		discount = reserved.Discount
		o.CouponID = couponID
		o.DiscountAmount = discount
		if !OrderTotal(d.Subtotal, discount, d.ProrationCredit).Equal(total) {
			return Created{}, model.Order{}, ErrQuoteChanged
		}
	}

	if d.Item != nil {
		it := *d.Item
		it.OrderID = o.ID
		if err := s.repo.CreateItem(ctx, &it); err != nil {
			return Created{}, model.Order{}, err
		}
	}

	if bal.Payable.IsPositive() && bal.Applied.IsPositive() {
		_, err := s.ledger.HoldForOrder(ctx, ledgersvc.HoldInput{
			OrderID: o.ID,
			UserID:  d.UserID,
			Amount:  bal.Applied,
		})
		if err != nil {
			return Created{}, model.Order{}, err
		}
	}

	if err := s.emitOrderCreated(ctx, o); err != nil {
		return Created{}, model.Order{}, err
	}

	if bal.Payable.IsZero() {
		settled, err := s.settleZeroPay(ctx, o, d.Actor)
		if err != nil {
			return Created{}, model.Order{}, err
		}
		o = settled
	}

	if d.AuditAction != "" {
		auditData := map[string]any{
			"order_no": o.OrderNo,
			"kind":     d.Kind,
			"total":    o.TotalAmount.StringFixed(2),
		}
		if d.Kind == KindUpgrade {
			for k, v := range changeAuditData(o, total) {
				auditData[k] = v
			}
		}
		for k, v := range d.AuditData {
			auditData[k] = v
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       d.AuditAction,
			ResourceType: ResourceOrder,
			ResourceID:   &o.ID,
			Data:         auditData,
		}); err != nil {
			return Created{}, model.Order{}, err
		}
	}

	created := Created{
		OrderID:        o.ID,
		OrderNo:        o.OrderNo,
		Total:          o.TotalAmount,
		Discount:       o.DiscountAmount,
		BalanceApplied: o.BalanceApplied,
		Payable:        o.PayableAmount,
		Waived:         o.WaivedAmount,
		Status:         o.Status,
	}
	if d.Kind == KindUpgrade {
		created.Change = &ChangeAmounts{
			ProrationCredit: o.ProrationCreditAmount,
			BalanceRefund:   planChangeRefund(o.ProrationCreditAmount, o.SubtotalAmount, o.DiscountAmount),
		}
	}
	return created, o, nil
}

// changeAuditData 是换套餐单审计多记的几项：方向（免零头之前的应付为正是升级，否则降级）、
// 履约时退进余额的钱、免掉的零头（放弃的收入）。
func changeAuditData(o model.Order, total decimal.Decimal) map[string]any {
	direction := "downgrade"
	if total.IsPositive() {
		direction = "upgrade"
	}
	return map[string]any{
		"direction":        direction,
		"balance_refund":   planChangeRefund(o.ProrationCreditAmount, o.SubtotalAmount, o.DiscountAmount).StringFixed(2),
		"small_due_waived": o.WaivedAmount.StringFixed(2),
	}
}
